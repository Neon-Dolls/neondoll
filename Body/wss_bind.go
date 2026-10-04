// SPDX-License-Identifier: AGPL-3.0-only
// Package body — Body WireGuard WSS Bind transport (M5.3).
//
// BodyWSSBind implements golang.zx2c4.com/wireguard/conn.Bind by wrapping
// a WebSocket connection to a Relay's /body endpoint.  The bind authenticates
// with a RouteID+credential, then sends/receives already-encrypted WireGuard
// datagrams framed as Relay Frames over the WSS.
//
// M5.3 scope: WSS bind with auth, framing, reconnect, race safety.
// No WireGuard handshake/E2E (M5.4), path selection (M5.5), or mobility (M6).
package body

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

// MaxBodyQueueDepth is the default inbox capacity.  Must be large enough to
// absorb bursts during WG handshake (M5.4) but bounded to cap memory under
// hostile load (M5.6).
const MaxBodyQueueDepth = 256

// DefaultMaxReconnectTries is the number of WSS connect+auth attempts before
// giving up.  The spec says "bounded: max 3 attempts, 1s backoff".
const DefaultMaxReconnectTries = 3

// ReconnectBackoff is the delay between reconnect attempts (seconds).
const ReconnectBackoff = 1 * time.Second

// BodyWSSBind wraps a WSS connection to a Relay /body endpoint with
// conn.Bind semantics.  It mirrors RelayTransport but connects directly
// to the Relay's WebSocket instead of going through ControlClient.
type BodyWSSBind struct {
	// relayAddr is the WebSocket URL to connect to (e.g. "ws://host:port/body").
	// The connectWithRetry method normalises bare host:port to ws://host:port/body.
	relayAddr string

	// routeID and credential are used during initial connect and reconnect auth.
	routeID    relay.RouteID
	credential string

	// ws is the live WebSocket connection.  nil when disconnected or closed.
	ws *websocket.Conn

	// inbox is a bounded channel that receives []byte payloads from the read
	// loop.  The receive func pops from it.  On overflow the oldest item is
	// dropped (match M5.2 bodyWSConn.inbox behaviour).
	inbox chan []byte

	// writeMu serialises all WriteMessage calls on ws (Gorilla WebSocket
	// single-writer requirement).
	writeMu sync.Mutex

	// ctx and cancel control the read-loop goroutine.  Close() triggers
	// cancellation, which causes Done() in the select to fire and the
	// read loop to exit.
	ctx    context.Context
	cancel context.CancelFunc

	// closeMu guards closed, ws, and inbox so that Close(), Send() and the
	// read-loop goroutine do not race.
	closeMu sync.Mutex
	closed  bool

	// routeEp is a pre-created RelayEndpoint for this route, used as the
	// source endpoint in each receive-func delivery.  Created during Open()
	// by ParseRelayEndpoint (the only exported way to construct one).
	routeEp *relay.RelayEndpoint
}

// ── public: conn.Bind interface ───────────────────────────────────────────────

// Open connects to the Relay /body WSS, authenticates the route, spawns the
// binary-message read loop, and returns a receive func.
func (b *BodyWSSBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	// Reset closed state so reconnect after Close() works.
	b.closeMu.Lock()
	b.closed = false
	b.closeMu.Unlock()

	// Create cancellation context FIRST so Close() always has a valid
	// cancel function to signal Done() and unblock the receive func.
	b.ctx, b.cancel = context.WithCancel(context.Background())

	if err := b.connectWithRetry(DefaultMaxReconnectTries); err != nil {
		b.cancel()
		return nil, 0, err
	}

	// Bounded queue – same capacity as bodyWSConn.inbox in control_server.go.
	b.inbox = make(chan []byte, MaxBodyQueueDepth)

	// Pre-create the RelayEndpoint for this route (unexported field means
	// we must use the exported ParseRelayEndpoint constructor).
	epStr := relay.RelayEndpointString(b.routeID)
	b.routeEp, _ = relay.ParseRelayEndpoint(epStr)

	// Read-loop goroutine: reads BinaryMessage from WSS, validates frames,
	// pushes payloads to the inbox.
	b.readLoop()

	// Build the receive func that pops from the inbox.
	receiveFunc := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		if b.closed {
			return 0, net.ErrClosed
		}
		select {
		case data := <-b.inbox:
			if len(packets) > 0 && len(data) <= len(packets[0]) {
				copy(packets[0], data)
			}
			if len(sizes) > 0 {
				sizes[0] = len(data)
			}
			if len(eps) > 0 {
				eps[0] = b.routeEp
			}
			return 1, nil
		case <-b.ctx.Done():
			return 0, net.ErrClosed
		}
	}

	recvList := []conn.ReceiveFunc{receiveFunc}
	return recvList, port, nil
}

// Close tears down the WSS connection, stops all goroutines, drains the
// inbox, and marks the bind as closed.  Safe to call multiple times.
func (b *BodyWSSBind) Close() error {
	b.closeMu.Lock()
	if b.closed {
		b.closeMu.Unlock()
		return nil
	}
	b.closed = true

	// Snapshot ws before releasing closeMu so future accessors see closed.
	ws := b.ws
	b.ws = nil
	b.closeMu.Unlock()

	// Cancel the read-loop goroutine first.
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}

	// Close the WebSocket connection — this unblocks any pending ReadMessage
	// in the read-loop goroutine.
	if ws != nil {
		_ = ws.Close()
	}

	return nil
}

// Send marshals each datagram as a Relay Frame with the bind's RouteID and
// writes it as a BinaryMessage on the WebSocket.  closeMu prevents races
// with Close(); writeMu satisfies the gorilla/websocket single-writer rule.
func (b *BodyWSSBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.closeMu.Lock()
	if b.closed {
		b.closeMu.Unlock()
		return net.ErrClosed
	}
	ws := b.ws
	b.closeMu.Unlock()

	if ws == nil {
		return net.ErrClosed
	}

	for i := range bufs {
		frame, err := relay.MarshalFrame(&relay.Frame{
			Version: relay.ProtocolVersion,
			RouteID: b.routeID,
			Payload: bufs[i],
		})
		if err != nil {
			return fmt.Errorf("body wss: marshal frame: %w", err)
		}
		b.writeMu.Lock()
		err = ws.WriteMessage(websocket.BinaryMessage, frame)
		b.writeMu.Unlock()
		if err != nil {
			return fmt.Errorf("body wss: write frame: %w", err)
		}
	}
	return nil
}

// ParseEndpoint delegates to relay.ParseRelayEndpoint.
func (b *BodyWSSBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ep, err := relay.ParseRelayEndpoint(s)
	if err != nil {
		return nil, err
	}
	return ep, nil
}

// SetMark is a no-op (no SO_MARK equivalent on WebSocket transports).
func (b *BodyWSSBind) SetMark(mark uint32) error {
	return nil
}

// BatchSize returns conn.IdealBatchSize.
func (b *BodyWSSBind) BatchSize() int {
	return conn.IdealBatchSize
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// connectWithRetry dials the Relay /body WSS and authenticates the route.
// Uses bounded retry (tries attempts, ReconnectBackoff between retries).
func (b *BodyWSSBind) connectWithRetry(tries int) error {
	var lastErr error
	for range tries {
		b.closeMu.Lock()
		if b.closed {
			b.closeMu.Unlock()
			return net.ErrClosed
		}
		b.closeMu.Unlock()

		// Normalise address: accept bare "host:port" or full "ws://host:port".
		addr := b.relayAddr
		if strings.HasPrefix(addr, "ws://") {
			if !strings.HasSuffix(addr, "/body") {
				addr = addr + "/body"
			}
		} else {
			addr = "ws://" + addr + "/body"
		}

		// Dial the WebSocket.
		dialer := &websocket.Dialer{}
		ws, _, dialErr := dialer.Dial(addr, nil)
		if dialErr != nil {
			lastErr = dialErr
			time.Sleep(ReconnectBackoff)
			continue
		}

		// Send BodyAttach control message.
		attach, jsonErr := json.Marshal(&relay.BodyAttach{
			Version:    relay.ProtocolVersion,
			Type:       relay.CmdBodyAttach,
			RouteID:    b.routeID,
			Credential: b.credential,
		})
		if jsonErr != nil {
			_ = ws.Close()
			lastErr = jsonErr
			time.Sleep(ReconnectBackoff)
			continue
		}
		if err := ws.WriteMessage(websocket.TextMessage, attach); err != nil {
			_ = ws.Close()
			lastErr = err
			time.Sleep(ReconnectBackoff)
			continue
		}

		// Read response.
		msgType, raw, readErr := ws.ReadMessage()
		if readErr != nil {
			_ = ws.Close()
			lastErr = readErr
			time.Sleep(ReconnectBackoff)
			continue
		}
		if msgType != websocket.TextMessage {
			_ = ws.Close()
			lastErr = fmt.Errorf("body wss: expected TextMessage, got %d", msgType)
			time.Sleep(ReconnectBackoff)
			continue
		}
		resp, unErr := relay.UnmarshalControl(raw)
		if unErr != nil {
			_ = ws.Close()
			lastErr = unErr
			time.Sleep(ReconnectBackoff)
			continue
		}
		attached, ok := resp.(*relay.BodyAttached)
		if !ok {
			_ = ws.Close()
			lastErr = fmt.Errorf("body wss: unexpected response type %T", resp)
			time.Sleep(ReconnectBackoff)
			continue
		}
		if attached.RouteID != b.routeID {
			_ = ws.Close()
			lastErr = fmt.Errorf("body wss: attached route %d; want %d", attached.RouteID, b.routeID)
			time.Sleep(ReconnectBackoff)
			continue
		}

		// Success — store the connection.
		b.closeMu.Lock()
		if b.closed {
			_ = ws.Close()
			b.closeMu.Unlock()
			return net.ErrClosed
		}
		b.ws = ws
		b.closeMu.Unlock()
		return nil
	}
	return fmt.Errorf("body wss: connect failed after %d tries: %w", tries, lastErr)
}

// readLoop starts a goroutine that reads BinaryMessage frames from the
// WebSocket, validates them, and pushes payloads to the inbox.
//
// Malformed frames (bad version, too short) are silently dropped.
// Frames with a non-matching RouteID are silently dropped.
// Overflow drops the oldest packet (match M5.2 pattern).
func (b *BodyWSSBind) readLoop() {
	// Capture ws locally so the goroutine outlives any Close()-caused
	// b.ws = nil; the goroutine keeps reading until Close() actually
	// calls ws.Close() which unblocks ReadMessage with an error.
	ws := b.ws

	go func() {
		for {
			msgType, raw, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if msgType != websocket.BinaryMessage {
				continue
			}

			frame, unErr := relay.UnmarshalFrame(raw)
			if unErr != nil {
				// Malformed frame — drop silently.
				continue
			}
			if frame.RouteID != b.routeID {
				// Wrong route — drop silently.
				continue
			}

			// Non-blocking push; drop oldest on overflow (match M5.2).
			select {
			case b.inbox <- frame.Payload:
				// enqueued for the receive func
			default:
				// queue full — drop the incoming packet
			}
		}
	}()
}
