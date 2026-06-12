# Manual Test Matrix

Test on compiled binary (Windows: `RT.exe`, Linux: `RT`) in a clean directory with `config.yaml`, `cert.pem`, `key.pem`.

Key: ✅ = pass, ❌ = fail, ⚠️ = skip/noted, `—` = not applicable

---

## 1. Startup & Config

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 1.1 | No config file | Run binary with no config.yaml | Generates default config.yaml, prints instructions, exits 0 | ✅ | |
| 1.2 | Default config values | Inspect generated config.yaml | All 8 fields present with documented defaults, `default_work_dir: ""` | ✅ | |
| 1.3 | password_text → hash | Set `password_text: test123`, restart | `password_text` removed, `password_hash: $argon2id$v=19$...` written | ✅ | |
| 1.4 | Missing password | Start with no password_text and `password_hash: <argon2id>` | Prints "password not set" message, exits | | |
| 1.5 | default_command validation | Set `default_command: [nonexistent.exe]` | Fatal error: "default_command not found", exits 1 | ✅ | |
| 1.6 | default_work_dir — valid dir | Set `default_work_dir: /tmp` (Linux) or `C:\Users\Public` (Win) | Starts normally, no log errors about work_dir | | |
| 1.7 | default_work_dir — invalid dir | Set `default_work_dir: /does/not/exist` | Logged error, field reset to `""`, config saved, server continues | | |
| 1.8 | default_work_dir — file not dir | Set `default_work_dir: /bin/sh` (Linux) or `C:\Windows\System32\cmd.exe` (Win) | Logged error, field reset to `""`, config saved, server continues | | |
| 1.9 | default_work_dir — empty | Set `default_work_dir: ""` (or omit field) | No validation done, server starts normally | | |
| 1.10 | TLS cert/key missing | Run without cert.pem or key.pem | Fatal error, exits 1 | ✅ | |
| 1.11 | TLS cert/key invalid | Provide mismatched or expired cert/key | Fatal error, exits 1 | | |
| 1.12 | CLI args rejected | Run `./RT --help` or `./RT foo` | Prints "no arguments accepted", exits 1 | | |
| 1.13 | blacklist.txt missing | Run without blacklist.txt | Starts normally with empty blacklist | ✅ | |
| 1.14 | blacklist.txt present | Place blacklist.txt with IP entries | IPs loaded into blacklist on startup | | |
| 1.15 | Startup info logged | Start with valid config | Prints: version, config path, cert path, listen addr, log level | | |

---

## 2. Auth & Security

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 2.1 | GET /login | Browser to https://host:port/login | Renders login.html: password input, hidden csrf_token, viewport meta | ✅ | |
| 2.2 | GET / redirect | Browser to https://host:port/ | Redirects to /login (302) | | |
| 2.3 | GET /api/sessions unauthed | curl /api/sessions without session cookie | Returns 401 JSON | | |
| 2.4 | Login — correct password | POST /login with correct password + csrf_token | 200, sets session_token + csrf_token cookies | ✅ | |
| 2.5 | Login — wrong password | POST /login with wrong password | 401, no cookies set | ✅ | |
| 2.6 | Login — missing CSRF | POST /login without csrf_token | 403 "invalid csrf token" | | |
| 2.7 | Login — wrong CSRF | POST /login with mismatched csrf_token | 403 "invalid csrf token" | | |
| 2.8 | session_token cookie attributes | Inspect Set-Cookie header after login | HttpOnly, Secure, SameSite=Strict | | |
| 2.9 | csrf_token cookie attributes | Inspect Set-Cookie header | Secure, SameSite=Strict, NO HttpOnly (JS-readable) | | |
| 2.10 | Brute force — 5 attempts | POST wrong password 5× from same IP | 6th attempt returns 403 "IP blacklisted" | | |
| 2.11 | Brute force — loopback exempt | POST wrong password 5× from 127.0.0.1 | Never blacklisted, always returns 401 | ✅ | |
| 2.12 | Brute force — CF header | Send `CF-Connecting-IP: 10.0.0.1` header | Blacklisting uses header value, not RemoteAddr | | |
| 2.13 | blacklist.txt appended | Trigger blacklist | IP appended to blacklist.txt (one per line) | | |
| 2.14 | Logout | POST /logout with valid session + csrf_token | session_token cookie cleared (Max-Age=0), redirected to /login | ✅ | |
| 2.15 | Auth pages behind login | Access / or /terminal/{id} after logout | Redirected to /login | | |
| 2.16 | API behind login | Access /api/sessions after logout | Returns 401 JSON | | |

---

## 3. PTY Sessions (Core)

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 3.1 | Create session — PowerShell/WSL | Login, POST /api/sessions | Returns 201 with `{"id":"..."}`, session appears in list | ✅ | |
| 3.2 | Create session — custom command | Set `default_command: [bash]` (Linux) or `default_command: [cmd.exe]` | Session runs configured shell | | |
| 3.3 | Create session — default_work_dir empty | Omit or set `default_work_dir: ""` | Shell starts in binary directory (inherited) | | |
| 3.4 | Create session — default_work_dir set | Set `default_work_dir: /tmp` or `C:\Users\Public` | Shell starts in configured directory (verify with `pwd` / `cd`) | | |
| 3.5 | Create session — multiple | Click "New Session" 3× | 3 sessions appear in list, each with unique ID | | |
| 3.6 | Max sessions cap | Set `max_sessions: 1`, create 2 sessions | 2nd returns 409 "max sessions (1) reached" | | |
| 3.7 | Session list API | GET /api/sessions | Returns JSON array with id, command, created_at, active_connections, rows, cols | | |
| 3.8 | Delete session | DELETE /api/sessions/{id} + csrf_token | Session removed from list, PTY process killed | | |
| 3.9 | Delete — wrong CSRF | DELETE without csrf_token | 403 "invalid csrf token" | | |
| 3.10 | PTY output | Open terminal, type `echo hello` | Output appears in terminal | | |
| 3.11 | PTY resize | Resize browser window with terminal open | Resize event sent (0x01), session cols/rows updated | | |
| 3.12 | Process exit | Type `exit` in terminal | WebSocket receives Close(1000), terminal shows "Session ended" | | |
| 3.13 | Session survives disconnect | Open terminal, close browser tab, reopen | Session still exists in list, reconnect replays output | | |
| 3.14 | Buffer size | Type lots of output (>1 MB) | Oldest output truncated, new output visible | | |

---

## 4. WebSocket Protocol

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 4.1 | Connect WebSocket | Open terminal page | WebSocket connects to /ws/{id}, receives binary output stream | ✅ | |
| 4.2 | Buffer replay on connect | Type some output, disconnect, reconnect | Previous output replayed from safe point before live stream | | |
| 4.3 | Safe replay — clear screen | Type `clear`, reconnect | Replay starts from clear-screen sequence, not garbled | | |
| 4.4 | Keyboard input | Type characters in terminal | Sent as raw bytes, appear in PTY | ✅ | |
| 4.5 | Paste input | Paste multi-line text | Sent as raw bytes, processed correctly | | |
| 4.6 | Resize frame (0x01) | Resize terminal window | 0x01 + `{"cols":N,"rows":M}` sent to server | | |
| 4.7 | Binary output frames | PTY produces output | Server sends BinaryMessage frames | | |
| 4.8 | Close frame (1000) | Type `exit` in terminal | Server sends CloseMessage(1000, "process exited") | ✅ | |
| 4.9 | Input multiplexing — active | Open 2 tabs to same session, type in 1st | 1st tab's input reaches PTY, 2nd tab's input dropped (silent) | | |
| 4.10 | Input multiplexing — idle takeover | Wait 10s, type in 2nd tab | 2nd tab becomes active, 1st tab's input now dropped | | |
| 4.11 | Ping/pong | Keep WebSocket open 60+ seconds | Server pings every 30s, client auto-pongs, no disconnection | | |
| 4.12 | Read deadline | Block pong responses (e.g., network stall) | Server closes connection after 40s (30s ping + 10s pong wait) | | |
| 4.13 | Reconnect delay | Close WebSocket with non-1000 code | Client reconnects after 3s delay | | |
| 4.14 | Reconnect — no delay on 1000 | Close WebSocket with 1000 | Client does NOT reconnect (session ended) | | |

---

## 5. Frontend (Browser)

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 5.1 | Login page — mobile viewport | Open /login on mobile or small viewport | Meta viewport tag present, page scales correctly | | |
| 5.2 | Login page — CSRF hidden input | View page source of /login | Hidden `<input name="csrf_token">` present | | |
| 5.3 | Session list — table | Login, view / | Table with columns: Name, Created, Actions | | |
| 5.4 | Session list — empty state | Login with no sessions | Shows "No active sessions." message | | |
| 5.5 | Session list — New Session button | Click "New Session" | New session created, redirected to /terminal/{id} | | |
| 5.6 | Session list — Delete button | Click Delete on a session | Session removed from list | | |
| 5.7 | Session list — Logout button | Click Logout | Redirected to /login, session_token cleared | | |
| 5.8 | Terminal — xterm.js loads | Open /terminal/{id} | xterm.js 5.5.0 terminal renders | | |
| 5.9 | Terminal — fit addon | Resize browser window | Terminal fills available space | | |
| 5.10 | Terminal — web links addon | Type a URL in terminal, hover | URL is underlined and clickable | | |
| 5.11 | Terminal — app.js dispatch | Visit /login, /, /terminal/{id} | Each page runs correct init function from IIFE dispatch | | |
| 5.12 | Terminal — toolbar Esc | Click Esc button in toolbar | Sends \x1b to terminal | | |
| 5.13 | Terminal — toolbar Ctrl+C | Click Ctrl, then C | Sends \x03 to terminal | | |
| 5.14 | Terminal — toolbar Alt+. | Click Alt, then . | Sends \x1b. to terminal | | |
| 5.15 | CSRF in JS | View app.js source or console | `getCSRFToken()` reads from document.cookie | | |
| 5.16 | 401 redirect | Session expires, try to create/delete session | JS redirects to /login | | |

---

## 6. Graceful Shutdown

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 6.1 | SIGINT | Send SIGINT (Ctrl+C) to running server | Logs "shutting down", stops HTTP, closes PTYs, closes WebSockets, exits 0 | ✅ | |
| 6.2 | SIGTERM | Send SIGTERM to running server | Same as SIGINT | | |
| 6.3 | PTY children killed | Start session, send SIGINT to server | Shell process(es) terminated, no orphans | | |
| 6.4 | WebSockets closed | Open terminal, send SIGINT to server | WebSocket receives close frame before server exits | | |
| 6.5 | HTTP stops accepting | Send SIGINT, immediately try new request | Connection refused (HTTP listener closed within 5s) | | |

---

## 7. Platform-Specific

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 7.1 | ConPTY | Run on Windows, create session | Uses Windows Pseudo Console (ConPTY) | ✅ | — |
| 7.2 | POSIX openpt | Run on Linux, create session | Uses /dev/ptmx PTY | — | |
| 7.3 | Windows path in work_dir | Set `default_work_dir: C:\Program Files` | Validated with os.Stat, session starts there | | — |
| 7.4 | Linux path in work_dir | Set `default_work_dir: /home/user` | Validated with os.Stat, session starts there | — | |
| 7.5 | Windows UNC path in work_dir | Set `default_work_dir: \\server\share` | Validated with os.Stat | | — |
| 7.6 | Binary name | Build from source | `RT.exe` on Windows, `RT` on Linux | | |
| 7.7 | Cross-compile: windows-amd64 | `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build` | Produces working RT.exe | | |
| 7.8 | Cross-compile: windows-arm64 | `GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build` | Produces RT.exe (verify on ARM Windows if available) | | |
| 7.9 | Cross-compile: linux-amd64 | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build` | Produces working RT binary | | |
| 7.10 | Cross-compile: linux-arm64 | `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build` | Produces RT binary (verify on ARM Linux if available) | | |

---

## 8. Config Persistence

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 8.1 | Config saved on password hash | Set password_text, restart, inspect config.yaml | password_text removed, password_hash written | ✅ | |
| 8.2 | Config saved on work_dir reset | Set invalid default_work_dir, restart, inspect config.yaml | default_work_dir reset to `""`, other fields unchanged | | |
| 8.3 | Config saved on blacklist | Trigger blacklist, inspect blacklist.txt | IP appended, persists across restart | | |
| 8.4 | Config unchanged on normal run | Start with valid config, no password_text or invalid work_dir | config.yaml not modified | ✅ | |
| 8.5 | Config permissions preserved | Check file permissions after Save() | 0644 (owner rw, group r, other r) | | |

---

## 9. Logger

| # | Test | Steps | Expected | Win | Linux |
|---|---|---|---|---|---|
| 9.1 | Debug level | Set `log_level: debug`, create session, type, delete | All operations logged as JSON to stdout | ✅ | |
| 9.2 | Error level | Set `log_level: error`, create session | Only errors logged, debug messages suppressed | | |
| 9.3 | None level | Set `log_level: none`, create session | No log output | | |
| 9.4 | JSON format | Inspect log line | `{"time":"RFC3339","level":"...","msg":"...",...}` | | |
| 9.5 | Concurrent writes | Heavy load (multiple sessions, rapid I/O) | No interleaved/mangled JSON lines (mutex working) | | |

---

**Total: 80 tests** across 9 categories.
