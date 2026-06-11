# Claude Remote Terminal V1

## Objective

A self hosted, single binary remote terminal service primarily intended for Claude Code access from browsers and iPhone Safari.

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
Go Binary
        |
WebSocket
        |
PTY
        |
User Process
        |
Claude / Shell / Other CLI
```

The application is a generic PTY manager.

The application does not manage Claude Code directly.

Claude Code is simply one possible command executed inside a PTY.

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
creack/pty
golang.org/x/crypto
```

---

## Frontend

Static assets only.

Files:

```text
index.html
login.html
terminal.html
app.js
```

Dependencies:

```text
xterm.js (CDN)
xterm.css (CDN)
```

No bundler.

No framework.

No build process.

---

# Authentication

## Method

Simple HTTPS login.

Configuration stores:

```yaml
password_hash: <argon2id hash>
```

Generated through:

```bash
claude-remote hash-password
```

---

## Login Flow

```text
Browser
    |
HTTPS POST /login
    |
Argon2id verification
    |
Session cookie issued
```

Cookie flags:

```text
HttpOnly
Secure
SameSite=Strict
```

---

## Logout

```text
POST /logout
```

Session invalidated immediately.

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
```

PTY remains alive.

Process remains alive.

Claude remains alive.

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

# Process Launch

## Configurable Command

Configuration:

```yaml
default_command:
  - pwsh.exe
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
  - claude
```

```yaml
default_command:
  - bash
```

Application never hardcodes Claude.

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

Initial target:

```text
1 MB
```

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

# Browser UI

## Login Page

```text
+----------------------+
| Passcode             |
|                      |
| [ Login ]            |
+----------------------+
```

---

## Session List Page

```text
+----------------------+
| Session A            |
| Session B            |
| Session C            |
|                      |
| [ New Session ]      |
+----------------------+
```

Displays:

```text
Session ID
Creation Time
Last Activity
Running Status
```

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

No toolbar.

No menus.

No dashboard.

Terminal only.

---

# WebSocket Protocol

## Browser → Server

```text
Keyboard Input
Paste
Resize Event
```

---

## Server → Browser

```text
Terminal Output
Exit Notification
```

Raw terminal stream.

No JSON RPC layer.

---

# HTTP Routes

## Authentication

```text
GET  /login
POST /login
POST /logout
```

---

## Sessions

```text
GET    /
GET    /session/list
POST   /session/new
DELETE /session/{id}
```

---

## Terminal

```text
GET /terminal/{id}
GET /ws/{id}
```

---

# Windows Support

## PTY Backend

Use:

```text
ConPTY
```

through Go PTY library.

Primary target platform.

---

# Linux Support

Future milestone.

Use:

```text
forkpty()
```

through same abstraction.

No architectural changes required.

---

# Configuration

Example:

```yaml
listen: 127.0.0.1:8080

password_hash: <argon2id>

default_command:
  - pwsh.exe

max_sessions: 32

buffer_size: 1048576
```

---

# Cloudflare Deployment

Application listens:

```text
127.0.0.1:8080
```

Cloudflare Tunnel handles:

```text
TLS
Public DNS
Remote Access
```

Application remains tunnel agnostic.

---

# Security

## Password Storage

```text
Argon2id
```

only.

No plaintext storage.

---

## Session Security

```text
HttpOnly
Secure
SameSite=Strict
```

cookies.

---

## Login Protection

Per-IP rate limiting:

```text
5 failed attempts
```

Lockout:

```text
5 minutes
```

---

# Repository Layout

```text
/
├── cmd/
├── internal/
│   ├── auth/
│   ├── config/
│   ├── pty/
│   ├── session/
│   ├── websocket/
│   └── web/
│
├── web/
│   ├── login.html
│   ├── terminal.html
│   └── app.js
│
├── configs/
│
├── .github/
│   └── workflows/
│
└── main.go
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

* Login works
* Logout works
* Session cookie works
* PTY creation works
* ConPTY works on Windows
* Terminal resize works
* Clipboard paste works
* WebSocket reconnect works
* Session survives browser disconnect
* Output replay works
* Multiple PTY sessions work
* Claude Code runs correctly
* Safari works
* Cloudflare Tunnel works
* Single executable deployment
* No external runtime dependencies
