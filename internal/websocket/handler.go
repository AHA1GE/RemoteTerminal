// Package websocket provides WebSocket upgrade handling, PTY output streaming,
// and input multiplexing for Remote Terminal.
package websocket

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AHA1GE/RemoteTerminal/internal/pty"
	"github.com/gorilla/websocket"
)

// Timing constants.
const (
	writeWait     = 10 * time.Second // time allowed to write a message to the client
	pingPeriod    = 30 * time.Second // server → client ping interval
	pongWait      = 10 * time.Second // time to wait for pong after a ping
	outputBufSize = 256              // subscriber channel buffer capacity
)

// PtyConnection tracks a single WebSocket connection to a PTY session.
type PtyConnection struct {
	ID        string // unique connection ID (8 bytes hex)
	SessionID string // PTY session this connects to
}

// Handler manages WebSocket upgrades and active connections for PTY sessions.
type Handler struct {
	sessionStore *pty.PtySessionStore
	log          Logger
	upgrader     websocket.Upgrader
	connections  map[string]*websocket.Conn // connID → raw conn (for shutdown)
	connMu       sync.Mutex
}

// Logger is the logging interface required by Handler.
type Logger interface {
	Debug(msg string, fields map[string]interface{})
	Error(msg string, fields map[string]interface{})
}

// NewHandler returns a WebSocket handler backed by the given session store.
func NewHandler(sessionStore *pty.PtySessionStore, log Logger) *Handler {
	return &Handler{
		sessionStore: sessionStore,
		log:          log,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Allow all origins — Cloudflare Tunnel may rewrite the Origin header.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		connections: make(map[string]*websocket.Conn),
	}
}

// ServeHTTP handles a WebSocket upgrade request. The session_token cookie must
// already be validated by middleware wrapping this handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/ws/")
	if sessionID == "" || strings.Contains(sessionID, "/") {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	session, ok := h.sessionStore.Get(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Error("websocket upgrade failed", map[string]interface{}{
			"session_id": sessionID,
			"error":      err.Error(),
		})
		return
	}

	// Generate a short unique connection ID.
	connID := newConnectionID()

	// Register for shutdown tracking.
	h.connMu.Lock()
	h.connections[connID] = conn
	h.connMu.Unlock()

	pc := &PtyConnection{
		ID:        connID,
		SessionID: sessionID,
	}

	h.log.Debug("websocket connected", map[string]interface{}{
		"session_id": sessionID,
		"conn_id":    connID,
	})

	h.handleConnection(conn, session, pc)

	// Unregister.
	h.connMu.Lock()
	delete(h.connections, connID)
	h.connMu.Unlock()

	h.log.Debug("websocket disconnected", map[string]interface{}{
		"session_id": sessionID,
		"conn_id":    connID,
	})
}

// CloseAll closes every active WebSocket connection. Used during shutdown.
func (h *Handler) CloseAll() {
	h.connMu.Lock()
	defer h.connMu.Unlock()
	for _, conn := range h.connections {
		conn.Close()
	}
	h.connections = make(map[string]*websocket.Conn)
}

// ---------------------------------------------------------------------------
// Connection lifecycle
// ---------------------------------------------------------------------------

func (h *Handler) handleConnection(conn *websocket.Conn, session *pty.PtySession, pc *PtyConnection) {
	// Channel for PTY output. Buffer sized to smooth bursts without blocking
	// the read loop; slow consumers are dropped upstream.
	outputCh := make(chan []byte, outputBufSize)

	// Subscribe to PTY output.
	session.Subscribe(pc.ID, outputCh)

	// Replay the ring buffer from the latest safe replay point.
	if sp, ok := session.RingBuffer.LatestSafeReplayPoint(); ok {
		data := session.RingBuffer.ReadFrom(sp.Offset)
		if len(data) > 0 {
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			conn.WriteMessage(websocket.BinaryMessage, data)
		}
	} else {
		// No safe point: replay everything available.
		oldest := session.RingBuffer.Snapshot() - int64(session.RingBuffer.Used())
		data := session.RingBuffer.ReadFrom(oldest)
		if len(data) > 0 {
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			conn.WriteMessage(websocket.BinaryMessage, data)
		}
	}

	// Coordination.
	done := make(chan struct{})

	// Pong handler keeps the read deadline alive.
	conn.SetReadDeadline(time.Now().Add(pingPeriod + pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pingPeriod + pongWait))
		return nil
	})

	// --- outputWriter: PTY output → WebSocket ---
	go h.outputWriter(conn, outputCh, done)

	// --- pinger: periodic pings ---
	go h.pinger(conn, done)

	// --- inputReader: WebSocket → PTY (blocking, runs in this goroutine) ---
	h.inputReader(conn, session, pc, done)

	// Connection is done. Signal goroutines and clean up.
	close(done)
	session.Unsubscribe(pc.ID)
}

// ---------------------------------------------------------------------------
// outputWriter
// ---------------------------------------------------------------------------

func (h *Handler) outputWriter(conn *websocket.Conn, outputCh <-chan []byte, done <-chan struct{}) {
	for {
		select {
		case data, ok := <-outputCh:
			if !ok {
				// PTY closed (process exited or explicit close).
				conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "process exited"),
					time.Now().Add(writeWait))
				return
			}
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// pinger
// ---------------------------------------------------------------------------

func (h *Handler) pinger(conn *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			// Send a final ping before exiting so we don't leave
			// a send on the write deadline unpaired.
			return
		}
	}
}

// ---------------------------------------------------------------------------
// inputReader (blocking; runs on the connection goroutine)
// ---------------------------------------------------------------------------

func (h *Handler) inputReader(conn *websocket.Conn, session *pty.PtySession, pc *PtyConnection, done <-chan struct{}) {
	for {
		msgType, message, err := conn.ReadMessage()
		if err != nil {
			// Client disconnected, timeout, or read error.
			return
		}

		// Only binary messages are expected (raw terminal input or resize control).
		if msgType != websocket.BinaryMessage {
			continue
		}

		if len(message) == 0 {
			continue
		}

		// Resize control message: 0x01 prefix + JSON {cols, rows}.
		if message[0] == 0x01 {
			h.handleResize(session, message[1:])
			continue
		}

		// Keyboard input / paste — apply input multiplexing.
		h.processInput(session, pc, message)
	}
}

// ---------------------------------------------------------------------------
// Resize
// ---------------------------------------------------------------------------

type resizeMsg struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func (h *Handler) handleResize(session *pty.PtySession, payload []byte) {
	var dims resizeMsg
	if err := json.Unmarshal(payload, &dims); err != nil {
		return
	}
	if dims.Cols < 1 || dims.Rows < 1 || dims.Cols > 1024 || dims.Rows > 1024 {
		return
	}
	session.Resize(dims.Cols, dims.Rows)
}

// ---------------------------------------------------------------------------
// Input multiplexing (implementplan.md lines 387–414)
// ---------------------------------------------------------------------------

// newConnectionID generates a short unique connection identifier (8 random bytes
// encoded as hex).
func newConnectionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (h *Handler) processInput(session *pty.PtySession, pc *PtyConnection, data []byte) {
	if session.ClaimActiveInput(pc.ID) {
		session.Write(data)
		return
	}

	h.log.Debug("dropped input from read-only connection", map[string]interface{}{
		"session_id": session.ID,
		"conn_id":    pc.ID,
	})
}
