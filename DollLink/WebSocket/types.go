package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Actions"
	"github.com/Neon-Dolls/neondoll/DollLink/Authentication"
	"github.com/Neon-Dolls/neondoll/DollLink/Capabilities"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
)

var ErrNoAuthenticatedSession = errors.New("ws: no authenticated execution session")

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

// ExecutionSessionLifecycle receives identities already verified by the
// configured authentication boundary. sessionID is an opaque connection
// generation used to reject stale updates and disconnects.
type ExecutionSessionLifecycle interface {
	SessionConnected(context.Context, string, string) error
	CapabilitiesAdvertised(context.Context, string, string, []capabilities.Capability) error
	SessionDisconnected(string, string)
}

type BodyAuthenticator interface {
	BeginAuthentication(context.Context, string, authentication.Hello) (authentication.Challenge, error)
	CompleteAuthentication(context.Context, string, authentication.Proof) (authentication.Accept, string, error)
	CancelAuthentication(string)
}

// upgrader upgrades HTTP connections to WebSocket.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// writeTimeout for sending messages.
const writeTimeout = 10 * time.Second
const defaultAuthenticationTimeout = 30 * time.Second

// Server is a WebSocket transport for Doll Link.
type Server struct {
	// activationMu serializes authenticated lifecycle callbacks, execution
	// ownership changes, and execution-session selection. When both locks are
	// needed, activationMu must always be acquired before mu. Lifecycle
	// callbacks run without mu held and must not synchronously re-enter methods
	// that acquire activationMu.
	activationMu sync.Mutex
	mu           sync.Mutex

	conns                 map[string]*clientConn
	execution             map[string]*clientConn
	log                   Logger
	listen                string
	mux                   *http.ServeMux
	httpSrv               *http.Server
	handler               Handler
	listenerAddr          string
	authenticateExecution func(*http.Request) (string, error)
	bodyAuthenticator     BodyAuthenticator
	authenticationTimeout time.Duration
	executionLifecycle    ExecutionSessionLifecycle

	// listener is an optional pre-created net.Listener.
	// When set (typically via SetListener before Start), the server
	// uses this listener instead of creating its own via net.Listen.
	// This enables serving through an overlay netstack (e.g., a WG
	// userspace tunnel) where host net.Listen is not reachable.
	listener net.Listener

	// writeJSONFn is a test hook. When set, writeJSON delegates to it
	// instead of writing to the real WebSocket connection.
	writeJSONFn func(cc *clientConn, event events.Event) error
}

// clientConn tracks a single WebSocket connection.
type clientConn struct {
	id        string
	conn      *websocket.Conn
	remote    string
	writeMu   sync.Mutex
	bodyID    string
	execution *execution.Client
	done      chan struct{}
}

// Config for the WS server.
type Config struct {
	Listen string

	// AuthenticateExecution may bind a stable, authenticated Body identity to
	// a connection. The callback is a trust boundary: it must authenticate the
	// request using a mechanism outside this transport and must never trust an
	// overlay address, membership record, or unverified body_id field. When nil
	// or unsuccessful, the connection remains usable for Events/Actions but is
	// ineligible for execution.
	AuthenticateExecution func(*http.Request) (string, error)

	// ExecutionSessionLifecycle integrates authenticated connection
	// generations and canonical capability advertisements with Core. It does
	// not authenticate and is never invoked without AuthenticateExecution.
	ExecutionSessionLifecycle ExecutionSessionLifecycle

	// BodyAuthenticator performs the canonical in-band Ed25519 handshake.
	// When configured, every connection remains nonprivileged until it
	// completes that handshake.
	BodyAuthenticator BodyAuthenticator

	// AuthenticationTimeout bounds the complete in-band handshake, including
	// clients that connect and send nothing. Zero uses 30 seconds.
	AuthenticationTimeout time.Duration
}

// New creates a WS Server with the given config, logger, and event handler.
func New(cfg Config, log Logger, handler Handler) *Server {
	mux := http.NewServeMux()
	s := &Server{
		conns:     make(map[string]*clientConn),
		execution: make(map[string]*clientConn),
		log:       log,
		listen:    cfg.Listen,
		mux:       mux,
		handler:   handler,
	}
	if cfg.AuthenticateExecution != nil {
		s.authenticateExecution = cfg.AuthenticateExecution
	}
	s.executionLifecycle = cfg.ExecutionSessionLifecycle
	s.bodyAuthenticator = cfg.BodyAuthenticator
	s.authenticationTimeout = cfg.AuthenticationTimeout
	if s.authenticationTimeout <= 0 {
		s.authenticationTimeout = defaultAuthenticationTimeout
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

// SetExecutionSessionLifecycle completes Core wiring when the lifecycle owner
// itself delegates execution through this Server. Call it before Start.
func (s *Server) SetExecutionSessionLifecycle(lifecycle ExecutionSessionLifecycle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executionLifecycle = lifecycle
}

// SetBodyAuthenticator installs the production in-band authentication
// boundary. Call it before Start.
func (s *Server) SetBodyAuthenticator(authenticator BodyAuthenticator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodyAuthenticator = authenticator
}

// SetListener sets an optional pre-created net.Listener for the server.
// When set before Start, the server uses this listener instead of calling
// net.Listen. This enables serving through an overlay netstack tunnel.
func (s *Server) SetListener(l net.Listener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listener = l
}

// RegisterHandler adds a bootstrap HTTP endpoint to the server that owns the
// Doll Link WebSocket. Call it before Start.
func (s *Server) RegisterHandler(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// Start begins the HTTP/WebSocket server.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()

	var err error
	if listener == nil {
		listener, err = net.Listen("tcp", s.listen)
		if err != nil {
			return err
		}
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
	s.execution = make(map[string]*clientConn)
	s.mu.Unlock()

	for _, cc := range conns {
		if cc.execution != nil {
			cc.execution.Close(execution.ErrDisconnected)
		}
		cc.conn.Close()
	}
	var shutdownErr error
	if srv != nil {
		shutdownErr = srv.Shutdown(ctx)
	}
	for _, cc := range conns {
		if cc.done == nil {
			continue
		}
		select {
		case <-cc.done:
		case <-ctx.Done():
			if shutdownErr != nil {
				return shutdownErr
			}
			return ctx.Err()
		}
	}
	return shutdownErr
}

// ConnCount returns the number of tracked connections.
func (s *Server) ConnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	var authenticatedBodyID string
	// In-band authentication takes precedence. The legacy callback remains an
	// explicit trusted fixture/boundary for deployments that have not enabled
	// Body Authentication v1; it must never bypass a configured handshake.
	if s.bodyAuthenticator == nil && s.authenticateExecution != nil {
		bodyID, err := s.authenticateExecution(r)
		if err != nil {
			s.log.Warn("ws execution authentication failed", map[string]any{"error": err.Error(), "remote": r.RemoteAddr})
		} else if bodyID == "" {
			s.log.Warn("ws execution authentication returned empty Body identity", map[string]any{"remote": r.RemoteAddr})
		} else {
			authenticatedBodyID = bodyID
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error("ws upgrade failed", map[string]any{"error": err.Error(), "remote": r.RemoteAddr})
		return
	}

	cc := &clientConn{
		id:     "conn-" + uuid.NewString(),
		conn:   conn,
		remote: r.RemoteAddr,
		done:   make(chan struct{}),
	}
	if s.bodyAuthenticator != nil {
		if err := conn.SetReadDeadline(time.Now().Add(s.authenticationTimeout)); err != nil {
			conn.Close()
			return
		}
	}
	if authenticatedBodyID != "" {
		if err := s.activateAuthenticated(r.Context(), cc, authenticatedBodyID, nil); err != nil {
			s.log.Warn("ws execution registration rejected", map[string]any{"error": err.Error(), "remote": r.RemoteAddr})
		}
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

func (s *Server) activateAuthenticated(ctx context.Context, cc *clientConn, bodyID string, beforeInstall func() error) error {
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	s.mu.Lock()
	lifecycle := s.executionLifecycle
	s.mu.Unlock()
	if lifecycle != nil {
		if err := lifecycle.SessionConnected(ctx, cc.id, bodyID); err != nil {
			return err
		}
	}
	client, err := execution.NewClient(bodyID, executionSender{cc: cc})
	if err != nil {
		return err
	}
	if beforeInstall != nil {
		if err := beforeInstall(); err != nil {
			client.Close(execution.ErrDisconnected)
			if lifecycle != nil {
				lifecycle.SessionDisconnected(cc.id, bodyID)
			}
			return err
		}
	}
	cc.bodyID, cc.execution = bodyID, client
	if err := cc.conn.SetReadDeadline(time.Time{}); err != nil {
		client.Close(execution.ErrDisconnected)
		if lifecycle != nil {
			lifecycle.SessionDisconnected(cc.id, bodyID)
		}
		return err
	}
	s.mu.Lock()
	s.conns[cc.id] = cc
	replaced := s.execution[bodyID]
	s.execution[bodyID] = cc
	s.mu.Unlock()
	if replaced != nil && replaced != cc {
		replaced.execution.Close(execution.ErrDisconnected)
		replaced.conn.Close()
	}
	return nil
}

// readLoop reads messages from a WebSocket connection until it closes.
func (s *Server) readLoop(cc *clientConn) {
	defer func() {
		if s.bodyAuthenticator != nil {
			s.bodyAuthenticator.CancelAuthentication(cc.id)
		}
		cc.conn.Close()
		if cc.execution != nil {
			s.activationMu.Lock()
			cc.execution.Close(execution.ErrDisconnected)
		}
		s.mu.Lock()
		delete(s.conns, cc.id)
		if s.execution[cc.bodyID] == cc {
			delete(s.execution, cc.bodyID)
		}
		count := len(s.conns)
		lifecycle := s.executionLifecycle
		s.mu.Unlock()
		if cc.execution != nil && lifecycle != nil {
			lifecycle.SessionDisconnected(cc.id, cc.bodyID)
		}
		if cc.execution != nil {
			s.activationMu.Unlock()
		}
		s.log.Info("ws client disconnected", map[string]any{
			"id":    cc.id,
			"total": count,
		})
		close(cc.done)
	}()

	for {
		_, message, err := cc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				s.log.Warn("ws read error", map[string]any{"id": cc.id, "error": err.Error()})
			}
			return
		}

		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(message, &envelope); err != nil {
			s.log.Warn("ws invalid message", map[string]any{"id": cc.id, "error": err.Error()})
			if s.bodyAuthenticator != nil && cc.execution == nil {
				return
			}
			errResp := events.NewErrorResponse("", "", fmt.Sprintf("invalid message: %v", err))
			s.writeJSON(cc, errResp)
			continue
		}
		if s.bodyAuthenticator != nil && cc.execution == nil {
			switch envelope.Type {
			case authentication.TypeHello:
				var hello authentication.Hello
				if err := json.Unmarshal(message, &hello); err != nil {
					return
				}
				challenge, err := s.bodyAuthenticator.BeginAuthentication(context.Background(), cc.id, hello)
				if err != nil || s.writeValue(cc, challenge) != nil {
					s.log.Warn("ws Body authentication hello rejected", map[string]any{"id": cc.id})
					return
				}
			case authentication.TypeProof:
				var proof authentication.Proof
				if err := json.Unmarshal(message, &proof); err != nil {
					return
				}
				accept, bodyID, err := s.bodyAuthenticator.CompleteAuthentication(context.Background(), cc.id, proof)
				if err != nil {
					s.log.Warn("ws Body authentication proof rejected", map[string]any{"id": cc.id})
					return
				}
				if err := s.activateAuthenticated(context.Background(), cc, bodyID, func() error { return s.writeValue(cc, accept) }); err != nil {
					s.log.Warn("ws authenticated Body activation rejected", map[string]any{"id": cc.id, "error": err.Error()})
					return
				}
			default:
				s.log.Warn("ws pre-authentication message rejected", map[string]any{"id": cc.id, "type": envelope.Type})
				return
			}
			continue
		}
		if s.bodyAuthenticator != nil && (envelope.Type == authentication.TypeHello || envelope.Type == authentication.TypeProof ||
			envelope.Type == authentication.TypeChallenge || envelope.Type == authentication.TypeAccept) {
			s.log.Warn("ws authentication message after authentication rejected", map[string]any{"id": cc.id, "type": envelope.Type})
			return
		}
		if envelope.Type == string(execution.TypeResult) {
			if cc.execution == nil {
				s.log.Warn("ws execution result on unauthenticated connection", map[string]any{"id": cc.id})
				continue
			}
			var result execution.Result
			if err := json.Unmarshal(message, &result); err != nil {
				s.log.Warn("ws invalid execution result", map[string]any{"id": cc.id, "error": err.Error()})
				continue
			}
			if err := cc.execution.HandleResult(result); err != nil {
				s.log.Warn("ws rejected execution result", map[string]any{"id": cc.id, "error": err.Error()})
			}
			continue
		}
		if envelope.Type == string(execution.TypeRequest) {
			s.log.Warn("ws rejected execution request from Body connection", map[string]any{"id": cc.id})
			continue
		}
		if envelope.Type == capabilities.Type {
			s.mu.Lock()
			lifecycle := s.executionLifecycle
			s.mu.Unlock()
			if cc.execution == nil || lifecycle == nil {
				s.log.Warn("ws capabilities on unauthenticated connection", map[string]any{"id": cc.id})
				continue
			}
			var advertisement capabilities.Advertisement
			if err := json.Unmarshal(message, &advertisement); err != nil {
				s.log.Warn("ws invalid capability advertisement", map[string]any{"id": cc.id, "error": err.Error()})
				continue
			}
			if err := advertisement.Validate(); err != nil {
				s.log.Warn("ws rejected capability advertisement", map[string]any{"id": cc.id, "error": err.Error()})
				continue
			}
			if advertisement.BodyID != cc.bodyID {
				s.log.Warn("ws capability Body identity mismatch", map[string]any{"id": cc.id})
				continue
			}
			if err := lifecycle.CapabilitiesAdvertised(context.Background(), cc.id, cc.bodyID, advertisement.Payload.Capabilities); err != nil {
				s.log.Warn("ws rejected capability update", map[string]any{"id": cc.id, "error": err.Error()})
			}
			continue
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

func (s *Server) writeValue(cc *clientConn, value any) error {
	cc.writeMu.Lock()
	defer cc.writeMu.Unlock()
	return writeJSON(cc, value)
}

// writeJSON sends a JSON-encoded event to a client connection.
func (s *Server) writeJSON(cc *clientConn, event events.Event) error {
	cc.writeMu.Lock()
	defer cc.writeMu.Unlock()
	if s.writeJSONFn != nil {
		return s.writeJSONFn(cc, event)
	}
	return writeJSON(cc, event)
}

func writeJSON(cc *clientConn, value any) error {
	cc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return cc.conn.WriteJSON(value)
}

type executionSender struct {
	cc *clientConn
}

func (sender executionSender) SendExecutionRequest(ctx context.Context, request execution.Request) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	sender.cc.writeMu.Lock()
	defer sender.cc.writeMu.Unlock()
	return writeJSON(sender.cc, request)
}

// Execute sends an execution request to the connection authenticated as
// bodyID. bodyID only selects a session that was established by the configured
// authentication boundary; it is not accepted as authentication evidence.
func (s *Server) Execute(ctx context.Context, bodyID, dollID string, payload execution.RequestPayload) (execution.Result, error) {
	s.activationMu.Lock()
	s.mu.Lock()
	cc := s.execution[bodyID]
	s.mu.Unlock()
	s.activationMu.Unlock()
	if cc == nil || cc.execution == nil {
		return execution.Result{}, fmt.Errorf("%w for Body %q", ErrNoAuthenticatedSession, bodyID)
	}
	return cc.execution.Execute(ctx, dollID, payload)
}

// DisconnectBody immediately invalidates the current authenticated session
// for bodyID. Closing the execution client releases pending requests; closing
// the socket causes the normal generation-aware disconnect lifecycle to run.
func (s *Server) DisconnectBody(bodyID string) {
	s.activationMu.Lock()
	s.mu.Lock()
	cc := s.execution[bodyID]
	s.mu.Unlock()
	if cc != nil {
		cc.execution.Close(execution.ErrDisconnected)
		_ = cc.conn.Close()
	}
	s.activationMu.Unlock()
}

// SendAction delivers an outbound action to all connected WebSocket clients.
//
// Core 1 delivery rule:
//   - Zero connected clients → error.
//   - All writes fail            → error.
//   - At least one write succeeds → nil (partial success is acceptable).
//
// Failed individual writes are logged but do not abort delivery to other
// clients. No retries, queues, receipts, or multi-Body delivery policy.
//
// Implements dispatch.Sender via an adapter in the Core boundary.
func (s *Server) SendAction(ctx context.Context, action actions.Action) error {
	s.mu.Lock()
	conns := make([]*clientConn, 0, len(s.conns))
	for _, cc := range s.conns {
		if s.bodyAuthenticator != nil && cc.execution == nil {
			continue
		}
		conns = append(conns, cc)
	}
	s.mu.Unlock()

	if len(conns) == 0 {
		return fmt.Errorf("send action: no connected clients")
	}

	// Map to an Event that clients can consume the same way as a response.
	event := actionToEvent(action)

	var (
		lastErr    error
		anySuccess bool
	)
	for _, cc := range conns {
		if err := s.writeJSON(cc, event); err != nil {
			s.log.Warn("send action: write error", map[string]any{
				"client_id": cc.id,
				"error":     err.Error(),
			})
			lastErr = err
		} else {
			anySuccess = true
		}
	}
	if !anySuccess && lastErr != nil {
		// All writes failed — no client received the action.
		return fmt.Errorf("send action: all writes failed: %w", lastErr)
	}
	// At least one client received the action — partial success is acceptable.
	return nil
}

// actionToEvent converts an actions.Action into an events.Event suitable
// for WebSocket delivery.
func actionToEvent(action actions.Action) events.Event {
	switch action.Type {
	case actions.TypeSendMessage:
		payload, _ := action.Payload.(actions.SendMessagePayload)
		return events.Event{
			Type:      events.TypeMessage,
			Source:    "core",
			Timestamp: time.Now().UTC(),
			Payload: events.MessagePayload{
				Text: payload.Text,
			},
		}
	default:
		return events.Event{
			Type:      events.TypeSystem,
			Source:    "core",
			Timestamp: time.Now().UTC(),
			Payload: events.SystemPayload{
				Event: string(action.Type),
				Data:  action.Payload,
			},
		}
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	count := len(s.conns)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"connections":%d,"status":"running"}`, count)
}
