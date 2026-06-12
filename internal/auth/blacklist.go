// Package auth provides IP blacklisting for brute-force login protection.
package auth

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

// IPBlacklist tracks failed login attempts per IP and permanently blacklists
// IPs that exceed 5 failures. Blacklisted IPs are persisted to a text file
// in the binary's directory.
type IPBlacklist struct {
	mu             sync.Mutex
	blacklistPath  string
	blacklisted    map[string]bool
	failedAttempts map[string]int
}

// NewIPBlacklist loads an existing blacklist file from the given path. If the
// file does not exist, an empty blacklist is created. Lines starting with #
// are treated as comments.
func NewIPBlacklist(path string) (*IPBlacklist, error) {
	bl := &IPBlacklist{
		blacklistPath:  path,
		blacklisted:    make(map[string]bool),
		failedAttempts: make(map[string]int),
	}
	if err := bl.load(); err != nil {
		return nil, err
	}
	return bl, nil
}

// IsBlacklisted returns true if the IP has been permanently blacklisted.
func (bl *IPBlacklist) IsBlacklisted(ip string) bool {
	bl.mu.Lock()
	defer bl.mu.Unlock()
	return bl.blacklisted[ip]
}

// RecordFailedAttempt increments the per-IP failure counter. If the counter
// reaches 5, the IP is added to the in-memory blacklist and appended to the
// blacklist file. Returns true if the IP was just blacklisted.
func (bl *IPBlacklist) RecordFailedAttempt(ip string) (bool, error) {
	// Never blacklist the loopback address — local testing and reverse-proxy
	// setups (e.g. Cloudflare Tunnel) may send requests from 127.0.0.1.
	if ip == "127.0.0.1" || ip == "::1" {
		return false, nil
	}

	bl.mu.Lock()
	bl.failedAttempts[ip]++
	count := bl.failedAttempts[ip]
	if count < 5 {
		bl.mu.Unlock()
		return false, nil
	}
	if bl.blacklisted[ip] {
		// Already blacklisted from a previous run; no need to append again.
		bl.mu.Unlock()
		return false, nil
	}
	bl.blacklisted[ip] = true
	bl.mu.Unlock()

	if err := bl.appendToFile(ip); err != nil {
		return true, err
	}
	return true, nil
}

// ResetAttempts clears the failed-attempt counter for the given IP. Called
// after a successful login.
func (bl *IPBlacklist) ResetAttempts(ip string) {
	bl.mu.Lock()
	delete(bl.failedAttempts, ip)
	bl.mu.Unlock()
}

// ClientIP extracts the real client IP from the request. It reads the
// CF-Connecting-IP header (set by Cloudflare Tunnel) first, falling back to
// the direct remote address. The port is stripped from RemoteAddr.
func (bl *IPBlacklist) ClientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr may not have a port (e.g. Unix socket or bare IP).
		return r.RemoteAddr
	}
	return host
}

// load reads the blacklist file, one IP per line. Missing file is not an
// error — the blacklist starts empty.
func (bl *IPBlacklist) load() error {
	f, err := os.Open(bl.blacklistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open blacklist: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bl.blacklisted[line] = true
	}
	return scanner.Err()
}

// appendToFile appends a single IP address to the blacklist file. The file
// is created if it does not exist.
func (bl *IPBlacklist) appendToFile(ip string) error {
	f, err := os.OpenFile(bl.blacklistPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("open blacklist for append: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintf(f, "%s\n", ip); err != nil {
		return fmt.Errorf("write blacklist: %w", err)
	}
	return nil
}
