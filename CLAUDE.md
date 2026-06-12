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

# Tidy dependencies
go mod tidy

# Run (requires config.yaml, cert.pem, key.pem in working directory)
./RT
```

CI (`go mod tidy` then `go vet ./...` then `CGO_ENABLED=0 go build`) runs on every push/PR to `main`. Tagged commits (`v*`) trigger a GitHub Release with binaries for windows-amd64, windows-arm64, linux-amd64, linux-arm64.

## Architecture

### Module structure

The Go module is `github.com/AHA1GE/RemoteTerminal` (Go 1.21). There are two packages:

- **Root `assets` package** (`embed.go`) — a single `//go:embed web/*` directive exposing `assets.FS`. Imported by `main` as a separate package (not dot-imported).
- **`internal/config/config.go`** — `Config` struct, YAML load/save, `ExeDir()` and `Path()` helpers. All paths are relative to the binary's directory.

Everything else currently lives flat in `cmd/remote-terminal/main.go` (636 lines). The plan calls for splitting into `internal/auth/`, `internal/pty/`, `internal/session/`, `internal/websocket/`, and `internal/web/` — none of these exist yet.

### Request flow (what exists today)

```
Browser → HTTPS (TLS 1.2+, self-signed cert) → Go net/http mux
                                                  ├── /healthz        (public)
                                                  ├── /login          (public GET/POST)
                                                  ├── /app.js         (public, static)
                                                  ├── /, /terminal/*  (authMiddleware → redirect to /login)
                                                  └── /api/*, /logout (apiAuthMiddleware → 401 JSON)
```

The router is hand-rolled in `setupRoutes()` using `http.NewServeMux` with a two-level dispatch: a top-level mux routes by path prefix, then sub-muxes (`protectedPage`, `protectedAPI`) handle exact path matching.

### Auth model

- **Password**: Argon2id stored in `config.yaml` as `$argon2id$v=19$m=...,t=...,p=...$salt$hash`. Verified with `subtle.ConstantTimeCompare`.
- **Sessions**: 256-bit `crypto/rand` tokens, base64-encoded, stored in an in-memory `SessionStore` (mutex-protected map). Two cookies:
  - `session_token` — HttpOnly, Secure, SameSite=Strict
  - `csrf_token` — Secure, SameSite=Strict, **not** HttpOnly (JS must read it)
- **CSRF**: Double-submit cookie pattern. `ensureCSRF()` sets the cookie if absent; `validateCSRF()` compares body value vs cookie value with `ConstantTimeCompare`. Applied to all POST/DELETE routes.

### Frontend (static, no build step)

Four files in `web/`, embedded at compile time via `assets.FS`:

| File | Served at | Behavior |
|---|---|---|
| `login.html` | `GET /login` | Password form, hidden CSRF input, viewport meta for mobile |
| `index.html` | `GET /` | Session table skeleton, "New Session" + "Logout" buttons |
| `terminal.html` | `GET /terminal/{id}` | Loads xterm.js 5.5.0 + fitAddon + webLinksAddon from CDN, then `app.js` |
| `app.js` | `GET /app.js` | Single IIFE, page-type-dispatching by `window.location.pathname` |

`app.js` handles all three page types. Login and session-list logic is complete. Terminal page initializes xterm.js correctly but has **placeholder code** for WebSocket — see gaps below.

### Configuration

`config.yaml` lives next to the binary. Six fields: `listen`, `password_hash`, `default_command` (string array), `max_sessions`, `buffer_size`, `log_level`. Sample at `configs/config.sample.yaml`.

Startup flow: reject CLI args → load config (generate default + exit if missing) → validate password_hash is set (generate random password + print hash + exit if placeholder) → load TLS cert/key (fatal if missing) → print help → start HTTPS.

### Logger

Custom structured JSON logger (~50 lines). Three levels: `debug`, `error`, `none`. Emits to stdout. Format: `{"time":"RFC3339","level":"...","msg":"...","key":"value",...}`. Uses a mutex for safe concurrent writes.

## What is NOT yet implemented

The auth layer, config, TLS, logger, CLI startup, and static asset serving are complete and match the plan in `implementplan.md`. The following subsystems are stubs or absent:

1. **PTY management** — `POST /api/sessions` returns 501. `go-pty` not in go.mod. No `PtySession` struct, no ConPTY integration, no process lifecycle.
2. **WebSocket** — `GET /ws/{id}` route not registered. `gorilla/websocket` not in go.mod. No upgrade handler, no ping/pong, no input/output streaming.
3. **Ring buffer** — No circular buffer type. `buffer_size` from config is unused. No safe replay points.
4. **Input multiplexing** — No active-connection tracking, no 10s deactivation timeout, no read-only enforcement.
5. **IP blacklist** — No `blacklist.txt`, no per-IP failure counter, no `CF-Connecting-IP` header parsing.
6. **Graceful shutdown** — Only step 1 (stop accepting HTTP) exists. Missing: PTY closure, child-process cleanup, WebSocket close, session-record removal.
7. **`app.js` terminal WebSocket layer** — xterm.js is initialized but `onData` is an empty callback, no `new WebSocket()`, no reconnect logic, no resize-dimension forwarding to server.
8. **`GET /api/sessions`** — Returns empty `[]` instead of real PTY session data.

See `implementplan.md` for the full specification of each subsystem.

## Design constraints

- Single binary, static assets embedded via `go:embed`
- No database (all state in memory)
- No external runtime dependencies
- `cert.pem` + `key.pem` must be user-provided (no auto-generation)
- Binary accepts zero CLI arguments
- Hard-fault on fatal startup errors (log + `os.Exit(1)`); log-and-continue on runtime errors
- Windows-first via ConPTY (`aymanbagabas/go-pty`), Linux via forkpty (same API)
- License: GPL-3.0
