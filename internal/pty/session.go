package pty

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	goPty "github.com/aymanbagabas/go-pty"
)

// PtySession is a managed PTY with an output ring buffer and subscriber-based
// output broadcast for WebSocket connections. PTY lifetime is independent of
// browser lifetime — sessions survive disconnects.
type PtySession struct {
	ID        string
	CreatedAt time.Time
	LastSeenAt time.Time

	pty    *goPty.Pty
	Width  int
	Height int

	ActiveConnections  int
	ActiveConnectionID string

	RingBuffer *CircularBuffer

	mu          sync.Mutex
	closed      bool
	subsClosed  bool                        // set true after subscriber channels are cleaned up
	subscribers map[string]chan<- []byte     // connID → output channel
	subMu       sync.RWMutex
}

// PtySessionStore holds all active PTY sessions in memory.
type PtySessionStore struct {
	mu          sync.Mutex
	sessions    map[string]*PtySession
	maxSessions int
}

// NewPtySessionStore returns an empty session store with the given capacity.
func NewPtySessionStore(max int) *PtySessionStore {
	if max <= 0 {
		max = 32
	}
	return &PtySessionStore{
		sessions:    make(map[string]*PtySession),
		maxSessions: max,
	}
}

// Create starts a new PTY running cmd, allocates a ring buffer, and launches a
// read-loop goroutine that broadcasts output to subscribers. width and height
// set the initial terminal size in cols/rows.
func (s *PtySessionStore) Create(cmd []string, width, height, bufferSize int) (*PtySession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.sessions) >= s.maxSessions {
		return nil, fmt.Errorf("max sessions (%d) reached", s.maxSessions)
	}

	id, err := generateSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}

	// go-pty requires at least one arg (the command name)
	name, args := cmd[0], cmd[1:]

	pt, err := goPty.NewWithOptions(
		goPty.WithCommand(name, args...),
		goPty.WithSize(width, height),
	)
	if err != nil {
		return nil, fmt.Errorf("create PTY: %w", err)
	}

	if bufferSize <= 0 {
		bufferSize = 1048576 // 1 MB default
	}

	sess := &PtySession{
		ID:          id,
		CreatedAt:   time.Now(),
		LastSeenAt:  time.Now(),
		pty:         pt,
		Width:       width,
		Height:      height,
		RingBuffer:  NewCircularBuffer(bufferSize),
		subscribers: make(map[string]chan<- []byte),
	}

	s.sessions[id] = sess

	// Start the read loop in a background goroutine.
	go sess.readLoop()

	return sess, nil
}

// Get returns the session with the given ID, or nil.
func (s *PtySessionStore) Get(id string) (*PtySession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// Delete closes the PTY, removes the session from the store, and cleans up
// subscriber channels. Returns an error if the session does not exist.
func (s *PtySessionStore) Delete(id string) error {
	s.mu.Lock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session not found: %s", id)
	}
	delete(s.sessions, id)
	s.mu.Unlock()

	return sess.Close()
}

// List returns a snapshot of all active sessions.
func (s *PtySessionStore) List() []*PtySession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PtySession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}

// CloseAll closes every PTY and clears the store. Used during graceful shutdown.
func (s *PtySessionStore) CloseAll() {
	s.mu.Lock()
	sessions := make([]*PtySession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.sessions = make(map[string]*PtySession)
	s.mu.Unlock()

	for _, sess := range sessions {
		sess.Close()
	}
}

// Count returns the number of active sessions (test helper).
func (s *PtySessionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// ---------------------------------------------------------------------------
// PtySession methods
// ---------------------------------------------------------------------------

// Close shuts down the PTY and all subscriber channels. It is idempotent.
func (ps *PtySession) Close() error {
	ps.mu.Lock()
	if ps.closed {
		ps.mu.Unlock()
		return nil
	}
	ps.closed = true
	ps.mu.Unlock()

	// Close the PTY first — this will cause the read loop to exit.
	err := ps.pty.Close()

	// Clean up subscriber channels. Guarded by subsClosed so we don't
	// double-close if the read loop already cleaned up.
	ps.cleanupSubscribers()

	return err
}

// cleanupSubscribers closes all subscriber channels and removes them from the
// map. Safe to call multiple times — only the first call does the work.
func (ps *PtySession) cleanupSubscribers() {
	ps.subMu.Lock()
	if ps.subsClosed {
		ps.subMu.Unlock()
		return
	}
	ps.subsClosed = true
	for id, ch := range ps.subscribers {
		close(ch)
		delete(ps.subscribers, id)
	}
	ps.subMu.Unlock()
}

// Resize changes the terminal dimensions.
func (ps *PtySession) Resize(width, height int) error {
	ps.Width = width
	ps.Height = height
	return ps.pty.Resize(width, height)
}

// Write sends data to the PTY and updates LastSeenAt. It must only be called
// by the active connection (input multiplexing is enforced by the caller).
func (ps *PtySession) Write(data []byte) (int, error) {
	ps.LastSeenAt = time.Now()
	return ps.pty.Write(data)
}

// ClaimActiveInput attempts to make connID the active input connection for this
// session. Returns true if input should be forwarded; false if it should be
// silently dropped (read-only connection).
//
// The first connection to send input becomes active. The active connection is
// deactivated after 10 seconds of inactivity, allowing another connection to
// take over.
func (ps *PtySession) ClaimActiveInput(connID string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// Already the active connection.
	if ps.ActiveConnectionID == connID {
		ps.LastSeenAt = time.Now()
		return true
	}

	// No active connection — claim it.
	if ps.ActiveConnectionID == "" {
		ps.ActiveConnectionID = connID
		ps.LastSeenAt = time.Now()
		return true
	}

	// Active connection is idle — steal it.
	if time.Since(ps.LastSeenAt) > 10*time.Second {
		ps.ActiveConnectionID = connID
		ps.LastSeenAt = time.Now()
		return true
	}

	// Active connection is still sending input — read-only.
	return false
}

// Subscribe adds a channel that will receive PTY output. The channel must have
// adequate buffer capacity to avoid blocking the read loop.
func (ps *PtySession) Subscribe(connID string, ch chan<- []byte) {
	ps.subMu.Lock()
	ps.subscribers[connID] = ch
	ps.subMu.Unlock()

	ps.mu.Lock()
	ps.ActiveConnections++
	ps.mu.Unlock()
}

// Unsubscribe removes and closes a subscriber channel. It also decrements the
// active connection count and clears the active connection ID if this
// connection was the active one.
func (ps *PtySession) Unsubscribe(connID string) {
	ps.subMu.Lock()
	ch, ok := ps.subscribers[connID]
	if ok {
		delete(ps.subscribers, connID)
		close(ch)
	}
	ps.subMu.Unlock()

	ps.mu.Lock()
	if ps.ActiveConnections > 0 {
		ps.ActiveConnections--
	}
	if ps.ActiveConnectionID == connID {
		ps.ActiveConnectionID = ""
	}
	ps.mu.Unlock()
}

// Closed returns true if the PTY has been closed.
func (ps *PtySession) Closed() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.closed
}

// readLoop reads output from the PTY in a dedicated goroutine. Data is written
// to the ring buffer and broadcast to all subscribers. Slow subscribers are
// silently dropped (they can catch up via buffer replay on reconnect). When
// the PTY returns an error (process exit), all subscriber channels are closed.
func (ps *PtySession) readLoop() {
	buf := make([]byte, 32*1024) // 32 KB read buffer

	for {
		n, err := ps.pty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// Write to ring buffer (thread-safe, non-blocking).
			ps.RingBuffer.Write(data)
			ps.LastSeenAt = time.Now()

			// Broadcast to subscribers. Non-blocking: if a subscriber's
			// channel is full we drop the data rather than blocking the
			// PTY read loop. Dropped subscribers can catch up via buffer
			// replay on reconnect.
			ps.subMu.RLock()
			for _, ch := range ps.subscribers {
				select {
				case ch <- data:
				default:
					// subscriber too slow — drop
				}
			}
			ps.subMu.RUnlock()
		}

		if err != nil {
			// Process exited or PTY closed.
			ps.mu.Lock()
			ps.closed = true
			ps.mu.Unlock()

			// Signal subscribers that the session has ended.
			// Guarded against double-close with Close().
			ps.cleanupSubscribers()
			return
		}
	}
}

// generateSessionID returns a 256-bit crypto/rand session ID encoded as
// base64 URL-safe text.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
