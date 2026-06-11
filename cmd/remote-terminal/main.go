package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/AHA1GE/RemoteTerminal/internal/config"

	// Root-level assets package (embed.go with //go:embed web/*)
	assets "github.com/AHA1GE/RemoteTerminal"
)

// version is set at build time via -ldflags.
var version = "dev"

// =============================================================================
// Logger
// =============================================================================

type LogLevel int

const (
	LogNone  LogLevel = iota
	LogError
	LogDebug
)

type Logger struct {
	mu     sync.Mutex
	level  LogLevel
	writer interface{ Write([]byte) (int, error) }
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
	if fields == nil {
		fields = make(map[string]interface{})
	}
	fields["time"] = time.Now().UTC().Format(time.RFC3339)
	fields["level"] = level
	fields["msg"] = msg
	b, _ := json.Marshal(fields)
	l.mu.Lock()
	fmt.Fprintln(l.writer.(interface{ Write([]byte) (int, error) }), string(b))
	l.mu.Unlock()
}

func parseLogLevel(s string) LogLevel {
	switch strings.ToLower(s) {
	case "debug":
		return LogDebug
	case "error":
		return LogError
	default:
		return LogNone
	}
}

// =============================================================================
// Session store (in-memory)
// =============================================================================

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*userSession
}

type userSession struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*userSession),
	}
}

func (s *SessionStore) Validate(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.sessions[token]
	return ok
}

func (s *SessionStore) Create(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[token] = &userSession{
		ID:        token,
		CreatedAt: time.Now(),
	}
}

func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// =============================================================================
// Token generation
// =============================================================================

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// =============================================================================
// Password verification (Argon2id)
// =============================================================================

func verifyPassword(password, storedHash string) (bool, error) {
	// storedHash format: $argon2id$v=19$m=65536,t=3,p=2$<base64-salt>$<base64-hash>
	if !strings.HasPrefix(storedHash, "$argon2id") {
		return false, fmt.Errorf("unsupported hash format")
	}

	parts := strings.Split(storedHash, "$")
	if len(parts) != 6 {
		return false, fmt.Errorf("invalid hash format")
	}

	var memory uint32 = 65536
	var timeCost uint32 = 3
	var threads uint8 = 2

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		// Use defaults if parsing fails
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}

	computed := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(expectedHash)))

	return subtle.ConstantTimeCompare(computed, expectedHash) == 1, nil
}

// =============================================================================
// generatePasswordHash creates an Argon2id hash for a given password
// =============================================================================

func generatePasswordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	const (
		mem     uint32 = 65536
		iters   uint32 = 3
		threads uint8  = 2
		keyLen  uint32 = 32
	)

	hash := argon2.IDKey([]byte(password), salt, iters, mem, threads, keyLen)

	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		mem, iters, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

// =============================================================================
// HTTP handlers
// =============================================================================

type Server struct {
	cfg      config.Config
	log      *Logger
	sessions *SessionStore
	httpSrv  *http.Server
	exeDir   string
}

func NewServer(cfg config.Config, log *Logger, exeDir string) *Server {
	return &Server{
		cfg:      cfg,
		log:      log,
		sessions: NewSessionStore(),
		exeDir:   exeDir,
	}
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_token")
		if err != nil || !s.sessions.Validate(cookie.Value) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) apiAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_token")
		if err != nil || !s.sessions.Validate(cookie.Value) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) {
	_, err := r.Cookie("csrf_token")
	if err == nil {
		return
	}
	token, genErr := generateToken()
	if genErr != nil {
		s.log.Error("failed to generate CSRF token", map[string]interface{}{"error": genErr.Error()})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    token,
		Path:     "/",
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) validateCSRF(r *http.Request) bool {
	bodyToken := r.FormValue("csrf_token")
	cookie, err := r.Cookie("csrf_token")
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(bodyToken), []byte(cookie.Value)) == 1
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.ensureCSRF(w, r)
	data, _ := fs.ReadFile(assets.FS, "web/login.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.validateCSRF(r) {
		s.log.Debug("CSRF validation failed", map[string]interface{}{"path": r.URL.Path})
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	password := r.FormValue("password")
	ok, err := verifyPassword(password, s.cfg.PasswordHash)
	if err != nil {
		s.log.Error("password verification error", map[string]interface{}{"error": err.Error()})
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		s.log.Debug("login failed", map[string]interface{}{"remote_addr": r.RemoteAddr})
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}

	sessionToken, err := generateToken()
	if err != nil {
		s.log.Error("failed to generate session token", map[string]interface{}{"error": err.Error()})
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	s.sessions.Create(sessionToken)

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	s.log.Debug("login successful", map[string]interface{}{"remote_addr": r.RemoteAddr})

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.validateCSRF(r) {
		s.log.Debug("CSRF validation failed", map[string]interface{}{"path": r.URL.Path})
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	cookie, err := r.Cookie("session_token")
	if err == nil {
		s.sessions.Delete(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	s.log.Debug("logout", map[string]interface{}{"remote_addr": r.RemoteAddr})

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, _ := fs.ReadFile(assets.FS, "web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	data, _ := fs.ReadFile(assets.FS, "web/terminal.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleGetSessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode([]interface{}{})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	json.NewEncoder(w).Encode(map[string]string{"error": "PTY sessions not yet implemented"})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validateCSRF(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(assets.FS, "web/app.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	w.Write(data)
}

// =============================================================================
// Router setup
// =============================================================================

func (s *Server) setupRoutes() http.Handler {
	mux := http.NewServeMux()

	// Public routes (no auth)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleLoginPage(w, r)
		case http.MethodPost:
			s.handleLogin(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Static files (no auth needed for app.js)
	mux.HandleFunc("/app.js", s.handleStatic)

	// Protected page routes
	protectedPage := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			s.handleIndex(w, r)
		default:
			if strings.HasPrefix(r.URL.Path, "/terminal/") {
				s.handleTerminalPage(w, r)
			} else {
				http.NotFound(w, r)
			}
		}
	}))

	// Protected API routes
	protectedAPI := s.apiAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/sessions" && r.Method == http.MethodGet:
			s.handleGetSessions(w, r)
		case r.URL.Path == "/api/sessions" && r.Method == http.MethodPost:
			s.handleCreateSession(w, r)
		case r.URL.Path == "/logout" && r.Method == http.MethodPost:
			s.handleLogout(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/sessions/") && r.Method == http.MethodDelete:
			s.handleDeleteSession(w, r)
		default:
			http.NotFound(w, r)
		}
	}))

	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if strings.HasPrefix(path, "/api/") || path == "/logout" {
			protectedAPI.ServeHTTP(w, r)
			return
		}

		if path == "/" || strings.HasPrefix(path, "/terminal/") {
			protectedPage.ServeHTTP(w, r)
			return
		}

		http.NotFound(w, r)
	}))

	return mux
}

// =============================================================================
// Graceful shutdown
// =============================================================================

func (s *Server) Shutdown(ctx context.Context) {
	s.log.Debug("shutting down server", nil)

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
		s.log.Error("HTTP server shutdown error", map[string]interface{}{"error": err.Error()})
	}

	s.log.Debug("server stopped", nil)
}

// =============================================================================
// Main
// =============================================================================

func main() {
	log := &Logger{level: LogDebug, writer: os.Stdout}

	// 1. No CLI arguments accepted
	if len(os.Args) > 1 {
		fmt.Println("remote-terminal takes no arguments. Place config.yaml in the same directory and run without arguments.")
		os.Exit(1)
	}

	// 2. Determine executable directory
	exeDir, err := config.ExeDir()
	if err != nil {
		log.Error("failed to determine executable directory", map[string]interface{}{"error": err.Error()})
		os.Exit(1)
	}

	configPath := filepath.Join(exeDir, "config.yaml")

	// 3. Load or generate config
	cfg, err := config.Load(configPath)
	if err != nil {
		cfg = config.Default()
		if saveErr := config.Save(configPath, cfg); saveErr != nil {
			log.Error("failed to write default config", map[string]interface{}{"error": saveErr.Error(), "path": configPath})
			os.Exit(1)
		}
		fmt.Printf("Config file created: %s\n", configPath)
		fmt.Println("Edit this file, then restart.")
		os.Exit(0)
	}

	// 4. Validate password_hash is set
	if cfg.PasswordHash == "" || cfg.PasswordHash == "<argon2id>" {
		randomPassword, _ := generateToken()
		randomPassword = randomPassword[:16]
		hash, err := generatePasswordHash(randomPassword)
		if err != nil {
			log.Error("failed to generate password hash", map[string]interface{}{"error": err.Error()})
			log.Error("password_hash not set in config.yaml", nil)
			os.Exit(1)
		}
		fmt.Println("password_hash not set in config.yaml")
		fmt.Println("")
		fmt.Printf("Generated password: %s\n", randomPassword)
		fmt.Printf("Add this to your config.yaml:\n  password_hash: %s\n", hash)
		fmt.Println("")
		fmt.Println("Alternatively, generate your own with any argon2id tool.")
		os.Exit(1)
	}

	// 5. Set log level
	log.level = parseLogLevel(cfg.LogLevel)

	// 6. Load TLS certificate (user must provide cert.pem + key.pem)
	certPath := filepath.Join(exeDir, "cert.pem")
	keyPath := filepath.Join(exeDir, "key.pem")

	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		log.Error("TLS certificate not found or invalid", map[string]interface{}{
			"cert":  certPath,
			"key":   keyPath,
			"error": err.Error(),
		})
		log.Error("Place cert.pem and key.pem in the binary directory and restart.", nil)
		os.Exit(1)
	}

	// 7. Create server
	srv := NewServer(cfg, log, exeDir)
	handler := srv.setupRoutes()

	srv.httpSrv = &http.Server{
		Addr:    cfg.Listen,
		Handler: handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			MinVersion:   tls.VersionTLS12,
		},
	}

	// 8. Print startup info
	fmt.Printf("remote-terminal %s\n\n", version)
	fmt.Printf("Config:   %s\n", configPath)
	fmt.Printf("Cert:     %s\n", certPath)
	fmt.Printf("Key:      %s\n", keyPath)
	fmt.Printf("Listen:   %s\n", cfg.Listen)
	fmt.Printf("Log level: %s\n", cfg.LogLevel)
	fmt.Println()

	// 9. Start HTTPS server
	go func() {
		ln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			log.Error("failed to listen", map[string]interface{}{"error": err.Error(), "addr": cfg.Listen})
			os.Exit(1)
		}
		tlsLn := tls.NewListener(ln, srv.httpSrv.TLSConfig)
		log.Debug("server started", map[string]interface{}{
			"listen":    cfg.Listen,
			"log_level": cfg.LogLevel,
		})
		if err := srv.httpSrv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
			log.Error("server error", map[string]interface{}{"error": err.Error()})
		}
	}()

	// 10. Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Debug("received signal", map[string]interface{}{"signal": sig.String()})

	// 11. Graceful shutdown
	srv.Shutdown(context.Background())
}
