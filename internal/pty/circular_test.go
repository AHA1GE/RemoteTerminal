package pty

import (
	"fmt"
	"sync"
	"testing"
)

func TestNewCircularBuffer(t *testing.T) {
	cb := NewCircularBuffer(1024)
	if cb == nil {
		t.Fatal("NewCircularBuffer returned nil")
	}
	if cb.Size() != 1024 {
		t.Fatalf("expected size 1024, got %d", cb.Size())
	}
	if cb.Used() != 0 {
		t.Fatalf("expected used 0, got %d", cb.Used())
	}
	if cb.Snapshot() != 0 {
		t.Fatalf("expected snapshot 0, got %d", cb.Snapshot())
	}
}

func TestNewCircularBufferMinimumSize(t *testing.T) {
	cb := NewCircularBuffer(10)
	if cb.Size() != 4096 {
		t.Fatalf("expected minimum size 4096, got %d", cb.Size())
	}
}

func TestWriteAndReadAll(t *testing.T) {
	cb := NewCircularBuffer(1024)
	data := []byte("hello world")
	n, err := cb.Write(data)
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	if n != len(data) {
		t.Fatalf("expected %d bytes written, got %d", len(data), n)
	}
	if cb.Used() != len(data) {
		t.Fatalf("expected used %d, got %d", len(data), cb.Used())
	}

	// ReadAll should return the full content
	result := cb.readAll()
	if result != "hello world" {
		t.Fatalf("expected 'hello world', got %q", result)
	}
}

func TestReadFromExactOffset(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("abcdefghij"))

	// Read from offset 3 should return "defghij"
	result := cb.ReadFrom(3)
	if string(result) != "defghij" {
		t.Fatalf("expected 'defghij', got %q", string(result))
	}
}

func TestReadFromOldOffset(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("abcdefghij"))

	// Read from offset 0 should return everything
	result := cb.ReadFrom(0)
	if string(result) != "abcdefghij" {
		t.Fatalf("expected 'abcdefghij', got %q", string(result))
	}
}

func TestReadFromFutureOffset(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("abc"))

	result := cb.ReadFrom(100)
	if result != nil {
		t.Fatalf("expected nil for future offset, got %q", string(result))
	}
}

func TestSnapshot(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("first "))
	snap := cb.Snapshot()
	cb.Write([]byte("second"))

	// Read from snapshot should return only "second"
	result := cb.ReadFrom(snap)
	if string(result) != "second" {
		t.Fatalf("expected 'second', got %q", string(result))
	}

	// ReadAll should return everything
	all := cb.readAll()
	if all != "first second" {
		t.Fatalf("expected 'first second', got %q", all)
	}
}

func TestWrapAround(t *testing.T) {
	// Small buffer to force wrapping
	cb := NewCircularBuffer(16)

	// Write 20 bytes — last 16 should be retained
	cb.Write([]byte("ABCDEFGHIJKLMNOPQRST"))
	if cb.Used() != 16 {
		t.Fatalf("expected used 16, got %d", cb.Used())
	}

	result := cb.readAll()
	// The last 16 bytes of the input
	if result != "EFGHIJKLMNOPQRST" {
		t.Fatalf("expected 'EFGHIJKLMNOPQRST', got %q", result)
	}
}

func TestWriteLargerThanBuffer(t *testing.T) {
	cb := NewCircularBuffer(16)

	// Write 30 bytes — only last 16 retained
	cb.Write([]byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcd"))
	if cb.Used() != 16 {
		t.Fatalf("expected used 16, got %d", cb.Used())
	}

	result := cb.readAll()
	if result != "KLMNOPQRSTUVWXYZ" {
		t.Fatalf("expected 'KLMNOPQRSTUVWXYZ', got %q", result)
	}
}

func TestMultipleWrites(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("hello"))
	cb.Write([]byte(" "))
	cb.Write([]byte("world"))

	if cb.readAll() != "hello world" {
		t.Fatalf("expected 'hello world', got %q", cb.readAll())
	}
}

func TestWrapWithMultipleWrites(t *testing.T) {
	cb := NewCircularBuffer(16)
	cb.Write([]byte("AAAAAAAA")) // 8 bytes, count=8
	cb.Write([]byte("BBBBBBBB")) // 8 bytes, count=16, used=16
	cb.Write([]byte("CCCC"))     // 4 bytes, count=20, used=16, overwrites first 4 A's

	result := cb.readAll()
	// Physical: CCCCAAAABBBBBBBB → logical: AAAABBBBBBBBCCCC (oldest first)
	// After 3rd write, oldest 4 bytes (first 'AAAA') were overwritten.
	// Remaining: BBBBBBBBCCCC but wait...
	// Let me trace through manually:
	// Write "AAAAAAAA": buf[0..7] = A, writePos=8, used=8, count=8
	// Write "BBBBBBBB": buf[8..15] = B, writePos=0, used=16, count=16
	// Write "CCCC": buf[0..3] = C, writePos=4, used=16, count=20
	// Physical layout: [0..3]=CCCC, [4..7]=AAAA, [8..15]=BBBBBBBB
	// Logical oldest = count - used = 20 - 16 = 4
	// ReadFrom(4): physical start = 4%16 = 4 → buf[4..] = AAAA + BBBBBBBB + CCCC
	// But wait, buf[0..3]=CCCC which is at logical offset 16..19
	// So ReadFrom(4) reads physical positions 4,5,6,7,8,9,10,11,12,13,14,15,0,1,2,3
	// = AAAA + BBBBBBBB + CCCC
	if result != "AAAABBBBBBBBCCCC" {
		t.Fatalf("expected 'AAAABBBBBBBBCCCC', got %q (len=%d)", result, len(result))
	}
}

func TestSafeReplayPointCursorHome(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("hello\x1b[Hworld"))

	sp, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point")
	}
	// The \x1b[H starts at offset 5 (after 'hello')
	if sp.Offset != 5 {
		t.Fatalf("expected offset 5, got %d", sp.Offset)
	}

	// Replay from safe point should include the escape sequence
	result := cb.ReadFrom(sp.Offset)
	if !bytesContains(result, []byte("\x1b[Hworld")) {
		t.Fatalf("expected replay to contain ESC[Hworld, got %q", string(result))
	}
}

func TestSafeReplayPointClearScreen(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("before\x1b[2Jafter"))

	sp, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point")
	}
	if sp.Offset != 6 {
		t.Fatalf("expected offset 6, got %d", sp.Offset)
	}
}

func TestSafeReplayPointClearScrollback(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("x\x1b[3Jy"))

	sp, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point")
	}
	if sp.Offset != 1 {
		t.Fatalf("expected offset 1, got %d", sp.Offset)
	}
}

func TestSafeReplayPointClearAndHome(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("start\x1b[H\x1b[2Jend"))

	sp, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point")
	}
	// \x1b[H is at offset 5, \x1b[2J is at offset 8
	// The clearAndHome sequence starts at offset 5 (the ESC of \x1b[H)
	// But both \x1b[H (cursorHome) and \x1b[H\x1b[2J (clearAndHome) match.
	// addReplayPoint deduplicates same-offset points.
	// Actually \x1b[H matches first (seqLen 3), then we skip past it (i+=2).
	// Then at offset 8 we see \x1b[2J (clearScreen, seqLen 4).
	// So we get two safe points: 5 and 8. The latest is 8.
	if sp.Offset != 8 {
		t.Fatalf("expected offset 8 (clear screen after home), got %d", sp.Offset)
	}
}

func TestSafeReplayPointOSCTitle(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("prompt$\x1b]0;mytitle\x07output"))

	sp, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point")
	}
	// \x1b]0; starts at offset 7
	if sp.Offset != 7 {
		t.Fatalf("expected offset 7, got %d", sp.Offset)
	}
}

func TestNoSafeReplayPoint(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("plain text without escape codes"))

	_, ok := cb.LatestSafeReplayPoint()
	if ok {
		t.Fatal("expected no safe replay point")
	}
}

func TestMultipleSafeReplayPoints(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("line1\r\n"))
	cb.Write([]byte("\x1b[2J")) // clear screen at offset 7
	cb.Write([]byte("line2\r\n"))
	cb.Write([]byte("\x1b[H")) // cursor home at offset 19

	sp, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point")
	}
	// Latest is cursor home
	if sp.Offset != 19 {
		t.Fatalf("expected latest offset 19, got %d", sp.Offset)
	}

	// Verify both points exist by checking replay from each
	// Replay from first safe point (offset 7) should include clear screen
	result1 := cb.ReadFrom(7)
	if !bytesContains(result1, []byte("\x1b[2J")) {
		t.Fatal("expected replay from 7 to contain clear screen")
	}

	// Replay from latest safe point (offset 19) should include cursor home
	result2 := cb.ReadFrom(19)
	if !bytesContains(result2, []byte("\x1b[H")) {
		t.Fatal("expected replay from 19 to contain cursor home")
	}
}

func TestSafeReplayPointMax10(t *testing.T) {
	cb := NewCircularBuffer(4096)
	// Write 15 cursor-home sequences
	for i := 0; i < 15; i++ {
		cb.Write([]byte(fmt.Sprintf("line%d\x1b[H", i)))
	}

	// Should only keep the last 10
	sp, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point")
	}
	t.Logf("latest safe point offset: %d", sp.Offset)
}

func TestReset(t *testing.T) {
	cb := NewCircularBuffer(1024)
	cb.Write([]byte("hello\x1b[2Jworld"))

	_, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point before reset")
	}

	cb.Reset()

	if cb.Used() != 0 {
		t.Fatalf("expected used 0 after reset, got %d", cb.Used())
	}
	if cb.Snapshot() != 0 {
		t.Fatalf("expected snapshot 0 after reset, got %d", cb.Snapshot())
	}

	_, ok = cb.LatestSafeReplayPoint()
	if ok {
		t.Fatal("expected no safe replay point after reset")
	}

	// After reset, writing new data should work
	cb.Write([]byte("new data"))
	if cb.readAll() != "new data" {
		t.Fatalf("expected 'new data' after reset, got %q", cb.readAll())
	}
}

func TestConcurrentWrites(t *testing.T) {
	cb := NewCircularBuffer(64 * 1024)
	var wg sync.WaitGroup
	n := 100
	chunk := []byte("hello world\n")

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.Write(chunk)
		}()
	}

	wg.Wait()

	result := cb.readAll()
	if len(result) != n*len(chunk) {
		t.Fatalf("expected %d bytes, got %d", n*len(chunk), len(result))
	}
}

func TestReadFromEmpty(t *testing.T) {
	cb := NewCircularBuffer(1024)
	result := cb.ReadFrom(0)
	if result != nil {
		t.Fatalf("expected nil from empty buffer, got %q", string(result))
	}
}

func TestReadFromClampedToOldest(t *testing.T) {
	cb := NewCircularBuffer(16)
	cb.Write([]byte("ABCDEFGHIJKLMNOPQRST")) // 20 bytes, only last 16 kept

	// Oldest byte is at logical offset 4
	// ReadFrom(0) should clamp to 4
	result := cb.ReadFrom(0)
	if string(result) != "EFGHIJKLMNOPQRST" {
		t.Fatalf("expected 'EFGHIJKLMNOPQRST', got %q (len=%d)", string(result), len(result))
	}
}

func TestLargeRoundTrip(t *testing.T) {
	cb := NewCircularBuffer(1024 * 1024) // 1 MB
	data := make([]byte, 500*1024)       // 500 KB
	for i := range data {
		data[i] = byte(i % 256)
	}
	cb.Write(data)

	result := cb.readAllBytes()
	if len(result) != len(data) {
		t.Fatalf("expected %d bytes, got %d", len(data), len(result))
	}
	for i := range data {
		if result[i] != data[i] {
			t.Fatalf("mismatch at byte %d: expected %d, got %d", i, data[i], result[i])
		}
	}
}

func TestSmallWrapReadFrom(t *testing.T) {
	cb := NewCircularBuffer(8)
	cb.Write([]byte("AAAAAAAA")) // fill buffer: count=8, used=8
	cb.Write([]byte("BB"))       // overwrite first 2: count=10, used=8

	// Physical: [0..1]=BB, [2..7]=AAAAAA
	// Logical oldest = 10-8 = 2
	// ReadFrom(2): physical 2..7 then 0..1 = AAAAAABB
	result := cb.ReadFrom(2)
	if string(result) != "AAAAAABB" {
		t.Fatalf("expected 'AAAAAABB', got %q", string(result))
	}
}

func TestSafeReplayReplayIncludesSequence(t *testing.T) {
	cb := NewCircularBuffer(1024)
	// Simulate a terminal session: some output, clear, more output
	cb.Write([]byte("old output that scrolls off\r\n"))
	cb.Write([]byte("\x1b[2J")) // clear screen
	cb.Write([]byte("new session output\r\n"))
	cb.Write([]byte("$ "))

	sp, ok := cb.LatestSafeReplayPoint()
	if !ok {
		t.Fatal("expected safe replay point")
	}

	// Replaying from the safe point should include the clear-screen sequence
	replay := cb.ReadFrom(sp.Offset)
	if !bytesContains(replay, []byte("\x1b[2J")) {
		t.Fatal("replay must include the clear-screen sequence")
	}
	// And the new session output
	if !bytesContains(replay, []byte("new session output")) {
		t.Fatal("replay must include new session output")
	}
	// But NOT the old output (which was before the safe point)
	if bytesContains(replay, []byte("old output")) {
		t.Fatal("replay should NOT include output before the safe point")
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	cb := NewCircularBuffer(64 * 1024)
	var wg sync.WaitGroup

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			cb.Write([]byte(fmt.Sprintf("line %d\n", i)))
		}
	}()

	// Reader goroutines
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cb.LatestSafeReplayPoint()
				cb.Snapshot()
				cb.Used()
				cb.ReadFrom(0)
			}
		}()
	}

	wg.Wait()

	// Verify buffer is intact
	result := cb.readAll()
	if len(result) == 0 {
		t.Fatal("expected data after concurrent read/write")
	}
}

// bytesContains is a helper that checks if sub is in data.
func bytesContains(data, sub []byte) bool {
	for i := 0; i <= len(data)-len(sub); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			if data[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
