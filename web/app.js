// Remote Terminal — client application
// Handles: login page, session list page, terminal page
(function () {
  'use strict';

  // --- helpers ---

  function getCookie(name) {
    var match = document.cookie.match(new RegExp('(^| )' + name + '=([^;]+)'));
    return match ? match[2] : null;
  }

  function getCSRFToken() {
    return getCookie('csrf_token');
  }

  // --- page detection ---

  var path = window.location.pathname;
  var isLoginPage = path === '/login';
  var isTerminalPage = path.startsWith('/terminal/');
  var isIndexPage = !isLoginPage && !isTerminalPage;

  // =========================================================================
  // Login page
  // =========================================================================
  if (isLoginPage) {
    var form = document.getElementById('login-form');
    var csrfInput = document.getElementById('csrf-token');
    var errorEl = document.getElementById('error');

    // Populate CSRF token from cookie
    var csrf = getCSRFToken();
    if (csrf && csrfInput) {
      csrfInput.value = csrf;
    }

    if (form) {
      form.addEventListener('submit', function (e) {
        e.preventDefault();
        var password = document.getElementById('password').value;
        var token = getCSRFToken();

        fetch('/login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: 'password=' + encodeURIComponent(password) + '&csrf_token=' + encodeURIComponent(token || ''),
          redirect: 'manual'
        }).then(function (res) {
          if (res.status === 200) {
            window.location.href = '/';
          } else if (res.status === 401) {
            if (errorEl) { errorEl.textContent = 'Invalid passcode.'; errorEl.style.display = 'block'; }
          } else if (res.status === 403) {
            if (errorEl) { errorEl.textContent = 'Access denied.'; errorEl.style.display = 'block'; }
          } else {
            if (errorEl) { errorEl.textContent = 'Error: ' + res.status; errorEl.style.display = 'block'; }
          }
        }).catch(function () {
          if (errorEl) { errorEl.textContent = 'Network error.'; errorEl.style.display = 'block'; }
        });
      });
    }
  }

  // =========================================================================
  // Session list page (index)
  // =========================================================================
  if (isIndexPage) {
    var sessionsBody = document.getElementById('sessions-body');
    var noSessions = document.getElementById('no-sessions');
    var newBtn = document.getElementById('new-session');
    var logoutBtn = document.getElementById('logout');

    function loadSessions() {
      fetch('/api/sessions')
        .then(function (res) {
          if (res.status === 401) { window.location.href = '/login'; return; }
          return res.json();
        })
        .then(function (sessions) {
          if (!sessionsBody) return;
          sessionsBody.innerHTML = '';
          if (!sessions || sessions.length === 0) {
            if (noSessions) noSessions.style.display = 'block';
            return;
          }
          if (noSessions) noSessions.style.display = 'none';
          sessions.forEach(function (s) {
            var tr = document.createElement('tr');
            tr.innerHTML =
              '<td><a href="/terminal/' + s.id + '">' + s.id + '</a></td>' +
              '<td>' + (s.created_at || '') + '</td>' +
              '<td>' + (s.last_seen_at || '') + '</td>' +
              '<td>' + (s.running ? 'running' : 'exited') + '</td>' +
              '<td><button class="delete" data-id="' + s.id + '">Delete</button></td>';
            sessionsBody.appendChild(tr);
          });

          // Attach delete handlers
          sessionsBody.querySelectorAll('button.delete').forEach(function (btn) {
            btn.addEventListener('click', function () {
              var id = btn.getAttribute('data-id');
              var token = getCSRFToken();
              fetch('/api/sessions/' + id, {
                method: 'DELETE',
                headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
                body: 'csrf_token=' + encodeURIComponent(token || '')
              }).then(function () { loadSessions(); });
            });
          });
        });
    }

    if (newBtn) {
      newBtn.addEventListener('click', function () {
        var token = getCSRFToken();
        fetch('/api/sessions', {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: 'csrf_token=' + encodeURIComponent(token || '')
        }).then(function (res) {
          if (res.status === 401) { window.location.href = '/login'; return; }
          return res.json();
        }).then(function (data) {
          if (data && data.id) { window.location.href = '/terminal/' + data.id; }
          loadSessions();
        });
      });
    }

    if (logoutBtn) {
      logoutBtn.addEventListener('click', function () {
        var token = getCSRFToken();
        fetch('/logout', {
          method: 'POST',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: 'csrf_token=' + encodeURIComponent(token || '')
        }).then(function () { window.location.href = '/login'; });
      });
    }

    loadSessions();
  }

  // =========================================================================
  // Terminal page
  // =========================================================================
  if (isTerminalPage) {
    var sessionId = path.split('/').pop();
    var termEl = document.getElementById('terminal');

    if (termEl && typeof Terminal !== 'undefined') {
      var term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: 'Consolas, "Courier New", monospace',
        theme: {
          background: '#ffffff',
          foreground: '#1a1a2e',
          cursor: '#0066cc',
          cursorAccent: '#ffffff',
          selectionBackground: '#0066cc40',
          black: '#2e2e2e',
          red: '#c41a16',
          green: '#007400',
          yellow: '#9c6500',
          blue: '#0451a5',
          magenta: '#a626a4',
          cyan: '#0184bc',
          white: '#d0d0d0',
          brightBlack: '#666666',
          brightRed: '#e0251d',
          brightGreen: '#009100',
          brightYellow: '#c48400',
          brightBlue: '#0066cc',
          brightMagenta: '#bc05bc',
          brightCyan: '#0097c4',
          brightWhite: '#ffffff'
        }
      });

      var fitAddon = new FitAddon.FitAddon();
      var webLinksAddon = new WebLinksAddon.WebLinksAddon();

      term.loadAddon(fitAddon);
      term.loadAddon(webLinksAddon);
      term.open(termEl);
      fitAddon.fit();

      // ---- WebSocket ----
      var ws = null;
      var reconnectTimer = null;
      var protocol = window.location.protocol === 'https:' ? 'wss://' : 'ws://';
      var wsUrl = protocol + window.location.host + '/ws/' + sessionId;

      function connect() {
        if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
          return;
        }

        ws = new WebSocket(wsUrl);
        ws.binaryType = 'arraybuffer';

        ws.onopen = function () {
          if (reconnectTimer) {
            clearTimeout(reconnectTimer);
            reconnectTimer = null;
          }
          sendResize();
        };

        ws.onmessage = function (e) {
          if (e.data instanceof ArrayBuffer) {
            term.write(new Uint8Array(e.data));
          } else if (typeof e.data === 'string') {
            term.write(e.data);
          }
        };

        ws.onclose = function (e) {
          ws = null;
          if (e.code === 1000) {
            term.writeln('\r\n\x1b[34mSession ended.\x1b[0m');
          } else {
            term.writeln('\r\n\x1b[31mDisconnected. Reconnecting in 3s…\x1b[0m');
            if (reconnectTimer) clearTimeout(reconnectTimer);
            reconnectTimer = setTimeout(connect, 3000);
          }
        };

        ws.onerror = function () {
          // onclose fires next.
        };
      }

      function sendResize() {
        if (!ws || ws.readyState !== WebSocket.OPEN) return;
        var dims = fitAddon.proposeDimensions();
        if (dims && dims.cols > 0 && dims.rows > 0) {
          ws.send('\x01' + JSON.stringify({ cols: dims.cols, rows: dims.rows }));
        }
      }

      // ---- toolbar modifier buttons ----
      var armed = { ctrl: false, alt: false };
      var btnEsc = document.getElementById('btn-esc');
      var btnCtrl = document.getElementById('btn-ctrl');
      var btnAlt = document.getElementById('btn-alt');

      function updateButtons() {
        if (btnCtrl) btnCtrl.className = armed.ctrl ? 'armed' : '';
        if (btnAlt) btnAlt.className = armed.alt ? 'armed' : '';
      }

      function disarmAll() {
        armed.ctrl = false;
        armed.alt = false;
        updateButtons();
      }

      if (btnEsc) {
        btnEsc.addEventListener('click', function () {
          if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send('\x1b');
          }
          disarmAll();
        });
      }

      if (btnCtrl) {
        btnCtrl.addEventListener('click', function () {
          armed.ctrl = !armed.ctrl;
          updateButtons();
        });
      }

      if (btnAlt) {
        btnAlt.addEventListener('click', function () {
          armed.alt = !armed.alt;
          updateButtons();
        });
      }

      term.onData(function (data) {
        if (!ws || ws.readyState !== WebSocket.OPEN) return;

        if (armed.ctrl || armed.alt) {
          // Build the combo: Alt sends ESC prefix, Ctrl maps the key to
          // its control character (e.g. Ctrl+C -> \x03).
          var result = '';
          if (armed.alt) result += '\x1b';
          if (armed.ctrl) {
            result += String.fromCharCode(data.charCodeAt(0) & 0x1f);
          } else {
            result += data;
          }
          ws.send(result);
          disarmAll();
        } else {
          ws.send(data);
        }
      });

      term.onResize(function (dims) {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send('\x01' + JSON.stringify({ cols: dims.cols, rows: dims.rows }));
        }
      });

      window.addEventListener('resize', function () {
        fitAddon.fit();
      });

      connect();
    }
  }
})();
