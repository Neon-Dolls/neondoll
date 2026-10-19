package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gorilla/websocket"
)

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
		cs.sendError(wsConn, 0, ErrInternal, "registration ID generation failed")
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
				cs.detachBody(rid)
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
				// Binary frame from Core → forward to Body WSS or UDP.
				// If an authenticated Body WSS attachment exists for this
				// route, deliver through WSS only. Otherwise fall back to
				// the Relay UDP path. This ensures no duplicate delivery
				// and provides natural UDP fallback when a WSS attachment
				// detaches or is replaced.
				frame, err := UnmarshalFrame(raw)
				if err != nil {
					cs.log.Debug("control_server: unmarshal frame", "error", err)
					continue
				}
				if !core.routes[frame.RouteID] {
					cs.log.Debug("control_server: frame for unknown route", "route_id", frame.RouteID)
					continue
				}

				cs.mu.RLock()
				bodyConn, hasBody := cs.body[frame.RouteID]
				cs.mu.RUnlock()
				if hasBody {
					// WSS attached → deliver through WSS only (no UDP).
					select {
					case bodyConn.inbox <- frame.Payload:
						// enqueued for body wss delivery
					default:
						bodyConn.dropCount.Add(1)
						cs.log.Debug("control_server: body wss queue full, dropping packet",
							"route_id", frame.RouteID)
					}
				} else {
					// No Body WSS → deliver through the existing Relay UDP path.
					cs.udpSendCount.Add(1)
					if err := cs.svc.UDP().SendTo(frame.RouteID, frame.Payload); err != nil {
						cs.log.Debug("control_server: sendto udp", "route_id", frame.RouteID, "error", err)
					}
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
				cs.detachBody(m.RouteID)

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
