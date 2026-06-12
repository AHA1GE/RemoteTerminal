# Remote Terminal V1

## Objective

A self hosted, single binary remote terminal service for browser and iPhone Safari access.

Design priorities:

* Windows first
* Linux support later
* Single binary deployment
* No database
* No tmux
* No React
* No Next.js
* No npm build chain
* No Anthropic account dependency
* No subscription dependency
* Cloudflare Tunnel compatible
* One user
* One machine

---

# Architecture

```text
Browser / Safari
        |
Cloudflare Tunnel
        |
HTTPS (self-signed cert, local TLS termination)
        |
Go Binary
        |
WebSocket
        |
PTY
        |
User Process
        |
Shell / CLI Tools
```

The application is a generic PTY manager.

The application does not manage any specific CLI tool directly.

Any CLI tool is simply one possible command executed inside a PTY.

---

# Technology Stack

## Backend

Language:

```text
Go
```

Reasons:

* Excellent Windows support
* Excellent GitHub Actions support
* Easy cross compilation
* Mature PTY ecosystem
* Single executable output

Libraries:

```text
net/http
gorilla/websocket
aymanbagabas/go-pty
golang.org/x/crypto
gopkg.in/yaml.v3
```

---

## Frontend

Static assets only, embedded in the binary via Go `embed`.

Files:

```text
web/
├── login.html
├── index.html
├── terminal.html
├── app.js
```

Dependencies:

```text
xterm.js (CDN)
xterm.css (CDN)
xterm-addon-fit (CDN)
xterm-addon-web-links (CDN)
```

`app.js` handles three concerns:

1. **Login page** — read `csrf_token` cookie, submit password + CSRF token via `POST /login`, redirect to `/` on success
2. **Session list page** — fetch `GET /api/sessions` JSON, render session table, handle "New Session" button via `POST /api/sessions`, handle "Delete" button via `DELETE /api/sessions/{id}`
3. **Terminal page** — initialize xterm.js with fitAddon + webLinksAddon, open WebSocket to `/ws/{id}`, forward keyboard input to WebSocket, handle resize events, replay buffer on connect, reconnect on disconnect

No bundler.

No framework.

No build process.

Embedded at compile time:

```go
//go:embed web/*
var staticFiles embed.FS
```

---

# TLS

## Approach

TLS is terminated locally by the Go binary. A self-signed certificate is used.

Cloudflare Tunnel connects to the local HTTPS endpoint. The tunnel handles public DNS and remote access; the application owns its own TLS.

## Certificate Management

The user must provide a TLS certificate and key in the binary's directory:

```text
cert.pem
key.pem
```

The binary loads these files on startup. The certificate is NOT auto-generated — it must be placed by the user before starting the server.

If either file is missing or invalid, the binary logs a fatal error and exits.

The user can generate a self-signed certificate with any TLS tool (e.g. `openssl`, `mkcert`, or PowerShell's `New-SelfSignedCertificate`). Cloudflare Tunnel connects with TLS verification disabled, so a self-signed cert is sufficient.

## Listen Address

```text
127.0.0.1:8443
```

Cloudflare Tunnel is configured to connect to `https://127.0.0.1:8443` with TLS verification disabled (since the cert is self-signed).

---

# Authentication

## Method

Simple HTTPS login.

The user sets a plaintext password in `config.yaml`:

```yaml
password_text: mypassword
```

On startup, the binary hashes the value with Argon2id, writes the hash to `password_hash`, and removes `password_text` from the file. No CLI tools, no manual hash pasting. To change the password, add `password_text` back with a new value and restart.

After the first run, only the hash remains:

```yaml
password_hash: $argon2id$v=19$m=65536,t=3,p=2$...
```

The plaintext password exists in the config file only transiently — it is removed automatically after hashing.

---

## Login Flow

```text
Browser
    |
GET /login
    |
Server returns login form with CSRF token
    |
HTTPS POST /login (password + CSRF token)
    |
CSRF token validated
    |
Argon2id password verification
    |
session_token cookie issued
```

Two cookies are used:

**`session_token`** (authentication):

```text
HttpOnly
Secure
SameSite=Strict
Path=/
```

**`csrf_token`** (CSRF protection):

```text
Secure
SameSite=Strict
Path=/
```

`csrf_token` is intentionally NOT `HttpOnly` — the client JS reads it to include in POST request bodies.

---

## CSRF Protection

Double-submit cookie pattern.

The `csrf_token` cookie is set on first visit to any page (before login). The same token is used for all subsequent POST requests in that browser session.

**Serving a page with a form:**

1. If no `csrf_token` cookie exists on the request, generate a random 256-bit CSRF token via `crypto/rand`
2. Set the token as a cookie: `csrf_token=<value>; Path=/; Secure; SameSite=Strict`
3. Embed the same token as a hidden field in the form

**On any state-changing POST:**

1. Read CSRF token from request body
2. Read CSRF token from `csrf_token` cookie
3. Compare with `crypto/subtle.ConstantTimeCompare`
4. Reject (403) if they do not match

---

## Session ID Generation

Session IDs are generated using `crypto/rand`:

```go
func generateSessionID() (string, error) {
    b := make([]byte, 32)
    _, err := crypto.Read(b)
    if err != nil {
        return "", err
    }
    return base64.RawURLEncoding.EncodeToString(b), nil
}
```

256 bits of entropy. URL-safe base64 encoding.

---

## Logout

```text
POST /logout
```

Session invalidated immediately.

CSRF token validated on POST (same pattern as login).

---

# Session Model

## Core Principle

PTY lifetime is independent of browser lifetime.

Browser tabs are clients.

PTY sessions are server owned resources.

---

## PTY Session

```go
type PtySession struct {
    ID string

    CreatedAt time.Time
    LastSeenAt time.Time

    Process *os.Process
    Pty *os.File

    Width int
    Height int

    ActiveConnections int
    ActiveConnectionID string

    RingBuffer *CircularBuffer
}
```

Stored in memory only.

No database.

---

## Session Lifecycle

### Creation

User selects:

```text
New Session
```

Server:

```text
Create PTY
Launch configured command
Register session
```

---

### Disconnect

When browser disconnects:

```text
WebSocket closes
```

Server:

```text
Decrease connection count
Clear active connection if this was the active one
```

PTY remains alive.

Process remains alive.

---

### Reconnect

User opens:

```text
/terminal/{session-id}
```

Server:

```text
Replay output buffer
Attach live stream
```

---

### Exit

When process exits:

```text
exit
Ctrl+D
Process crash
```

Server:

```text
Close PTY
Remove session record
Free memory
```

---

# Input Multiplexing

Multiple browser tabs can connect to the same PTY simultaneously. Only one connection at a time may send input to the PTY.

## Active Connection

- The first connection to open a PTY becomes the **active** connection
- When the active connection sends input, its `LastInputAt` timestamp is updated
- If the active connection sends **no input for 10 seconds**, it is **deactivated**
- Once deactivated, the next connection that sends input becomes the new active connection

## Read-Only Connections

- All connections that are **not** the active connection are **read-only**
- Input received from a read-only connection is **silently dropped**
- Read-only connections still receive live terminal output and buffer replay

## Connection Tracking

```go
type PtyConnection struct {
    ID          string
    SessionID   string
    LastInputAt time.Time
    IsActive    bool
}
```

---

# Process Launch

## Configurable Command

Configuration:

```yaml
default_command:
  - powershell.exe
```

Alternative examples:

```yaml
default_command:
  - powershell.exe
```

```yaml
default_command:
  - cmd.exe
```

```yaml
default_command:
  - wsl
```

```yaml
default_command:
  - bash
```

Application never hardcodes any specific command.

---

# Output Buffer

## Purpose

Allow reconnect after:

```text
Network interruption
Safari refresh
Phone lock
Tunnel interruption
```

without losing terminal history.

---

## Design

Each PTY owns:

```text
Ring Buffer
```

Default size:

```text
1 MB
```

Configurable via `buffer_size` in config.

Pipeline:

```text
PTY Output
    |
    +--> Ring Buffer
    |
    +--> Live WebSocket
```

Reconnect:

```text
Replay Buffer
Then
Attach Live Stream
```

---

## Safe Replay Points

A raw ring buffer may wrap mid-ANSI-escape-sequence. Replaying from the oldest byte would feed xterm.js a truncated escape sequence, causing garbled rendering.

To avoid this, the buffer tracks **safe replay points** — byte offsets where the terminal state is known-clean. A safe replay point is recorded whenever one of the following sequences passes through the buffer:

```text
\x1b[H          Cursor home (full-screen repaint imminent)
\x1b[2J         Clear entire screen
\x1b[3J         Clear screen + scrollback
\x1b[H\x1b[2J   Clear screen and home (combined)
\x1b]0;         OSC title change (no rendering impact, safe boundary)
```

On reconnect, replay starts from the most recent safe replay point, skipping any unreadable partial escape sequences before it. If no safe replay point exists (buffer has never been cleared), replay starts from the oldest available byte.

---

# Browser UI

## Login Page

```text
+----------------------+
| Passcode             |
|                      |
| [ Login ]            |
+----------------------+
```

Includes CSRF token as hidden form field.

Includes viewport meta tag for mobile:

```html
<meta name="viewport" content="width=device-width, initial-scale=1.0, user-scalable=no">
```

---

## Session List Page

Served at `GET /` (`index.html`).

```text
+----------------------+
| Session A            |
| Session B            |
| Session C            |
|                      |
| [ New Session ]      |
+----------------------+
```

Each session row is a clickable link to `/terminal/{id}`.

Displays:

```text
Session ID
Creation Time
Last Activity
Running Status
```

Rendered client-side: `app.js` fetches `GET /api/sessions` and builds the DOM.

---

## Terminal Page

```text
+----------------------------------+
|                                  |
|                                  |
|            xterm.js              |
|                                  |
|                                  |
+----------------------------------+
```

Uses xterm.js addons:

```text
fitAddon       — responsive resize to container
webLinksAddon  — clickable/tappable URLs
```

No toolbar.

No menus.

No dashboard.

Terminal only.

Includes viewport meta tag for mobile:

```html
<meta name="viewport" content="width=device-width, initial-scale=1.0, user-scalable=no">
```

---

# WebSocket Protocol

## Authentication

On WebSocket upgrade, the server validates the `session_token` cookie:

1. Read `session_token` cookie from upgrade request headers
2. Look up session in memory
3. If invalid or missing, reject upgrade with HTTP 401
4. If valid, complete upgrade

No unauthenticated WebSocket connections are permitted.

---

## Browser → Server

```text
Keyboard Input
Paste
Resize Event
```

Input is only forwarded to the PTY if the connection is the active connection.

---

## Server → Browser

```text
Terminal Output
Exit Notification
```

Raw terminal stream.

No JSON RPC layer.

---

## Ping/Pong

The server sends a WebSocket ping frame every 30 seconds.

If no pong response is received within 10 seconds, the connection is considered dead and is closed.

On the client side, `app.js` relies on the browser's built-in WebSocket ping/pong handling — no application-level code is needed. Browsers automatically respond to ping frames with pong frames per RFC 6455.

This ensures:
- Silent disconnections are detected promptly (especially Safari on iPhone, which aggressively suspends background tabs)
- The active connection is released when a tab is backgrounded and stops responding
- Stale connections do not hold the active connection slot indefinitely

---

# HTTP Routes

All routes except `GET /login`, `POST /login`, and `GET /healthz` require a valid `session_token` cookie.

## Pages (serve embedded HTML)

```text
GET  /                — index.html (session list page)
GET  /login           — login.html (login form with CSRF token)
GET  /terminal/{id}   — terminal.html (xterm.js terminal)
```

## Health

```text
GET /healthz  — returns 200 OK (no auth required)
```

## API (JSON)

```text
POST /login            — verify password + CSRF token, issue session_token cookie
POST /logout           — invalidate session_token (CSRF protected)
GET  /api/sessions     — list all active PTY sessions as JSON
POST /api/sessions     — create new PTY session, returns session ID as JSON
DELETE /api/sessions/{id} — terminate PTY session (CSRF protected)
```

## WebSocket

```text
GET /ws/{id}  — WebSocket upgrade (validates session_token cookie)
```

---

# Windows Support

## PTY Backend

Use:

```text
ConPTY
```

through `aymanbagabas/go-pty`:

```text
https://pkg.go.dev/github.com/aymanbagabas/go-pty
```

This library provides a unified API across Windows (ConPTY) and Unix (POSIX openpt), so the same code works on both platforms.

Primary target platform.

---

# Linux Support

Future milestone.

Use:

```text
POSIX openpt (open("/dev/ptmx") + ioctl)
```

through `aymanbagabas/go-pty` — same API, no code changes needed.

No architectural changes required.

---

# Configuration

## File Location

The config file lives in the same directory as the binary:

```text
<binary-dir>/config.yaml
```

All platforms use this convention. No platform-specific config paths.

## Startup Behavior

See [CLI](#cli) for the full startup flow. In summary: on first run, the user sets `password_text` in config.yaml. The binary hashes it to `password_hash` and removes `password_text`. If `password_hash` is still the placeholder and `password_text` is empty, the binary adds an empty `password_text` field and exits with instructions. The plaintext password exists only transiently — it is removed automatically.

## Example

```yaml
listen: 127.0.0.1:8443

# Set on first run; the binary hashes it and removes this field.
password_text:

# Set automatically from password_text on first run.
password_hash: <argon2id>

default_command:
  - powershell.exe

max_sessions: 32

buffer_size: 1048576

log_level: debug
```

---

# Cloudflare Deployment

Application listens:

```text
127.0.0.1:8443 (HTTPS, self-signed cert)
```

Cloudflare Tunnel handles:

```text
Public DNS
Remote Access
```

Cloudflare Tunnel is configured to connect to `https://127.0.0.1:8443` with TLS verification disabled.

Application remains tunnel agnostic.

---

# Security

## Password Storage

```text
Argon2id
```

The password is stored as an Argon2id hash (`password_hash` field). The user sets a plaintext password in `password_text` on first run; the binary hashes it immediately and removes the plaintext field. No plaintext is stored persistently — `password_text` exists only transiently until the next startup.

---

## Session Security

`session_token` cookie flags:

```text
HttpOnly
Secure
SameSite=Strict
Path=/
```

`csrf_token` cookie flags:

```text
Secure
SameSite=Strict
Path=/
```

Session IDs are 256-bit random values (`crypto/rand`).

---

## CSRF Protection

Double-submit cookie pattern on all state-changing POST/DELETE requests (`POST /login`, `POST /logout`, `POST /api/sessions`, `DELETE /api/sessions/{id}`).

---

## Login Protection

After 5 failed login attempts from a single IP address, that IP is permanently blacklisted.

Blacklisted IPs are stored in `blacklist.txt` in the binary's directory:

```text
<binary-dir>/blacklist.txt
```

One IP address per line.

The file is loaded on startup. On each blacklist event, the new IP is appended to the file immediately.

The real client IP is read from the `CF-Connecting-IP` header set by Cloudflare Tunnel, falling back to the direct remote address if the header is absent.

The failed attempt counter is per-IP, in-memory, and resets on successful login or server restart.

---

## WebSocket Security

`session_token` cookie validated on every WebSocket upgrade. Invalid/missing → 401, upgrade rejected.

---

# Graceful Shutdown

On receiving `SIGINT` or `SIGTERM`:

1. Stop accepting new HTTP connections
2. Send `SIGHUP` to all child processes (via PTY close)
3. Wait up to 5 seconds for child processes to exit gracefully
4. Force-kill any remaining child processes
5. Close all WebSocket connections
6. Remove session records
7. Exit

```go
func (s *Server) Shutdown(ctx context.Context) error {
    // 1. Stop accepting new connections
    s.httpServer.Shutdown(ctx)

    // 2-4. Close all PTYs, terminate child processes
    for _, sess := range s.sessions {
        sess.Close(ctx)
    }

    // 5. Close WebSocket connections
    // 6. Clear session store
    s.sessions.Clear()

    return nil
}
```

---

# Debug Logging

Structured logging to stdout. Each log line is a JSON object.

## Format

```json
{"time":"2026-06-12T10:30:00Z","level":"debug","msg":"ptY created","session_id":"abc123"}
{"time":"2026-06-12T10:30:01Z","level":"debug","msg":"WebSocket connected","session_id":"abc123","conn_id":"xyz789"}
{"time":"2026-06-12T10:30:10Z","level":"error","msg":"ptY read error","session_id":"abc123","error":"broken pipe"}
```

## Levels

Configured via `log_level` in config.yaml. Three values:

```yaml
log_level: debug   # all messages
log_level: error   # errors only
log_level: none    # no output
```

What each level includes:

```text
debug — PTY lifecycle, buffer writes, connection state changes, input forwarding,
        auth events (login/logout), session create/delete, server start/stop,
        blacklist hits, dropped input, reconnect attempts, ping/pong timeouts
error — PTY failures, WebSocket errors, process crashes, login failures,
        IP blacklisted
none  — no log output
```

## Implementation

```go
type LogLevel int

const (
    LogNone  LogLevel = iota // no output
    LogError                 // errors only
    LogDebug                 // all messages
)

type Logger struct {
    mu     sync.Mutex
    level  LogLevel
    writer io.Writer // os.Stdout
}

func (l *Logger) Debug(msg string, fields map[string]interface{}) {
    if l.level < LogDebug {
        return
    }
    l.emit("debug", msg, fields)
}

func (l *Logger) Error(msg string, fields map[string]interface{}) {
    if l.level < LogError {
        return
    }
    l.emit("error", msg, fields)
}

func (l *Logger) emit(level, msg string, fields map[string]interface{}) {
    fields["time"] = time.Now().UTC().Format(time.RFC3339)
    fields["level"] = level
    fields["msg"] = msg
    b, _ := json.Marshal(fields)
    l.mu.Lock()
    fmt.Fprintln(l.writer, string(b))
    l.mu.Unlock()
}
```

No external logging library. ~40 lines of Go.

---

# Error Handling

Hard-fault on fatal errors. Log and exit. No recovery, no retry.

## Fatal Errors (exit non-zero)

On these errors, log at `error` level and call `os.Exit(1)`:

```text
- config.yaml not found
- password not configured (password_text is empty and password_hash is still the placeholder)
- TLS certificate not found or invalid (cert.pem + key.pem must exist in binary directory)
- default_command not found (binary not on PATH)
- listen address already in use
- embedded static files fail to load (compile-time issue, should never happen)
```

The process manager (systemd, launchd, Docker, or manual restart) restarts the binary.

## Runtime Errors (log only, do not exit)

On these errors, log and continue:

```text
- PTY creation fails for a specific session        → error response to API caller
- PTY read/write error                             → close the session, log error
- WebSocket write fails (client disconnected)       → close connection, log debug
- Argon2id verification fails (bad password)        → log debug, return 401
- Login attempt from blacklisted IP                → log debug, return 403
- Input received from read-only connection          → log debug, drop input
```

The server process itself does not exit on runtime errors.

## Startup vs Runtime

```text
Startup errors   → fatal, log, exit
Runtime errors   → log, continue serving
SIGINT/SIGTERM   → graceful shutdown (see Graceful Shutdown section)
```

---

# CLI

## Startup Flow

When the binary runs, it executes in this order:

```
1. If len(os.Args) > 1
   → The binary takes no arguments
   → Print: "remote-terminal takes no arguments. Place config.yaml in the same directory and run without arguments."
   → Exit (non-zero)

2. Look for config.yaml in the binary's directory
   ├── Not found:
   │   → Generate default config file
   │   → Print: "Config file created: <binary-dir>/config.yaml"
   │   → Print: "Edit this file, then restart."
   │   → Exit (non-zero)
   │
   ├── Found with password_text set to a non-empty value:
   │   → Hash the plaintext with Argon2id
   │   → Write hash to password_hash
   │   → Remove password_text from config
   │   → Save config
   │   → Continue to step 3
   │
   ├── Found but password_hash is empty/placeholder AND password_text is empty:
   │   → Ensure password_text field exists in config
   │   → Save config
   │   → Print: "Password not configured."
   │   → Print: "Edit <binary-dir>/config.yaml, set password_text, and restart."
   │   → Exit (non-zero)
   │
   └── Found with valid password_hash and no password_text:
       → Continue to step 3

3. Look for cert.pem + key.pem in the binary's directory
   ├── Not found or invalid:
   │   → Log fatal error
   │   → Print: "Place cert.pem and key.pem in the binary directory and restart."
   │   → Exit (non-zero)
   │
   └── Found and valid:
       → Load and use them

4. Validate default_command is executable via LookPath (fatal if not found)

5. Load blacklist.txt (missing file is OK)

6. Print help info to stdout

7. Start HTTPS server
```

## Help Output

Printed to stdout on successful startup:

```text
remote-terminal v1.0.0

Config:   <binary-dir>/config.yaml
Cert:     <binary-dir>/cert.pem
Key:      <binary-dir>/key.pem
Listen:   127.0.0.1:8443
Log level: debug
```

## No CLI Options

The binary accepts no arguments, no flags, and no subcommands. If any argument is passed, it prints a message and exits.

---

# Repository Layout

```text
/
├── cmd/
│   └── remote-terminal/
│       └── main.go
├── internal/
│   ├── auth/
│   │   └── blacklist.go
│   ├── config/
│   │   └── config.go
│   ├── pty/
│   │   ├── circular.go
│   │   └── session.go
│   └── websocket/
│       └── handler.go
│
├── web/
│   ├── login.html
│   ├── index.html
│   ├── terminal.html
│   └── app.js
│
├── embed.go
├── configs/
│   └── config.sample.yaml
│
├── .github/
│   └── workflows/
│       └── build.yml
│
├── go.mod
└── go.sum
```

---

# CI/CD

GitHub Actions.

Build matrix:

```text
windows-amd64
windows-arm64
linux-amd64
linux-arm64
```

Artifacts published automatically on tagged releases.

---

# V1 Acceptance Criteria

* Login page loads at GET /login with CSRF token
* Login works (POST /login verifies password, issues session_token cookie)
* Logout works (POST /logout invalidates session_token)
* CSRF protection works (double-submit cookie on all state-changing requests)
* session_token cookie: HttpOnly, Secure, SameSite=Strict
* csrf_token cookie: Secure, SameSite=Strict (NOT HttpOnly, JS-readable)
* Session IDs are crypto/rand 256-bit
* Session list page loads at GET / (index.html, renders from GET /api/sessions JSON)
* New session creation works (POST /api/sessions)
* Session deletion works (DELETE /api/sessions/{id})
* PTY creation works
* ConPTY works on Windows (via go-pty)
* Terminal page loads at GET /terminal/{id} with xterm.js
* Terminal resize works (fitAddon)
* Clickable URLs work (webLinksAddon)
* Clipboard paste works
* WebSocket connects at /ws/{id} with session_token validation
* WebSocket authentication rejects invalid/missing session_token (401)
* Session survives browser disconnect (PTY remains alive)
* Input deactivation after 10s idle works
* Read-only enforcement on non-active connections works
* Output replay works from ring buffer on reconnect
* Multiple PTY sessions work
* CLI tools run correctly inside a PTY (shell, editors, REPLs)
* Safari works (desktop and iPhone)
* Mobile viewport meta renders correctly on iPhone
* TLS cert loaded from cert.pem + key.pem (user-provided, no auto-generation)
* Cloudflare Tunnel works (connects to https://127.0.0.1:8443)
* Graceful shutdown on SIGINT/SIGTERM
* Structured JSON logging to stdout (debug/error/none via log_level config)
* Startup help printed on successful launch
* Default config generated if none found, binary exits with instructions
* Binary hashes password_text to password_hash and removes password_text on startup
* Fatal errors log and exit; runtime errors log and continue
* No CLI arguments, flags, or subcommands accepted
* Single executable deployment (static assets embedded via go:embed)
* No external runtime dependencies
* Health check endpoint returns 200 at GET /healthz (no auth)
* WebSocket ping every 30s, dead connections closed after 10s no pong
* Ring buffer replays from most recent safe replay point on reconnect
* IP blacklisted permanently after 5 failed login attempts
* blacklist.txt written to binary directory, loaded on startup
* CF-Connecting-IP header used for client IP, with direct-address fallback
