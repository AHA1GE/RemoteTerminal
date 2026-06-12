package auth

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestNewIPBlacklistEmpty(t *testing.T) {
	bl, err := NewIPBlacklist(filepath.Join(t.TempDir(), "nonexistent.txt"))
	if err != nil {
		t.Fatalf("NewIPBlacklist: %v", err)
	}
	if bl.IsBlacklisted("10.0.0.1") {
		t.Fatal("expected IP not blacklisted in fresh blacklist")
	}
}

func TestIsBlacklistedFalse(t *testing.T) {
	bl, _ := NewIPBlacklist(filepath.Join(t.TempDir(), "empty.txt"))
	if bl.IsBlacklisted("192.168.1.1") {
		t.Fatal("expected IP not blacklisted")
	}
}

func TestRecordFailedAttemptBelowThreshold(t *testing.T) {
	bl, _ := NewIPBlacklist(filepath.Join(t.TempDir(), "blacklist.txt"))
	ip := "10.0.0.55"

	for i := 0; i < 4; i++ {
		blacklisted, err := bl.RecordFailedAttempt(ip)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		if blacklisted {
			t.Fatalf("attempt %d: should not be blacklisted yet", i+1)
		}
		if bl.IsBlacklisted(ip) {
			t.Fatalf("attempt %d: IsBlacklisted should be false", i+1)
		}
	}
}

func TestRecordFailedAttemptBlacklists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blacklist.txt")
	bl, _ := NewIPBlacklist(path)
	ip := "192.168.1.100"

	// 4 attempts should not blacklist
	for i := 0; i < 4; i++ {
		bl.RecordFailedAttempt(ip)
	}

	// 5th attempt should blacklist
	blacklisted, err := bl.RecordFailedAttempt(ip)
	if err != nil {
		t.Fatalf("5th attempt: %v", err)
	}
	if !blacklisted {
		t.Fatal("5th attempt should return blacklisted=true")
	}
	if !bl.IsBlacklisted(ip) {
		t.Fatal("IP should be blacklisted after 5 failures")
	}

	// Verify file was written
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read blacklist file: %v", err)
	}
	if string(data) != ip+"\n" {
		t.Fatalf("expected %q in file, got %q", ip+"\n", string(data))
	}
}

func TestRecordFailedAttemptBeyondThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blacklist.txt")
	bl, _ := NewIPBlacklist(path)
	ip := "10.0.0.1"

	// 5 attempts to blacklist
	for i := 0; i < 5; i++ {
		bl.RecordFailedAttempt(ip)
	}

	// 6th attempt: still blacklisted but shouldn't dupe the file
	blacklisted, err := bl.RecordFailedAttempt(ip)
	if err != nil {
		t.Fatalf("6th attempt: %v", err)
	}
	if blacklisted {
		t.Fatal("6th attempt should return blacklisted=false (already blacklisted)")
	}

	// File should only have one line
	data, _ := os.ReadFile(path)
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("expected 1 line in file, got %d. content: %q", lines, string(data))
	}
}

func TestResetAttempts(t *testing.T) {
	bl, _ := NewIPBlacklist(filepath.Join(t.TempDir(), "bl.txt"))
	ip := "10.0.0.2"

	// 3 failures
	for i := 0; i < 3; i++ {
		bl.RecordFailedAttempt(ip)
	}

	bl.ResetAttempts(ip)

	// Should need 5 fresh failures to blacklist again
	for i := 0; i < 4; i++ {
		blacklisted, _ := bl.RecordFailedAttempt(ip)
		if blacklisted {
			t.Fatal("should not be blacklisted yet after reset")
		}
	}
	blacklisted, _ := bl.RecordFailedAttempt(ip)
	if !blacklisted {
		t.Fatal("should be blacklisted after 5 fresh attempts post-reset")
	}
}

func TestClientIPFromCFHeader(t *testing.T) {
	bl, _ := NewIPBlacklist("")
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("CF-Connecting-IP", "203.0.113.42")

	ip := bl.ClientIP(r)
	if ip != "203.0.113.42" {
		t.Fatalf("expected CF-Connecting-IP, got %q", ip)
	}
}

func TestClientIPFallback(t *testing.T) {
	bl, _ := NewIPBlacklist("")
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.20.30.40:56789"

	ip := bl.ClientIP(r)
	if ip != "10.20.30.40" {
		t.Fatalf("expected RemoteAddr without port, got %q", ip)
	}
}

func TestClientIPNoPort(t *testing.T) {
	bl, _ := NewIPBlacklist("")
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.20.30.40"

	ip := bl.ClientIP(r)
	if ip != "10.20.30.40" {
		t.Fatalf("expected bare RemoteAddr, got %q", ip)
	}
}

func TestLoadBlacklistFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blacklist.txt")

	err := os.WriteFile(path, []byte("# blocked IPs\n10.0.0.1\n192.168.1.1\n\n# comment\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	bl, err := NewIPBlacklist(path)
	if err != nil {
		t.Fatalf("NewIPBlacklist: %v", err)
	}

	if !bl.IsBlacklisted("10.0.0.1") {
		t.Fatal("10.0.0.1 should be blacklisted")
	}
	if !bl.IsBlacklisted("192.168.1.1") {
		t.Fatal("192.168.1.1 should be blacklisted")
	}
	if bl.IsBlacklisted("10.0.0.2") {
		t.Fatal("10.0.0.2 should NOT be blacklisted")
	}
}

func TestConcurrentAccess(t *testing.T) {
	bl, _ := NewIPBlacklist(filepath.Join(t.TempDir(), "bl.txt"))
	done := make(chan struct{})

	// Concurrent readers and writers
	for i := 0; i < 10; i++ {
		go func(n int) {
			ip := "10.0.0." + string(rune('0'+n))
			for j := 0; j < 100; j++ {
				bl.IsBlacklisted(ip)
				bl.RecordFailedAttempt(ip)
				bl.ResetAttempts(ip)
				bl.ClientIP(&http.Request{RemoteAddr: ip + ":1234"})
			}
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestLoopbackNeverBlacklisted(t *testing.T) {
	bl, _ := NewIPBlacklist(filepath.Join(t.TempDir(), "bl.txt"))

	// 127.0.0.1 should never be blacklisted, no matter how many failures.
	for i := 0; i < 100; i++ {
		blacklisted, err := bl.RecordFailedAttempt("127.0.0.1")
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		if blacklisted {
			t.Fatalf("127.0.0.1 should never be blacklisted (attempt %d)", i+1)
		}
	}
	if bl.IsBlacklisted("127.0.0.1") {
		t.Fatal("127.0.0.1 should never be blacklisted")
	}

	// IPv6 loopback too.
	for i := 0; i < 100; i++ {
		blacklisted, _ := bl.RecordFailedAttempt("::1")
		if blacklisted {
			t.Fatal("::1 should never be blacklisted")
		}
	}
	if bl.IsBlacklisted("::1") {
		t.Fatal("::1 should never be blacklisted")
	}
}
