# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

Remote Terminal — a self-hosted, single-binary PTY manager. Browser and iPhone Safari access to a terminal via HTTPS + WebSocket, designed to sit behind Cloudflare Tunnel. Windows-first, Linux later. One user, one machine, no database, no React, no npm build chain.

## Build & dev commands

```bash
# Build (outputs RT.exe on Windows, RT on Linux)
go build -ldflags="-s -w -X main.version=$(git describe --tags --always)" -o RT ./cmd/remote-terminal/

# Vet
go vet ./...

# Tidy dependencies (also generates go.sum)
go mod tidy

# Run (requires config.yaml, cert.pem, key.pem in working directory)
./RT
```

CI (`go mod tidy` then `go vet ./...` then `CGO_ENABLED=0 go build`) runs on every push/PR to `main`. Tagged commits (`v*`) trigger a GitHub Release with binaries for windows-amd64, windows-arm64, linux-amd64, linux-arm64.

## Architecture

### Module structure

Module `github.com/AHA1GE/RemoteTerminal` (Go 1.21). Five packages:

| Package | Path | Purpose |
|---|---|---|
| `assets` | `embed.go` (root) | `//go:embed web/*` — exposes `assets.FS` |
| `config` | `internal/config/config.go` | `Config` struct, YAML load/save, `ExeDir()`/`Path()` helpers |
| `pty` | `internal/pty/` | `CircularBuffer` with safe replay points, `PtySession` + `PtySessionStore` |
| `auth` | `internal/auth/blacklist.go` | `IPBlacklist` — 5-strike brute-force protection, `CF-Connecting-IP` support |
| `websocket` | `internal/websocket/handler.go` | WebSocket upgrade, PTY output streaming, input multiplexing |

Everything is wired in `cmd/remote-terminal/main.go` (753 lines).

### Request flow

```
Browser → HTTPS (TLS 1.2+, self-signed cert) → Go net/http mux
                                                  ├── /healthz          (public)
                                                  ├── /login            (public GET/POST)
                                                  ├── /app.js           (public, static)
                                                  ├── /ws/              (wsAuthMiddleware → 401)
                                                  ├── /, /terminal/*    (authMiddleware → redirect to /login)
                                                  └── /api/*, /logout   (apiAuthMiddleware → 401 JSON)
```

The router is hand-rolled in `setupRoutes()` using `http.NewServeMux`. WebSocket (`/ws/`) is registered directly on the root mux before the catch-all. Page and API routes use sub-muxes (`protectedPage`, `protectedAPI`) behind auth middleware.

### Auth model

- **Password setup**: On first run, config is generated with an empty `password_text` field. The user sets a plaintext password, restarts, and the binary hashes it (Argon2id), writes the hash to `password_hash`, and removes `password_text` from the file. To reset: add `password_text` back with a new value and restart.
- **Password verification**: Argon2id stored in `config.yaml` as `$argon2id$v=19$m=...,t=...,p=...$salt$hash`. Verified with `subtle.ConstantTimeCompare`.
- **Sessions**: 256-bit `crypto/rand` tokens, base64-encoded, stored in an in-memory `SessionStore` (mutex-protected map). Two cookies:
  - `session_token` — HttpOnly, Secure, SameSite=Strict
  - `csrf_token` — Secure, SameSite=Strict, **not** HttpOnly (JS must read it)
- **CSRF**: Double-submit cookie pattern. `ensureCSRF()` sets the cookie if absent; `validateCSRF()` compares body value vs cookie value with `ConstantTimeCompare`. Applied to all POST/DELETE routes.
- **IP blacklist**: 5 failed login attempts from the same IP → permanent blacklist in `blacklist.txt` (loaded on startup, appended on blacklist event). `CF-Connecting-IP` header read first, `RemoteAddr` fallback. Loopback (`127.0.0.1`, `::1`) never blacklisted.

### PTY sessions

`PtySessionStore` holds in-memory `PtySession` objects with a configurable `max_sessions` cap. Each session owns a `go-pty` PTY (ConPTY on Windows, POSIX openpt on Unix), a `CircularBuffer` ring buffer for output history, and a subscriber map for WebSocket output broadcast. A read-loop goroutine reads PTY output, writes to the ring buffer, and fans data to all connected WebSocket subscribers via non-blocking channel sends (slow subscribers are silently dropped and catch up via buffer replay on reconnect).

Session lifecycle: PTY lifetime is independent of browser lifetime. Browsers are clients; PTYs are server-owned resources. Disconnecting a browser leaves the PTY process alive. Reconnecting replays the ring buffer from the most recent safe replay point, then attaches the live stream. When the process exits, subscriber channels are closed, signaling WebSocket handlers to send close frames.

### Ring buffer with safe replay points

`CircularBuffer` tracks monotonic logical byte offsets that never wrap, so safe replay points survive physical buffer wraps. Recognized safe-replay ANSI sequences: `\x1b[H`, `\x1b[2J`, `\x1b[3J`, `\x1b[H\x1b[2J`, `\x1b]0;`. On reconnect, output is replayed from the most recent safe point to avoid feeding xterm.js a truncated escape sequence.

### WebSocket protocol

```
Browser → Server (binary):
  <raw bytes>       = keyboard input / paste (only if active connection)
  0x01 + {cols,rows} = resize event

Server → Browser (binary):
  <raw bytes>       = terminal output (buffer replay first, then live stream)
  Close(1000)        = process exited
```

- **Input multiplexing**: First connection to send input becomes active. Idle connections are deactivated after 10 seconds (tracked via `LastSeenAt` on the session). Non-active connections have input silently dropped.
- **Ping/pong**: Server pings every 30s. Read deadline is 40s (30s ping + 10s pong wait). Browsers auto-pong per RFC 6455 — no client code needed.
- **Buffer replay**: On WebSocket connect, the ring buffer is replayed from the latest safe replay point; if no safe point exists, all available bytes are replayed.

### Frontend (static, no build step)

Four files in `web/`, embedded at compile time via `assets.FS`:

| File | Served at | Behavior |
|---|---|---|
| `login.html` | `GET /login` | Password form, hidden CSRF input, viewport meta for mobile |
| `index.html` | `GET /` | Session table skeleton, "New Session" + "Logout" buttons |
| `terminal.html` | `GET /terminal/{id}` | Loads xterm.js 5.5.0 + fitAddon + webLinksAddon from CDN, then `app.js` |
| `app.js` | `GET /app.js` | Single IIFE, page-type-dispatching by `window.location.pathname` |

`app.js` handles all three page types: login (POST /login with CSRF), session list (fetch GET /api/sessions, render table, create/delete sessions), and terminal (WebSocket to `/ws/{id}` with binary ArrayBuffer receive, keyboard input forward, resize events as 0x01+JSON control frames, reconnect with 3s delay on non-1000 close).

### Configuration

`config.yaml` lives next to the binary. Seven fields:

| Field | Type | Default | Purpose |
|---|---|---|---|
| `listen` | string | `127.0.0.1:8443` | HTTPS listen address |
| `password_text` | string | (empty) | Plaintext password — hashed and removed on startup |
| `password_hash` | string | `<argon2id>` | Argon2id hash (set automatically from `password_text`) |
| `default_command` | []string | `[powershell.exe]` | Command launched in new PTY sessions |
| `max_sessions` | int | `32` | Maximum concurrent PTY sessions |
| `buffer_size` | int | `1048576` | Ring buffer size per session (1 MB) |
| `log_level` | string | `debug` | `debug`, `error`, or `none` |

Sample at `configs/config.sample.yaml`.

### Startup flow

1. Reject any CLI arguments (print message, exit 1)
2. Determine executable directory
3. Load `config.yaml`; if missing, generate default and exit with instructions
4. If `password_text` is set → hash it → write `password_hash` → remove `password_text` → save → continue
5. If `password_hash` is still placeholder and `password_text` is empty → ensure `password_text` field exists in config → print message → exit
6. Set log level from config
7. Validate `default_command` is executable via `exec.LookPath` (fatal if not found)
8. Load `cert.pem` + `key.pem` (fatal if missing or invalid)
9. Load `blacklist.txt` (missing file is OK, starts empty)
10. Print startup info (version, config path, cert path, listen addr, log level)
11. Start HTTPS server with TLS 1.2+
12. Wait for SIGINT/SIGTERM

### Graceful shutdown

On SIGINT/SIGTERM, in order:
1. Stop accepting new HTTP connections (5s timeout)
2. Close all PTYs (closing the file descriptor terminates child processes via ConPTY)
3. Close all WebSocket connections
4. Exit (auth session store and PTY session store die with the process)

### Logger

Custom structured JSON logger (~50 lines). Three levels: `debug`, `error`, `none`. Emits to stdout. Format: `{"time":"RFC3339","level":"...","msg":"...","key":"value",...}`. Uses a mutex for safe concurrent writes.

## Design constraints

- Single binary, static assets embedded via `go:embed`
- No database (all state in memory)
- No external runtime dependencies beyond Go standard library
- `cert.pem` + `key.pem` must be user-provided (no auto-generation)
- Binary accepts zero CLI arguments
- Hard-fault on fatal startup errors (log + `os.Exit(1)`); log-and-continue on runtime errors
- Windows-first via ConPTY (`aymanbagabas/go-pty`), Unix via POSIX openpt (same API)
- `password_text` field is the only supported way to set/reset passwords — no CLI tools, no random generation
- License: GPL-3.0
