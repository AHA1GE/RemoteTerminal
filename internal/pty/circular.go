// Package pty provides PTY session management and a ring buffer for terminal
// output with safe replay points for reconnection without garbled rendering.
package pty

import (
	"bytes"
	"sync"
	"time"
)

// SafeReplayPoint marks a byte offset in the output stream where terminal state
// is known-clean. On reconnect, replay begins from the most recent safe point
// to avoid feeding xterm.js a truncated ANSI escape sequence.
type SafeReplayPoint struct {
	Offset int64     // logical byte offset in the stream
	Time   time.Time // when the safe point was recorded
}

// CircularBuffer is a fixed-size ring buffer for PTY output. It tracks logical
// byte offsets that never wrap, so safe replay points remain valid even after
// the physical buffer wraps. It implements io.Writer.
type CircularBuffer struct {
	mu           sync.Mutex
	buf          []byte            // underlying circular storage
	size         int               // total capacity in bytes
	writePos     int               // next physical write position in buf (0..size-1)
	count        int64             // total bytes written (monotonic, never wraps)
	used         int               // bytes currently stored (0..size)
	replayPoints []SafeReplayPoint // most recent safe replay points, newest last (max 10 kept)
}

// NewCircularBuffer returns a CircularBuffer with the given capacity in bytes.
func NewCircularBuffer(size int) *CircularBuffer {
	if size < 4096 {
		size = 4096
	}
	return &CircularBuffer{
		buf:          make([]byte, size),
		size:         size,
		replayPoints: make([]SafeReplayPoint, 0, 10),
	}
}

// Write implements io.Writer. It writes p to the ring buffer, updating logical
// offsets and tracking safe replay points. It is safe for concurrent use.
func (cb *CircularBuffer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.trackSafeReplayPoints(p)
	cb.writeData(p)
	return len(p), nil
}

// ReadFrom returns all available bytes from logicalOffset to the present. If
// logicalOffset is older than the oldest retained byte, it is clamped to the
// oldest available byte to avoid gaps.
func (cb *CircularBuffer) ReadFrom(logicalOffset int64) []byte {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.readFromLocked(logicalOffset)
}

// LatestSafeReplayPoint returns the most recent safe replay point and true, or
// a zero value and false if no safe point has been recorded.
func (cb *CircularBuffer) LatestSafeReplayPoint() (SafeReplayPoint, bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if len(cb.replayPoints) == 0 {
		return SafeReplayPoint{}, false
	}
	return cb.replayPoints[len(cb.replayPoints)-1], true
}

// Snapshot returns the current logical write count. Callers can use this to
// split replay (buffer up to snapshot) from live streaming (bytes after
// snapshot).
func (cb *CircularBuffer) Snapshot() int64 {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.count
}

// Size returns the buffer capacity in bytes.
func (cb *CircularBuffer) Size() int {
	return cb.size
}

// Used returns the number of bytes currently stored in the buffer.
func (cb *CircularBuffer) Used() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.used
}

// writeData copies p into the physical ring buffer, updating the write position
// and logical count. Caller must hold cb.mu.
func (cb *CircularBuffer) writeData(p []byte) {
	if len(p) >= cb.size {
		// Input larger than buffer: keep only the tail.
		p = p[len(p)-cb.size:]
	}

	for _, b := range p {
		cb.buf[cb.writePos] = b
		cb.writePos = (cb.writePos + 1) % cb.size
		cb.count++
		if cb.used < cb.size {
			cb.used++
		}
	}
}

// trackSafeReplayPoints scans p for ANSI escape sequences that indicate a known-clean
// terminal state. When found, records a SafeReplayPoint at the byte offset where the
// sequence begins — so replay from that offset includes the full sequence.
//
// Tracked sequences (implementplan.md lines 513-518):
//
//	\x1b[H          Cursor home (full-screen repaint imminent)
//	\x1b[2J         Clear entire screen
//	\x1b[3J         Clear screen + scrollback
//	\x1b[H\x1b[2J   Clear screen and home (combined)
//	\x1b]0;         OSC title change (no rendering impact, safe boundary)
//
// Caller must hold cb.mu.
func (cb *CircularBuffer) trackSafeReplayPoints(p []byte) {
	// Build a buffer of the data being written so we can do multi-byte matching
	// across the physical buffer boundary. We use the logical offset (count) at
	// the start of this write as the base for computing replay point offsets.

	baseOffset := cb.count

	for i := 0; i < len(p); i++ {
		if p[i] != 0x1b { // ESC
			continue
		}

		var seqLen int

		// Check for CSI sequences: ESC [
		if i+2 < len(p) && p[i+1] == 0x5b { // '['
			switch {
			case p[i+2] == 0x48: // ESC [ H — cursor home
				seqLen = 3
			case i+3 < len(p) && p[i+2] == 0x32 && p[i+3] == 0x4a: // ESC [ 2 J — clear screen
				seqLen = 4
			case i+3 < len(p) && p[i+2] == 0x33 && p[i+3] == 0x4a: // ESC [ 3 J — clear screen + scrollback
				seqLen = 4
			case i+5 < len(p) && p[i+2] == 0x48 && p[i+3] == 0x1b && p[i+4] == 0x5b && p[i+5] == 0x32 && i+6 < len(p) && p[i+6] == 0x4a:
				// ESC [ H ESC [ 2 J — combined clear and home
				seqLen = 7
			}
		}

		// Check for OSC sequence: ESC ] 0 ;
		if seqLen == 0 && i+3 < len(p) && p[i+1] == 0x5d && p[i+2] == 0x30 && p[i+3] == 0x3b {
			seqLen = 4
		}

		if seqLen > 0 {
			cb.addReplayPoint(baseOffset + int64(i))
			i += seqLen - 1 // skip past the sequence
		}
	}
}

// addReplayPoint records a safe replay point, keeping at most 10 (the most
// recent). Caller must hold cb.mu.
func (cb *CircularBuffer) addReplayPoint(offset int64) {
	// Avoid duplicate back-to-back points at the same offset (can happen with
	// combined sequences like \x1b[H\x1b[2J where both parts match).
	if len(cb.replayPoints) > 0 && cb.replayPoints[len(cb.replayPoints)-1].Offset == offset {
		return
	}

	cb.replayPoints = append(cb.replayPoints, SafeReplayPoint{
		Offset: offset,
		Time:   time.Now(),
	})

	// Keep only the last 10
	if len(cb.replayPoints) > 10 {
		// Shift left, reusing the slice
		n := len(cb.replayPoints) - 10
		cb.replayPoints = append(cb.replayPoints[:0], cb.replayPoints[n:]...)
	}
}

// Reset clears the buffer and all safe replay points.
func (cb *CircularBuffer) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.writePos = 0
	cb.count = 0
	cb.used = 0
	cb.replayPoints = cb.replayPoints[:0]
	// Zero the underlying buffer so old data isn't readable
	for i := range cb.buf {
		cb.buf[i] = 0
	}
}

// Ensure io.Writer is satisfied.
var _ interface{ Write([]byte) (int, error) } = (*CircularBuffer)(nil)

// escapeSequences holds the bytes of each recognized safe-replay ANSI sequence.
// Used only for documentation and testing.
var escapeSequences = map[string][]byte{
	"cursorHome":     {0x1b, 0x5b, 0x48},                                     // \x1b[H
	"clearScreen":    {0x1b, 0x5b, 0x32, 0x4a},                               // \x1b[2J
	"clearScrollback": {0x1b, 0x5b, 0x33, 0x4a},                              // \x1b[3J
	"clearAndHome":   {0x1b, 0x5b, 0x48, 0x1b, 0x5b, 0x32, 0x4a},            // \x1b[H\x1b[2J
	"oscTitle":       {0x1b, 0x5d, 0x30, 0x3b},                               // \x1b]0;
}

// readFromLocked is the lock-free implementation of ReadFrom. Caller must hold
// cb.mu. It is used by ReadFrom (which provides locking) and by test helpers
// (which manage their own locking).
func (cb *CircularBuffer) readFromLocked(logicalOffset int64) []byte {
	oldest := cb.count - int64(cb.used)
	if cb.used == 0 {
		return nil
	}

	start := logicalOffset
	if start < oldest {
		start = oldest
	}
	if start >= cb.count {
		return nil
	}

	physStart := int(start % int64(cb.size))
	length := int(cb.count - start)
	if length > cb.size {
		length = cb.size
	}

	out := make([]byte, length)

	if physStart+length <= cb.size {
		copy(out, cb.buf[physStart:physStart+length])
	} else {
		first := cb.size - physStart
		copy(out[:first], cb.buf[physStart:])
		copy(out[first:], cb.buf[:length-first])
	}

	return out
}

// readAll returns the full buffer contents as a string (test helper).
func (cb *CircularBuffer) readAll() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.used == 0 {
		return ""
	}
	oldest := cb.count - int64(cb.used)
	return string(cb.readFromLocked(oldest))
}

// readAllBytes returns the full buffer contents (test helper).
func (cb *CircularBuffer) readAllBytes() []byte {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.used == 0 {
		return nil
	}
	oldest := cb.count - int64(cb.used)
	return cb.readFromLocked(oldest)
}

// contains checks whether sub is present in the buffer's current contents (test helper).
func (cb *CircularBuffer) contains(sub []byte) bool {
	return bytes.Contains(cb.readAllBytes(), sub)
}
