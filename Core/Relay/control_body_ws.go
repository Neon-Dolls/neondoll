package relay

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// cleanup checks pointer identity and will find nil or a different bwc).
// Must NOT be called while holding cs.mu.
func (cs *ControlServer) detachBody(routeID RouteID) {
	cs.mu.Lock()
	if bwc := cs.body[routeID]; bwc != nil {
		delete(cs.body, routeID)
		bwc.cancel()
		bwc.conn.Close()
	}
	cs.mu.Unlock()
}

// ── Body WSS: Inbox drain and frame handling ───────────────────────

// drainInbox is a goroutine that drains the Core→Body packet channel
// and writes binary frames to the Body WebSocket. writeMu serializes
// both control responses and binary writes to the same connection,
// satisfying Gorilla WebSocket's single-writer requirement.
func (bw *bodyWSConn) drainInbox() {
	for {
		select {
		case <-bw.ctx.Done():
			return
		case payload := <-bw.inbox:
			frame, err := MarshalFrame(&Frame{
				Version: ProtocolVersion,
				RouteID: bw.routeID,
				Payload: payload,
			})
			if err != nil {
				continue
			}
			bw.writeMu.Lock()
			_ = bw.conn.WriteMessage(websocket.BinaryMessage, frame)
			bw.writeMu.Unlock()
		}
	}
}

// handleBodyFrame validates a binary frame from a Body WebSocket and
// forwards the opaque payload to the owning Core registration's packet
// sink if validation passes. Invalid or unauthorized frames are silently
// dropped with a warning log; no malformed frame reaches the Core sink.
func (cs *ControlServer) handleBodyFrame(bwc *bodyWSConn, raw []byte) {
	frame, err := UnmarshalFrame(raw)
	if err != nil {
		cs.log.Warn("body wss: unmarshal frame failed", "route", bwc.routeID, "err", err)
		return
	}
	if frame.Version != ProtocolVersion {
		cs.log.Warn("body wss: bad frame version", "route", bwc.routeID, "version", frame.Version)
		return
	}
	if frame.RouteID == 0 || frame.RouteID > MaxRouteID {
		cs.log.Warn("body wss: invalid route ID in frame", "route", bwc.routeID, "frame_route", frame.RouteID)
		return
	}
	if frame.RouteID != bwc.routeID {
		cs.log.Warn("body wss: frame RouteID mismatch", "auth_route", bwc.routeID, "frame_route", frame.RouteID)
		return
	}
	if len(frame.Payload) == 0 {
		cs.log.Warn("body wss: zero-length packet", "route", bwc.routeID)
		return
	}

	// Route must still exist and be owned by a registration.
	regID, ok := cs.svc.Registry().RouteRegistration(frame.RouteID)
	if !ok {
		cs.log.Warn("body wss: route not in registry", "route", frame.RouteID)
		return
	}

	// Look up the Core connection and write the payload as a binary
	// frame. We marshal the frame directly rather than going through
	// the packetSink interface, because the sink lives inside the UDP
	// listener and isn't exposed for direct invocation.
	cs.mu.RLock()
	coreConn, hasCore := cs.conns[regID]
	cs.mu.RUnlock()
	if !hasCore {
		cs.log.Warn("body wss: no core registration for route", "route", frame.RouteID)
		return
	}

	out, err := MarshalFrame(&Frame{
		Version: ProtocolVersion,
		RouteID: frame.RouteID,
		Payload: frame.Payload,
	})
	if err != nil {
		cs.log.Warn("body wss: marshal frame for core", "route", frame.RouteID, "err", err)
		return
	}
	coreConn.mu.Lock()
	_ = coreConn.conn.WriteMessage(websocket.BinaryMessage, out)
	coreConn.mu.Unlock()
}

// ── Body WSS Attachment ───────────────────────────────────────────────
//
// handleBodyWS accepts an inbound WebSocket from a Body and processes
// the mandatory "attach" message to authenticate and bind the connection
// to an existing open Relay route.

func (cs *ControlServer) handleBodyWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	bodyConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		cs.log.Debug("control_server: body WS upgrade failed", "error", err)
		return
	}

	// Expect a single BodyAttach message as the first message.
	_, raw, err := bodyConn.ReadMessage()
	if err != nil {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}
	msg, err := UnmarshalControl(raw)
	if err != nil {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}
	attach, ok := msg.(*BodyAttach)
	if !ok {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}

	// Validate required fields. All failures produce the same generic
	// "route authentication failed" error to avoid route enumeration.
	if attach.Version != ProtocolVersion {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}
	if attach.RouteID == 0 {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}
	if attach.Credential == "" {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}

	// Verify route exists and is open.
	routeEntry, found := cs.svc.Registry().Route(attach.RouteID)
	if !found {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}
	if routeEntry.State != RouteStateOpen {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}

	// Verify credential matches the route's stored credential.
	stored, credErr := cs.svc.Registry().RouteCredentialsFromEntry(attach.RouteID)
	if credErr != nil {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}
	if attach.Credential != stored.Token {
		cs.sendError(bodyConn, 0, ErrAuthFailed, "route authentication failed")
		bodyConn.Close()
		return
	}

	// ── All checks passed; this Body is now attached to the route ──

	maxDepth := cs.svc.Config().MaxBodyQueueDepth
	bd := maxDepth
	if bd == 0 {
		bd = defaultBodyMaxQueueDepth
	}
	bwcCtx, bwcCancel := context.WithCancel(context.Background())

	bwc := &bodyWSConn{
		conn:      bodyConn,
		routeID:   attach.RouteID,
		writeMu:   sync.Mutex{},
		inbox:     make(chan []byte, bd),
		dropCount: atomic.Int64{},
		ctx:       bwcCtx,
		cancel:    bwcCancel,
	}

	cs.mu.Lock()
	// Replacement: cancel any previous body attachment for this route.
	if existing := cs.body[attach.RouteID]; existing != nil {
		existing.cancel()
		existing.conn.Close()
	}
	cs.body[attach.RouteID] = bwc
	cs.mu.Unlock()

	// Send success response.
	resp, _ := MarshalControl(&BodyAttached{
		Version: ProtocolVersion,
		Type:    CmdBodyAttached,
		RouteID: attach.RouteID,
	})
	_ = bodyConn.WriteMessage(websocket.TextMessage, resp)

	// ── Inbox-draining goroutine (Core→Body) ────────────────
	// Drains the bounded channel and writes binary packets to the
	// Body WebSocket, serialized via writeMu.
	go bwc.drainInbox()

	// ── Read loop (Body→Core) ────────────────────────────────
	// Reads binary frames from the Body, validates them, and
	// forwards the opaque payload to the owning Core's packet sink.
	go func() {
		defer func() {
			cs.mu.Lock()
			// Only clean up if this bodyWSConn is still the current
			// attachment (pointer identity guards against a replacement
			// that already set a new bodyWSConn for this route).
			if cs.body[attach.RouteID] == bwc {
				delete(cs.body, attach.RouteID)
			}
			cs.mu.Unlock()
			bwc.cancel()
			_ = bodyConn.Close()
		}()

		for {
			msgType, raw, err := bodyConn.ReadMessage()
			if err != nil {
				return
			}

			if msgType == websocket.BinaryMessage {
				cs.handleBodyFrame(bwc, raw)
			}
			// Text messages after auth are protocol violations; ignore.
			// Ping/pong handled by Gorilla WebSocket automatically.
		}
	}()
}
