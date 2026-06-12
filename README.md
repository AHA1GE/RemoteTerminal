# Remote Terminal

Self-hosted, single-binary remote terminal. Browser and iPhone Safari access to a shell over HTTPS + WebSocket. Designed to sit behind Cloudflare Tunnel.

Windows-first. No database. No React. No npm build chain.

## Quick start

You need three files in the same directory as the binary:

1. **`config.yaml`** — created automatically on first run
2. **`cert.pem`** + **`key.pem`** — a self-signed TLS certificate

```powershell
# 1. Generate a self-signed certificate (PowerShell)
New-SelfSignedCertificate -DnsName localhost -CertStoreLocation Cert:\CurrentUser\My `
  | Export-Certificate -FilePath cert.pem -Type CERT
# → export the key as key.pem (use openssl or the Certificates MMC snap-in)

# Or with openssl:
# openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 3650 -nodes -subj "/CN=localhost"

# 2. Run the binary — it generates config.yaml and exits
./RT

# 3. Edit config.yaml: set password_text to your passcode
#    password_text: mypassword

# 4. Run again — it hashes your password and starts
./RT
```

Open `https://127.0.0.1:8443` in a browser (accept the self-signed cert warning). Log in with your passcode, click "New Session", and you're in a terminal.

## Configuration

`config.yaml` lives next to the binary.

| Field | Type | Default | Purpose |
|---|---|---|---|
| `listen` | string | `127.0.0.1:8443` | HTTPS listen address |
| `password_text` | string | (empty) | Plaintext password — hashed and removed on startup |
| `password_hash` | string | `<argon2id>` | Argon2id hash (set automatically from `password_text`) |
| `default_command` | []string | `[powershell.exe]` | Command launched in new PTY sessions |
| `max_sessions` | int | `32` | Maximum concurrent PTY sessions |
| `buffer_size` | int | `1048576` | Ring buffer size per session (1 MB) |
| `log_level` | string | `debug` | `debug`, `error`, or `none` |

**Setting a password**: Add `password_text: yourpasscode` to config.yaml and restart. The binary hashes it, writes `password_hash`, and removes `password_text`.

**Changing a password**: Add `password_text: newpasscode` back to config.yaml (remove or keep `password_hash` — it will be overwritten) and restart.

## Cloudflare Tunnel

Create a tunnel pointed at `https://127.0.0.1:8443` with TLS verification disabled (self-signed cert):

```yaml
# config.yml (cloudflared)
tunnel: <tunnel-id>
credentials-file: /path/to/credentials.json

ingress:
  - hostname: terminal.example.com
    service: https://127.0.0.1:8443
    originRequest:
      noTLSVerify: true
  - service: http_status:404
```

The application remains tunnel-agnostic. It reads the real client IP from the `CF-Connecting-IP` header set by Cloudflare.

## Build from source

```bash
git clone https://github.com/AHA1GE/RemoteTerminal.git
cd RemoteTerminal
go mod tidy
go build -ldflags="-s -w -X main.version=$(git describe --tags --always)" -o RT ./cmd/remote-terminal/
```

Requires Go 1.21+. Cross-compiles to Windows and Linux (amd64 + arm64). `CGO_ENABLED=0` by default.

## Features

- **PTY sessions** — ConPTY on Windows, forkpty on Linux. Multiple concurrent sessions, configurable command.
- **WebSocket transport** — raw terminal stream, no JSON RPC layer. Text and binary frames both accepted.
- **Input multiplexing** — multiple browser tabs can watch the same session. Only one sends input at a time; idle connections deactivate after 10 seconds.
- **Ring buffer** — 1 MB per session. Replays output on reconnect from safe ANSI boundaries (clear screen, cursor home) to avoid garbled rendering.
- **Toolbar** — Esc (immediate), Ctrl and Alt (arm, then type the letter). Useful for Ctrl+C / Alt+combos on browsers that intercept those keys.
- **Brute-force protection** — 5 failed logins from an IP → permanent blacklist in `blacklist.txt`. Loopback never blacklisted.
- **Graceful shutdown** — SIGINT/SIGTERM stops HTTP, closes all PTYs (terminates child processes), closes WebSockets, exits cleanly.
- **Structured JSON logging** — three levels (`debug`, `error`, `none`) to stdout.
- **Single binary** — static assets embedded via `go:embed`. No external runtime dependencies.

## Architecture

```
Browser / Safari
        |
Cloudflare Tunnel (optional)
        |
HTTPS (self-signed cert, TLS 1.2+)
        |
Go net/http mux
        ├── /healthz            (public)
        ├── /login              (public GET/POST)
        ├── /app.js             (public, static)
        ├── /ws/{id}            (session_token cookie → 401)
        ├── /, /terminal/{id}   (auth → redirect to /login)
        └── /api/*, /logout     (auth → 401 JSON)
                |
        WebSocket (gorilla/websocket)
                |
        PTY (go-pty → ConPTY / forkpty)
                |
        Shell / CLI tools
```

## License

GPL-3.0. See [LICENSE](LICENSE).
