package ws

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Logger is the minimal logging interface used by the WebSocket transport.
// DollLink defines its own to avoid importing Core's logger.
type Logger interface {
	Info(msg string, fields ...map[string]any)
	Warn(msg string, fields ...map[string]any)
	Error(msg string, fields ...map[string]any)
}

// Server is a minimal WebSocket transport stub.
type Server struct {
	mu      sync.Mutex
	conns   map[string]*conn
	log     Logger
	listen  string
	mux     *http.ServeMux
	httpSrv *http.Server
}

type conn struct {
	id     string
	remote string
}

// Config for the WS server.
type Config struct {
	Listen string
}

// New creates a WS Server stub.
func New(cfg Config, log Logger) *Server {
	mux := http.NewServeMux()
	s := &Server{
		conns:  make(map[string]*conn),
		log:    log,
		listen: cfg.Listen,
		mux:    mux,
	}

	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/ws/status", s.handleStatus)

	return s
}

// Start begins the HTTP server (WebSocket upgrade not yet implemented).
func (s *Server) Start(ctx context.Context) error {
	s.httpSrv = &http.Server{
		Addr:              s.listen,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.log.Info("ws stub starting", map[string]any{"addr": s.listen})
	return s.httpSrv.ListenAndServe()
}

// Shutdown stops the stub server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("ws stub shutting down", map[string]any{})
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
	fmt.Fprintln(w, `{"error":"websocket upgrade not yet implemented"}`)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	count := len(s.conns)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"connections":%d,"status":"stub"}`, count)
}

// ConnCount returns the number of tracked connections.
func (s *Server) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}