// Package relay — M4.6 Control Server.
//
// ControlServer bridges incoming Core WebSocket control connections with
// the Relay Service's route registry and UDP endpoints. Core's outbound
// WSS tunnel connects to this server; the server allocates UDP endpoints,
// routes inbound UDP datagrams back to Core as binary WS frames, and
// forwards Core's outbound binary frames as UDP to the remote peer.
package relay

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ── Errors ─────────────────────────────────────────────────────────────────────

var (
	// ErrServerClosed is returned when the server is shut down.
	ErrServerClosed = errors.New("relay: control server closed")
)

// ── ControlServer ──────────────────────────────────────────────────────────────

// ControlServer bridges incoming Core WS control connections with the
// Relay Service. It implements the Relay side of the Core↔Relay control
// protocol: Register, RouteOpen, RouteClose, and binary frame forwarding
// between the Core's WS tunnel and the Service's UDP endpoints.
type ControlServer struct {
	svc    *Service
	addr   string
	server *http.Server
	log    *slog.Logger

	mu    sync.RWMutex
	conns map[RegistrationID]*coreWSConn
	body  map[RouteID]*bodyWSConn

	// generateRegID creates RegistrationIDs. Default uses crypto/rand
	// (GenerateRegistrationID). Overridable in tests to simulate failure.
	generateRegID func() (RegistrationID, error)

	// udpSendCount counts Core→route frames delivered via UDP. Test-only
	// hook to verify selection between WSS and UDP delivery paths.
	udpSendCount atomic.Int64
}

// coreWSConn tracks one Core's WebSocket connection and its routes.
type coreWSConn struct {
	conn   *websocket.Conn
	regID  RegistrationID
	mu     sync.Mutex // guards write on conn — shared with packetSink
	routes map[RouteID]bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// bodyWSConn tracks one Body's WebSocket attachment to a route.
// The connection is tracked per-route; at most one bodyWSConn exists per
// route at any time. Replacement is deterministic (last-wins).
// Inbox is a bounded channel for Core→Body packets; a dedicated goroutine
// drains it and writes to the WebSocket, serialized via writeMu.
type bodyWSConn struct {
	conn      *websocket.Conn
	routeID   RouteID
	writeMu   sync.Mutex
	inbox     chan []byte
	dropCount atomic.Int64
	ctx       context.Context
	cancel    context.CancelFunc
}

// reply serializes a control message text write on the WS connection.
// The same mutex is shared with packetSink which writes binary frames.
func (c *coreWSConn) reply(msg any) {
	data, err := MarshalControl(msg)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.WriteMessage(websocket.TextMessage, data)
}

// packetSink delivers UDP datagrams from the service back to the Core's
// WS connection as binary frames. Registered via SetPacketSink so the
// Service's UDPListener delivers received datagrams to this coreWSConn.
type packetSink struct {
	conn    *websocket.Conn
	writeMu *sync.Mutex // shared with coreWSConn.mu
}

func (s *packetSink) Deliver(routeID RouteID, payload []byte, source SourceEndpoint) {
	frame, err := MarshalFrame(&Frame{
		Version: ProtocolVersion,
		RouteID: routeID,
		Payload: payload,
	})
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.WriteMessage(websocket.BinaryMessage, frame)
}

// ── ControlServer implementation ──────────────────────────────────────────────

// NewControlServer creates a ControlServer that bridges Core WS connections
// to the given Relay Service. The server listens on the given address.
func NewControlServer(svc *Service, addr string, log *slog.Logger) *ControlServer {
	if log == nil {
		log = slog.Default()
	}
	cs := &ControlServer{
		svc:           svc,
		addr:          addr,
		log:           log.With("component", "relay.control_server"),
		conns:         make(map[RegistrationID]*coreWSConn),
		body:          make(map[RouteID]*bodyWSConn),
		generateRegID: func() (RegistrationID, error) { return GenerateRegistrationID() },
		udpSendCount:  atomic.Int64{},
	}
	return cs
}

// Start begins listening for Core WebSocket connections.
func (cs *ControlServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/relay", cs.handleWS)
	mux.HandleFunc("/body", cs.handleBodyWS)

	cs.server = &http.Server{
		Addr:    cs.addr,
		Handler: mux,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	// Start in a goroutine; the context controls shutdown.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cs.server.Shutdown(shutdownCtx)
	}()

	ln, err := net.Listen("tcp", cs.addr)
	if err != nil {
		return fmt.Errorf("relay: control server listen %q: %w", cs.addr, err)
	}

	go func() {
		if err := cs.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cs.log.Error("control server serve error", "error", err)
		}
	}()

	cs.log.Info("control server started", "addr", cs.addr)
	return nil
}

// Addr returns the listening address of the control server.
func (cs *ControlServer) Addr() string {
	if cs.server == nil {
		return cs.addr
	}
	return cs.server.Addr
}

// ActiveRegistrations returns the number of currently connected Core instances.
func (cs *ControlServer) ActiveRegistrations() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.conns)
}

// VerifyRegistrationToken checks the provided token against configured
// credential verifiers (SHA-256 hex hashes) using constant-time comparison.
// The token's SHA-256 digest must match one of the configured verifier
// digests. If no verifiers are configured, registration fails (fail-closed).
func VerifyRegistrationToken(token string, credentials []string) error {
	hash := sha256.Sum256([]byte(token))
	tokenBytes := hash[:]
	for _, cred := range credentials {
		credBytes, err := hex.DecodeString(cred)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare(tokenBytes, credBytes) != 0 {
			return nil
		}
	}
	return fmt.Errorf("register.token does not match any configured credential")
}

func (cs *ControlServer) sendReply(wsConn *websocket.Conn, msg any) {
	data, err := MarshalControl(msg)
	if err != nil {
		return
	}
	_ = wsConn.WriteMessage(websocket.TextMessage, data)
}

func (cs *ControlServer) sendError(wsConn *websocket.Conn, routeID RouteID, code, message string) {
	cs.sendReply(wsConn, &RelayError{
		Type: CmdError, Code: code,
		Message: message, RouteID: routeID,
	})
}

// detachBody closes and removes any Body WSS attachment for the given route.
// Thread-safe against concurrent attach/replacement (the old body's deferred
