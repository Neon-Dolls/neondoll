// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
	"golang.zx2c4.com/wireguard/conn"
)

// MaxBodyQueueDepth is the default inbox capacity.  Must be large enough to
// hold a full WireGuard burst without dropping packets.
const MaxBodyQueueDepth = 256

// DefaultMaxReconnectTries is the number of WSS connect+auth attempts
// performed after an unexpected connection loss before the bind gives up.
const DefaultMaxReconnectTries = 3

// ReconnectBackoff is the delay between reconnect attempts.
const ReconnectBackoff = 1 * time.Second

// ErrReconnectExhausted is the terminal error returned by Receive (and Send)
// once bounded automatic reconnect attempts have all failed after an
// unexpected WSS loss.  The transport is permanently dead until a fresh Open.
var ErrReconnectExhausted = errors.New("body wss: reconnect exhausted")

// BodyWSSBind wraps a WSS connection to a Relay /body endpoint with
// conn.Bind semantics.  It mirrors RelayTransport but connects directly
// to the Relay's WebSocket instead of going through ControlClient.
//
// Lifecycle:
//
//   - Open connects to the Relay /body WebSocket, authenticates the route
//     (BodyAttach/BodyAttached handshake), and starts a read goroutine.  It
//     returns a single receive func that pops validated packet payloads from
//     a bounded inbox.
//   - An unexpected WSS loss (remote close, read/write error on a live
//     transport) triggers bounded automatic reconnection: the bind re-dials
//     and re-authenticates the SAME RouteID + credential, then resumes the
//     transport transparently.  Backoff is bounded and cancellable.
//   - If reconnect attempts are exhausted, the transport terminates with
//     ErrReconnectExhausted: Receive and Send fail cleanly (Receive unblocks
//     instead of blocking forever).
//   - Close permanently shuts the transport down and NEVER triggers a
//     reconnect.  A fresh Open re-creates the transport.
//
// The transport is one lifecycle "epoch": every Open creates a fresh context,
// receive funcs and inbox, and the read goroutine captures that epoch's
// context so a stale goroutine can never touch a newer epoch's state.
type BodyWSSBind struct {
	// relayAddr is the WebSocket URL to connect to (e.g. "ws://host:port/body").
	// The connectWithRetry method normalises bare host:port to ws://host:port/body.
	relayAddr string

	// routeID and credential are used during initial connect and reconnect auth.
	routeID    relay.RouteID
	credential string

	// Test/tuning knobs — zero values select the package defaults.  Tests may
	// set these before Open for fast, controlled reconnect behaviour.
	reconnectTries   int
	reconnectBackoff time.Duration
	queueDepth       int

	// ws is the live WebSocket connection.  nil when disconnected or closed.
	ws *websocket.Conn

	// writeMu serialises all WriteMessage calls on ws (Gorilla WebSocket
	// single-writer requirement).
	writeMu sync.Mutex

	// closeMu guards all mutable transport state below.
	closeMu     sync.Mutex
	closed      bool
	terminalErr error

	// ctx and cancel control the current epoch's read-loop goroutine and
	// unblock the receive funcs.  Cancellation means the epoch is over
	// (Close or reconnect exhaustion).
	ctx    context.Context
	cancel context.CancelFunc

	// inbox is the bounded channel the read loop pushes payloads into.
	// Created per-epoch in Open, captured by that epoch's read goroutine
	// and receive func.
	inbox chan []byte

	// routeEp is a pre-created RelayEndpoint for this route, used as the
	// source endpoint in each receive-func delivery.  Created during Open()
	// by ParseRelayEndpoint (the only exported way to construct one).
	routeEp *relay.RelayEndpoint

	// processed counts BinaryMessage frames the read loop has consumed
	// (delivered to the inbox or dropped on overflow).  Observability for
	// tests to synchronize deterministically; not part of the contract.
	processed atomic.Int64
}

// NewWSSBind creates a BodyWSSBind for the given Relay /body endpoint.
// relayAddr is the Relay's WSS address (e.g. "ws://host:port/"),
// routeID identifies the open route to attach to, and credential
// authenticates this bind against that route's stored credential.
func NewWSSBind(relayAddr string, routeID relay.RouteID, credential string) *BodyWSSBind {
	return &BodyWSSBind{
		relayAddr:  relayAddr,
		routeID:    routeID,
		credential: credential,
	}
}

// ── public: conn.Bind interface ───────────────────────────────────────────────

// Open connects to the Relay /body WSS, authenticates the route, spawns the
// binary-message read loop, and returns a receive func.
func (b *BodyWSSBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.closeMu.Lock()
	if !b.closed && b.ws != nil {
		b.closeMu.Unlock()
		return nil, 0, fmt.Errorf("body wss: bind already open")
	}
	// Fresh epoch: clear terminal state from a previous Close() or
	// exhausted-reconnect termination so this Open can authenticate and
	// run normally, then tear down the previous epoch's lifecycle
	// (normally already done by Close, but be defensive against
	// double-Open).  Always set ws=nil so a stale read-loop goroutine
	// can never observe the old connection again; it exits via the
	// b.ctx != ctx epoch check.
	b.closed = false
	b.terminalErr = nil
	if b.cancel != nil {
		b.cancel()
	}
	if b.ws != nil {
		_ = b.ws.Close()
		b.ws = nil
	}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	ctx := b.ctx
	b.closeMu.Unlock()

	// Connect (with bounded retry) before starting the read loop so Open
	// surfaces auth failures instead of silently returning a dead bind.
	if err := b.connectWithRetry(b.effectiveReconnectTries(), ctx); err != nil {
		b.closeMu.Lock()
		b.cancel()
		b.closeMu.Unlock()
		return nil, 0, err
	}

	// Bounded queue – same capacity as bodyWSConn.inbox in control_server.go.
	b.closeMu.Lock()
	b.inbox = make(chan []byte, b.effectiveQueueDepth())
	inbox := b.inbox
	b.closeMu.Unlock()

	// Pre-create the RelayEndpoint for this route (unexported field means
	// we must use the exported ParseRelayEndpoint constructor).
	epStr := relay.RelayEndpointString(b.routeID)
	b.routeEp, _ = relay.ParseRelayEndpoint(epStr)

	// Read-loop goroutine: reads BinaryMessage from WSS, validates frames,
	// pushes payloads to the inbox; reconnects automatically on loss.
	b.readLoop()

	// Build the receive func that pops from the inbox.  It captures this
	// epoch's ctx and inbox so a later Open can never rewire it.
	receiveFunc := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		// If the epoch is already over, fail immediately even if stale
		// packets are still queued (Close/exhaustion wins over delivery).
		select {
		case <-ctx.Done():
			return 0, b.currentTerminalErr()
		default:
		}
		select {
		case data := <-inbox:
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
		case <-ctx.Done():
			return 0, b.currentTerminalErr()
		}
	}

	return []conn.ReceiveFunc{receiveFunc}, port, nil
}

// Close tears down the WSS transport and cancels the current epoch.  It never
// triggers a reconnect: the read goroutine aborts any in-flight reconnection,
// Receive unblocks with net.ErrClosed, and Send fails with net.ErrClosed.
// Safe to call multiple times.
func (b *BodyWSSBind) Close() error {
	b.closeMu.Lock()
	if b.closed {
		b.closeMu.Unlock()
		return nil
	}
	b.closed = true
	b.terminalErr = net.ErrClosed

	// Snapshot ws and cancel before releasing closeMu so the read goroutine
	// sees closed and aborts any reconnect attempt.
	ws := b.ws
	b.ws = nil
	cancel := b.cancel
	b.closeMu.Unlock()

	// Cancel the epoch first so in-flight reconnect backoff aborts; closing
	// the websocket unblocks any pending ReadMessage in the read goroutine.
	if cancel != nil {
		cancel()
	}
	if ws != nil {
		_ = ws.Close()
	}
	return nil
}

// Send marshals each datagram as a Relay Frame with the bind's RouteID and
// writes it as a BinaryMessage on the current WebSocket.  Returns the
// terminal error if the epoch is closed or reconnect-exhausted.
func (b *BodyWSSBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.closeMu.Lock()
	if b.closed {
		err := b.terminalErr
		b.closeMu.Unlock()
		if err != nil {
			return err
		}
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

// dialAndAuth dials the Relay /body WSS endpoint and authenticates the route
// with the bind's configured credential.  On failure the websocket is closed
// and an error is returned; on success the connected, authenticated
// websocket is returned (not yet installed as b.ws).
func (b *BodyWSSBind) dialAndAuth() (*websocket.Conn, error) {
	closeOnErr := func(ws *websocket.Conn, err error) (*websocket.Conn, error) {
		_ = ws.Close()
		return nil, err
	}

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
		return nil, fmt.Errorf("body wss: dial: %w", dialErr)
	}

	// Send BodyAttach control message.
	attach, jsonErr := json.Marshal(&relay.BodyAttach{
		Version:    relay.ProtocolVersion,
		Type:       relay.CmdBodyAttach,
		RouteID:    b.routeID,
		Credential: b.credential,
	})
	if jsonErr != nil {
		return closeOnErr(ws, fmt.Errorf("body wss: marshal attach: %w", jsonErr))
	}
	if err := ws.WriteMessage(websocket.TextMessage, attach); err != nil {
		return closeOnErr(ws, fmt.Errorf("body wss: write attach: %w", err))
	}

	// Read response.
	msgType, raw, readErr := ws.ReadMessage()
	if readErr != nil {
		return closeOnErr(ws, fmt.Errorf("body wss: read attached: %w", readErr))
	}
	if msgType != websocket.TextMessage {
		return closeOnErr(ws, fmt.Errorf("body wss: expected attached TextMessage, got %d", msgType))
	}
	resp, unErr := relay.UnmarshalControl(raw)
	if unErr != nil {
		return closeOnErr(ws, fmt.Errorf("body wss: unmarshal attached: %w", unErr))
	}
	attached, ok := resp.(*relay.BodyAttached)
	if !ok {
		return closeOnErr(ws, fmt.Errorf("body wss: unexpected response type %T", resp))
	}
	if attached.RouteID != b.routeID {
		return closeOnErr(ws, fmt.Errorf("body wss: attached route %d, want %d", attached.RouteID, b.routeID))
	}

	return ws, nil
}

// connectWithRetry dials and authenticates up to tries times, waiting
// effectiveReconnectBackoff between attempts.  ctx cancellation (Close or a
// newer epoch) aborts the wait immediately.  Returns nil once a connection
// is installed as b.ws.
func (b *BodyWSSBind) connectWithRetry(tries int, ctx context.Context) error {
	var lastErr error
	for range tries {
		if !b.epochAlive(ctx) {
			return net.ErrClosed
		}

		ws, err := b.dialAndAuth()
		if err != nil {
			lastErr = err
			if !b.awaitBackoff(ctx) {
				return net.ErrClosed
			}
			continue
		}

		// Install only if still in the same epoch and not closed.
		b.closeMu.Lock()
		if b.closed || b.ctx != ctx {
			b.closeMu.Unlock()
			_ = ws.Close()
			return net.ErrClosed
		}
		old := b.ws
		b.ws = ws
		b.closeMu.Unlock()
		if old != nil && old != ws {
			_ = old.Close()
		}
		return nil
	}
	return fmt.Errorf("body wss: connect failed after %d tries: %w", tries, lastErr)
}

// reconnect attempts to re-establish a lost transport after an unexpected
// WSS loss: bounded attempts with cancellable backoff, each re-authenticating
// the SAME RouteID + credential.
//
// Returns true when the transport has been restored (a new WebSocket is
// installed and the caller should resume reading).  Returns false when the
// transport is permanently dead: either the epoch was cancelled (Close — no
// reconnect runs) or the bounded attempts were exhausted, in which case the
// bind terminates with ErrReconnectExhausted and the receive funcs unblock
// with that error.
func (b *BodyWSSBind) reconnect(ctx context.Context) bool {
	maxTries := b.effectiveReconnectTries()
	var lastErr error
	for attempt := 1; attempt <= maxTries; attempt++ {
		if !b.awaitBackoff(ctx) {
			return false
		}
		if !b.epochAlive(ctx) {
			return false
		}

		newWS, err := b.dialAndAuth()
		if err != nil {
			lastErr = err
			continue
		}

		// Reject a stale connection: only install if this epoch is still
		// current and the bind is not closed.
		b.closeMu.Lock()
		if b.closed || b.ctx != ctx {
			b.closeMu.Unlock()
			_ = newWS.Close()
			return false
		}
		old := b.ws
		b.ws = newWS
		b.closeMu.Unlock()
		if old != nil && old != newWS {
			_ = old.Close()
		}
		return true
	}

	b.terminate(fmt.Errorf("%w: %v", ErrReconnectExhausted, lastErr))
	return false
}

// terminate permanently shuts down the transport with err as the terminal
// error surfaced by Receive and Send.  Safe to call from the read goroutine;
// no-op if the bind is already closed (Close's net.ErrClosed wins).
func (b *BodyWSSBind) terminate(err error) {
	b.closeMu.Lock()
	if b.closed {
		b.closeMu.Unlock()
		return
	}
	b.closed = true
	b.terminalErr = err
	ws := b.ws
	b.ws = nil
	cancel := b.cancel
	b.closeMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ws != nil {
		_ = ws.Close()
	}
}

// epochAlive reports whether ctx is the current epoch and the bind is not
// closed.  Used to abort stale reconnection work after Close or a newer Open.
func (b *BodyWSSBind) epochAlive(ctx context.Context) bool {
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	return !b.closed && b.ctx == ctx
}

// awaitBackoff waits effectiveReconnectBackoff, aborting early when ctx is
// cancelled.  Returns false when the wait was aborted.
func (b *BodyWSSBind) awaitBackoff(ctx context.Context) bool {
	select {
	case <-time.After(b.effectiveReconnectBackoff()):
		return true
	case <-ctx.Done():
		return false
	}
}

// currentTerminalErr returns the epoch's terminal error, defaulting to
// net.ErrClosed if the bind was never explicitly terminated.
func (b *BodyWSSBind) currentTerminalErr() error {
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	if b.terminalErr != nil {
		return b.terminalErr
	}
	return net.ErrClosed
}

// readLoop starts a goroutine bound to the current epoch that reads
// BinaryMessage frames from the live WebSocket, validates them, and pushes
// payloads to the epoch's inbox.
//
// Malformed frames (bad version, too short) are silently dropped.
// Frames with a non-matching RouteID are silently dropped.
// Overflow drops the incoming packet (match M5.2 pattern).
// Unexpected connection loss triggers bounded automatic reconnect; on
// exhaustion the transport terminates so Receive unblocks.  ctx cancellation
// (Close or terminate) aborts reconnection and exits the goroutine.
func (b *BodyWSSBind) readLoop() {
	b.closeMu.Lock()
	ctx := b.ctx
	inbox := b.inbox
	b.closeMu.Unlock()

	go func(ctx context.Context, inbox chan []byte) {
		for {
			// Re-fetch the live ws each iteration so a reconnect installs
			// the new connection and stale ones are never read.
			b.closeMu.Lock()
			if b.closed || b.ctx != ctx {
				b.closeMu.Unlock()
				return
			}
			ws := b.ws
			b.closeMu.Unlock()
			if ws == nil {
				return
			}

			msgType, raw, err := ws.ReadMessage()
			if err != nil {
				select {
				case <-ctx.Done():
					// Explicit Close / terminate in progress: never reconnect.
					return
				default:
				}
				// Unexpected loss — attempt reconnect.  If it fails the
				// transport terminated (exhaustion or Close); exit.
				if !b.reconnect(ctx) {
					return
				}
				continue
			}
			if msgType != websocket.BinaryMessage {
				continue
			}
			b.processed.Add(1)

			frame, unErr := relay.UnmarshalFrame(raw)
			if unErr != nil {
				// Malformed frame — drop silently.
				continue
			}
			if frame.RouteID != b.routeID {
				// Wrong route — drop silently.
				continue
			}

			// Non-blocking push; drop the incoming packet on overflow.
			select {
			case inbox <- frame.Payload:
				// enqueued for the receive func
			default:
				// queue full — drop the incoming packet
			}
		}
	}(ctx, inbox)
}

// ── effective configuration ───────────────────────────────────────────────────

func (b *BodyWSSBind) effectiveReconnectTries() int {
	if b.reconnectTries > 0 {
		return b.reconnectTries
	}
	return DefaultMaxReconnectTries
}

func (b *BodyWSSBind) effectiveReconnectBackoff() time.Duration {
	if b.reconnectBackoff > 0 {
		return b.reconnectBackoff
	}
	return ReconnectBackoff
}

func (b *BodyWSSBind) effectiveQueueDepth() int {
	if b.queueDepth > 0 {
		return b.queueDepth
	}
	return MaxBodyQueueDepth
}
