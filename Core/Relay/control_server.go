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

	// generateRegID creates RegistrationIDs. Default uses crypto/rand
	// (GenerateRegistrationID). Overridable in tests to simulate failure.
	generateRegID func() (RegistrationID, error)
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
		generateRegID: func() (RegistrationID, error) { return GenerateRegistrationID() },
	}
	return cs
}

// Start begins listening for Core WebSocket connections.
func (cs *ControlServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/relay", cs.handleWS)

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

// handleWS upgrades an HTTP connection to WebSocket and manages the
// Core's control protocol lifecycle.
func (cs *ControlServer) handleWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		cs.log.Debug("control_server: upgrade failed", "error", err)
		return
	}

	// Expect Register as first message.
	_, raw, err := wsConn.ReadMessage()
	if err != nil {
		wsConn.Close()
		return
	}
	msg, err := UnmarshalControl(raw)
	if err != nil {
		cs.sendError(wsConn, 0, ErrAuthFailed, "invalid register message")
		wsConn.Close()
		return
	}
	reg, ok := msg.(*Register)
	if !ok || reg.Token == "" {
		cs.sendError(wsConn, 0, ErrAuthFailed, "register.token is required")
		wsConn.Close()
		return
	}

	// Verify the registration token against configured credential verifiers.
	if err := VerifyRegistrationToken(reg.Token, cs.svc.Config().Credentials); err != nil {
		cs.sendError(wsConn, 0, ErrAuthFailed, err.Error())
		wsConn.Close()
		return
	}

	// Register with the service's registry.
	regID, err := cs.generateRegID()
	if err != nil {
		cs.sendError(wsConn, 0, ErrInternal, err.Error())
		wsConn.Close()
		return
	}
	if err := cs.svc.Registry().AddRegistration(regID); err != nil {
		cs.sendError(wsConn, 0, ErrRateLimited, err.Error())
		wsConn.Close()
		return
	}

	// Set up the WS connection tracking.
	ctx, cancel := context.WithCancel(r.Context())
	core := &coreWSConn{
		conn:   wsConn,
		regID:  regID,
		routes: make(map[RouteID]bool),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	cs.mu.Lock()
	cs.conns[regID] = core
	cs.mu.Unlock()

	// Set the packet sink: UDP datagrams for this registration's routes
	// will be delivered to the WS connection as binary frames.
	cs.svc.SetPacketSink(regID, &packetSink{conn: core.conn, writeMu: &core.mu})

	// Send Registered response.
	resp, _ := MarshalControl(&Registered{
		Type:    CmdRegistered,
		RelayID: RelayID("neondoll-relay"),
	})
	_ = wsConn.WriteMessage(websocket.TextMessage, resp)

	// Enter the per-connection read loop.
	// We must re-bind the sink, Registry, and UDP references via closure.
	// The read loop uses the core's own references.
	go func() {
		defer func() {
			cs.mu.Lock()
			delete(cs.conns, regID)
			cs.mu.Unlock()
			// Collect the routes this registration owned. The read loop
			// goroutine is the only writer of core.routes, and this defer
			// runs in the same goroutine, so no extra locking is needed.
			routes := make([]RouteID, 0, len(core.routes))
			for rid := range core.routes {
				routes = append(routes, rid)
			}
			// Remove the sink on disconnect.
			cs.svc.SetPacketSink(regID, nil)
			// Remove registration from registry.
			cs.svc.Registry().RemoveRegistration(regID)
			// Release the registration's UDP endpoints so the ports are
			// free again: a reconnecting client restores its routes with
			// the same route IDs, and the endpoints must be re-bindable
			// on the same ports for the overlay path to recover.
			for _, rid := range routes {
				_ = cs.svc.UDP().Close(rid)
			}
			_ = wsConn.Close()
		}()

		for {
			msgType, raw, err := wsConn.ReadMessage()
			if err != nil {
				return
			}

			// Every message from the Core — control or binary — refreshes
			// the registration's LastKeepalive, preventing premature expiry.
			_ = cs.svc.Registry().Keepalive(regID)

			if msgType == websocket.BinaryMessage {
				// Binary frame from Core → forward to UDP
				frame, err := UnmarshalFrame(raw)
				if err != nil {
					cs.log.Debug("control_server: unmarshal frame", "error", err)
					continue
				}
				if !core.routes[frame.RouteID] {
					cs.log.Debug("control_server: frame for unknown route", "route_id", frame.RouteID)
					continue
				}
				if err := cs.svc.UDP().SendTo(frame.RouteID, frame.Payload); err != nil {
					cs.log.Debug("control_server: sendto udp", "route_id", frame.RouteID, "error", err)
				}
				continue
			}

			// Text message: control protocol
			msg, err := UnmarshalControl(raw)
			if err != nil {
				cs.log.Debug("control_server: unmarshal control", "error", err)
				continue
			}

			switch m := msg.(type) {
			case *RouteOpen:
				if err := ValidateControl(m); err != nil {
					core.reply(&RelayError{
						Type: CmdError, Code: ErrAuthFailed,
						Message: err.Error(), RouteID: m.RouteID,
					})
					continue
				}

				// Allocate UDP endpoint.
				ep, err := cs.svc.UDP().Bind(m.RouteID)
				if err != nil {
					core.reply(&RelayError{
						Type: CmdError, Code: ErrRouteLimit,
						Message: err.Error(), RouteID: m.RouteID,
					})
					continue
				}

				// Register the route in the registry with SERVER-GENERATED
				// credentials. AllocateRoute independently establishes random
				// credentials for the route; they are NOT derived from anything
				// the client presents. If the route already exists, the presented
				// credentials are verified against the route's established ones.
				if _, err := cs.svc.Registry().AllocateRoute(regID, m.RouteID); err != nil {
					if !errors.Is(err, ErrRouteAlreadyExists) {
						_ = cs.svc.UDP().Close(m.RouteID)
						core.reply(&RelayError{
							Type: CmdError, Code: ErrInternal,
							Message: err.Error(), RouteID: m.RouteID,
						})
						continue
					}

					// Existing route: the presented credential must match the
					// route's independently established credential. Never replace
					// the established credential with a client-presented one.
					stored, lookupErr := cs.svc.Registry().RouteCredentialsFromEntry(m.RouteID)
					if lookupErr != nil || m.Credentials.Token != stored.Token {
						_ = cs.svc.UDP().Close(m.RouteID)
						core.reply(&RelayError{
							Type: CmdError, Code: ErrAuthFailed,
							Message: "route credentials mismatch", RouteID: m.RouteID,
						})
						continue
					}
				}

				// Open route.
				if _, err := cs.svc.Registry().OpenRoute(regID, m.RouteID); err != nil {
					_ = cs.svc.UDP().Close(m.RouteID)
					_, _ = cs.svc.Registry().CloseRoute(regID, m.RouteID)
					core.reply(&RelayError{
						Type: CmdError, Code: ErrInternal,
						Message: err.Error(), RouteID: m.RouteID,
					})
					continue
				}

				_ = cs.svc.Registry().SetRouteEndpoint(regID, m.RouteID, string(ep))
				core.routes[m.RouteID] = true

				// Return the server-established credentials so the client learns them.
				openedCreds, _ := cs.svc.Registry().RouteCredentialsFromEntry(m.RouteID)
				core.reply(&RouteOpened{
					Type:              CmdRouteOpened,
					RouteID:           m.RouteID,
					AllocatedEndpoint: string(ep),
					Credentials:       openedCreds,
				})
				cs.log.Debug("control_server: route opened",
					"route_id", m.RouteID, "endpoint", string(ep),
					"reg_id", regID)

			case *RouteClose:
				if err := ValidateControl(m); err != nil {
					core.reply(&RelayError{
						Type: CmdError, Code: ErrAuthFailed,
						Message: err.Error(), RouteID: m.RouteID,
					})
					continue
				}
				if !core.routes[m.RouteID] {
					core.reply(&RelayError{
						Type: CmdError, Code: ErrNotFound,
						Message: "route not owned by this registration",
						RouteID: m.RouteID,
					})
					continue
				}
				_ = cs.svc.UDP().Close(m.RouteID)
				_, _ = cs.svc.Registry().CloseRoute(regID, m.RouteID)
				delete(core.routes, m.RouteID)

				core.reply(&RouteClosed{
					Type:    CmdRouteClosed,
					RouteID: m.RouteID,
				})

			default:
				cs.log.Debug("control_server: unexpected control message",
					"type", fmt.Sprintf("%T", msg))
			}
		}
	}()
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
