package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Logger"
)

// Server is the REST API server.
type Server struct {
	httpServer *http.Server
	log        *logger.Logger
	mux        *http.ServeMux
}

// Config for the API server.
type Config struct {
	Listen     string
	EnableCORS bool
}

// New creates an API Server.
func New(cfg Config, log *logger.Logger) *Server {
	mux := http.NewServeMux()
	s := &Server{
		mux: mux,
		log: log,
	}

	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/version", s.handleVersion)

	s.httpServer = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.withMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

// Start begins listening. Returns nil; call Shutdown to stop.
func (s *Server) Start() error {
	s.log.Info("api server starting", map[string]any{"addr": s.httpServer.Addr})
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("api server shutting down")
	return s.httpServer.Shutdown(ctx)
}

// RegisterHandler adds a custom handler to the mux.
func (s *Server) RegisterHandler(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
	s.log.Info("registered handler", map[string]any{"pattern": pattern})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": "0.1.0-draft",
		"build":   "core1",
	})
}

// Middleware stack.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Info("request",
			map[string]any{
				"method":   r.Method,
				"path":     r.URL.Path,
				"duration": time.Since(start).String(),
			})
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// ErrorResponse writes a JSON error.
func ErrorResponse(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// ParseErr is returned for malformed requests.
type ParseErr struct {
	Message string
}

func (e *ParseErr) Error() string {
	return fmt.Sprintf("parse error: %s", e.Message)
}

// ParseBody decodes JSON from the request body into dest.
func ParseBody(r *http.Request, dest any) error {
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		return &ParseErr{Message: err.Error()}
	}
	return nil
}