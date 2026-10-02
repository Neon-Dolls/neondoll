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
}

// coreWSConn tracks one Core's WebSocket connection and its routes.
type coreWSConn struct {
	conn   *websocket.Conn
	regID  RegistrationID
	mu     sync.Mutex // guards write on conn
	routes map[RouteID]bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// packetSink delivers UDP datagrams from the service back to the Core's
// WS connection as binary frames. Registered via SetPacketSink so the
// Service's UDPListener delivers received datagrams to this coreWSConn.
type packetSink struct {
	conn *websocket.Conn
	mu   sync.Mutex // serialize writes on the WebSocket
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
		svc:   svc,
		addr:  addr,
		log:   log.With("component", "relay.control_server"),
		conns: make(map[RegistrationID]*coreWSConn),
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

	// Register with the service's registry.
	regID := RegistrationID(fmt.Sprintf("core-%x-%d", reg.Token, time.Now().UnixNano()))
	if err := cs.svc.Registry().AddRegistration(regID, reg.Token); err != nil {
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
	cs.svc.SetPacketSink(regID, &packetSink{conn: core.conn})

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
			// Remove the sink on disconnect.
			cs.svc.SetPacketSink(regID, nil)
			// Remove registration from registry.
			cs.svc.Registry().RemoveRegistration(regID)
			_ = wsConn.Close()
		}()

		for {
			msgType, raw, err := wsConn.ReadMessage()
			if err != nil {
				return
			}

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
					cs.sendReply(wsConn, &RelayError{
						Type: CmdError, Code: ErrAuthFailed,
						Message: err.Error(), RouteID: m.RouteID,
					})
					continue
				}

				// Allocate UDP endpoint.
				ep, err := cs.svc.UDP().Bind(m.RouteID)
				if err != nil {
					cs.sendReply(wsConn, &RelayError{
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
						cs.sendReply(wsConn, &RelayError{
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
						cs.sendReply(wsConn, &RelayError{
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
					cs.sendReply(wsConn, &RelayError{
						Type: CmdError, Code: ErrInternal,
						Message: err.Error(), RouteID: m.RouteID,
					})
					continue
				}

				_ = cs.svc.Registry().SetRouteEndpoint(regID, m.RouteID, string(ep))
				core.routes[m.RouteID] = true

				// Return the server-established credentials so the client learns them.
				openedCreds, _ := cs.svc.Registry().RouteCredentialsFromEntry(m.RouteID)
				cs.sendReply(wsConn, &RouteOpened{
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
					cs.sendReply(wsConn, &RelayError{
						Type: CmdError, Code: ErrAuthFailed,
						Message: err.Error(), RouteID: m.RouteID,
					})
					continue
				}
				if !core.routes[m.RouteID] {
					cs.sendReply(wsConn, &RelayError{
						Type: CmdError, Code: ErrNotFound,
						Message: "route not owned by this registration",
						RouteID: m.RouteID,
					})
					continue
				}
				_ = cs.svc.UDP().Close(m.RouteID)
				_, _ = cs.svc.Registry().CloseRoute(regID, m.RouteID)
				delete(core.routes, m.RouteID)

				cs.sendReply(wsConn, &RouteClosed{
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
