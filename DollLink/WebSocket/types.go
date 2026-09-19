package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

// Logger is the minimal logging interface used by the WebSocket transport.
// DollLink defines its own to avoid importing Core's logger.
type Logger interface {
	Info(msg string, fields ...map[string]any)
	Warn(msg string, fields ...map[string]any)
	Error(msg string, fields ...map[string]any)
}

// Handler processes incoming events and returns response events.
type Handler interface {
	HandleEvent(ctx context.Context, event *events.Event) (*events.Event, error)
}

// upgrader upgrades HTTP connections to WebSocket.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// writeTimeout for sending messages.
const writeTimeout = 10 * time.Second

// Server is a WebSocket transport for Doll Link.
type Server struct {
	mu           sync.Mutex
	conns        map[string]*clientConn
	log          Logger
	listen       string
	mux          *http.ServeMux
	httpSrv      *http.Server
	handler      Handler
	listenerAddr string
}

// clientConn tracks a single WebSocket connection.
type clientConn struct {
	id     string
	conn   *websocket.Conn
	remote string
}

// Config for the WS server.
type Config struct {
	Listen string
}

// New creates a WS Server with the given config, logger, and event handler.
func New(cfg Config, log Logger, handler Handler) *Server {
	mux := http.NewServeMux()
	s := &Server{
		conns:   make(map[string]*clientConn),
		log:     log,
		listen:  cfg.Listen,
		mux:     mux,
		handler: handler,
	}

	s.mux.HandleFunc("/ws", s.handleWS)
	s.mux.HandleFunc("/ws/status", s.handleStatus)

	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	return s
}

// Start begins the HTTP/WebSocket server.
func (s *Server) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listenerAddr = listener.Addr().String()
	s.httpSrv.Addr = s.listenerAddr
	s.mu.Unlock()
	s.log.Info("ws server starting", map[string]any{"addr": s.listenerAddr})
	return s.httpSrv.Serve(listener)
}

// Addr returns the actual bound address (available after Start).
func (s *Server) Addr() string {
	s.mu.Lock()
	addr := s.listenerAddr
	s.mu.Unlock()
	return addr
}

// Shutdown stops the server gracefully.
func (s *Server) Shutdown(ctx context.Context) error {
	s.log.Info("ws server shutting down")
	s.mu.Lock()
	srv := s.httpSrv
	conns := s.conns
	s.conns = make(map[string]*clientConn)
	s.mu.Unlock()

	for id, cc := range conns {
		cc.conn.Close()
		_ = id
	}
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

// ConnCount returns the number of tracked connections.
func (s *Server) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error("ws upgrade failed", map[string]any{"error": err.Error(), "remote": r.RemoteAddr})
		return
	}

	cc := &clientConn{
		id:     fmt.Sprintf("conn-%d", time.Now().UnixNano()),
		conn:   conn,
		remote: r.RemoteAddr,
	}

	s.mu.Lock()
	s.conns[cc.id] = cc
	count := len(s.conns)
	s.mu.Unlock()

	s.log.Info("ws client connected", map[string]any{
		"id":     cc.id,
		"remote": cc.remote,
		"total":  count,
	})

	s.readLoop(cc)
}

// readLoop reads messages from a WebSocket connection until it closes.
func (s *Server) readLoop(cc *clientConn) {
	defer func() {
		cc.conn.Close()
		s.mu.Lock()
		delete(s.conns, cc.id)
		count := len(s.conns)
		s.mu.Unlock()
		s.log.Info("ws client disconnected", map[string]any{
			"id":    cc.id,
			"total": count,
		})
	}()

	for {
		_, message, err := cc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				s.log.Warn("ws read error", map[string]any{"id": cc.id, "error": err.Error()})
			}
			return
		}

		// Parse the incoming event.
		var event events.Event
		if err := json.Unmarshal(message, &event); err != nil {
			s.log.Warn("ws invalid message", map[string]any{"id": cc.id, "error": err.Error()})
			// Send back an error event.
			errResp := events.NewErrorResponse("", "", fmt.Sprintf("invalid message: %v", err))
			s.writeJSON(cc, errResp)
			continue
		}

		// Process through the handler.
		if s.handler != nil {
			response, err := s.handler.HandleEvent(context.Background(), &event)
			if err != nil {
				s.log.Error("ws handler error", map[string]any{"id": cc.id, "error": err.Error()})
				errResp := events.NewErrorResponse(event.ID, event.DollID, fmt.Sprintf("handler error: %v", err))
				s.writeJSON(cc, errResp)
				continue
			}

			// Echo the request ID as correlation ID if the response doesn't already have one.
			if response.CorrelationID == "" && event.ID != "" {
				response.CorrelationID = event.ID
			}

			if err := s.writeJSON(cc, *response); err != nil {
				s.log.Error("ws write error", map[string]any{"id": cc.id, "error": err.Error()})
				return
			}
		} else {
			// No handler configured — echo back an error.
			errResp := events.NewErrorResponse(event.ID, event.DollID, "no handler configured")
			s.writeJSON(cc, errResp)
		}
	}
}

// writeJSON sends a JSON-encoded event to a client connection.
func (s *Server) writeJSON(cc *clientConn, event events.Event) error {
	cc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return cc.conn.WriteJSON(event)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	count := len(s.conns)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"connections":%d,"status":"running"}`, count)
}
