package relay

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ErrNotConnected is returned when an operation is attempted before Start.
var ErrNotConnected = errors.New("relay: control client not connected")

// ErrClientClosed is returned after Shutdown.
var ErrClientClosed = errors.New("relay: control client closed")

// ErrRegistrationFailed is returned when the Relay rejects the registration.
var ErrRegistrationFailed = errors.New("relay: registration rejected by Relay")

// ErrConnectionLost is returned when pending operations are failed due to
// connection loss before the operation's caller timeout.
var ErrConnectionLost = errors.New("relay: connection lost")

// managedRoute tracks a route that should be restored after reconnect.
type managedRoute struct {
	Credentials       RouteCredentials
	AllocatedEndpoint string
}

// pendingOp represents an outstanding control request awaiting a response.
// connGen ties the operation to a specific connection generation so that
// stale readLoop goroutines from an old connection cannot satisfy operations
// belonging to a newer connection.
type pendingOp struct {
	routeID RouteID
	ch      chan errorOrMsg
	connGen int64
}

type errorOrMsg struct {
	msg any
	err error
}

// ControlClient manages an authenticated outbound WSS/TLS control tunnel
// to a Relay. It handles:
//   - WSS dial + TLS handshake
//   - Core registration with the Relay
//   - Route open/close over the control tunnel
//   - Automatic reconnect with exponential backoff
//   - Route restoration after reconnection
//   - Liveness via WebSocket-level ping frames
//   - Clean cancellation and shutdown
type ControlClient struct {
	config ClientConfig

	connMu sync.Mutex // guards conn and connCancel writes
	conn   *websocket.Conn

	// connCtx/connCancel are scoped to the current connection;
	// cancelled before a new connect() or during Shutdown.
	connCtx    context.Context
	connCancel context.CancelFunc

	// connGen increments on every connect() call. Each readLoop carries the
	// generation it was created with, and dispatch() only satisfies pending
	// ops whose connGen matches. This prevents a stale readLoop from
	// satisfying an operation from a newer connection.
	connGen atomic.Int64

	relayID  atomic.Value
	writeMu  sync.Mutex
	routesMu sync.RWMutex
	routes   map[RouteID]*managedRoute

	pendingMu sync.Mutex
	pending   map[string]*pendingOp

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	connected atomic.Bool
	closed    atomic.Bool

	Reconnects       atomic.Int64
	RoutesRestored   atomic.Int64
	LastConnectError atomic.Value

	// frameHandlerErrors counts errors returned by the registered frame
	// handler (e.g. queue-full drops). Observable via FrameHandlerErrors().
	frameHandlerErrors atomic.Int64

	// frameHandler is called by readLoop when a binary Frame message arrives
	// from the Relay. Set by RelayTransport on Open; cleared on Close.
	frameHandler atomic.Value
}

// NewControlClient creates a ControlClient with the given config.
func NewControlClient(cfg ClientConfig) *ControlClient {
	cfg.ApplyDefaults()
	return &ControlClient{
		config:  cfg,
		routes:  make(map[RouteID]*managedRoute),
		pending: make(map[string]*pendingOp),
		done:    make(chan struct{}),
	}
}

// RelayID returns the Relay's assigned identity after a successful registration.
func (c *ControlClient) RelayID() RelayID {
	v := c.relayID.Load()
	if v == nil {
		return ""
	}
	return RelayID(v.(string))
}

// Start dials the Relay, completes the registration, and spawns background
// goroutines for reading, pinging, and reconnecting.
func (c *ControlClient) Start(parent context.Context) error {
	if c.closed.Load() {
		return ErrClientClosed
	}
	if c.connected.Load() {
		return nil
	}

	c.ctx, c.cancel = context.WithCancel(parent)

	if err := c.connect(c.ctx); err != nil {
		return err
	}

	go c.reconnectLoop()

	return nil
}

// Shutdown gracefully closes the control tunnel. It cancels the
// client context, closes the WebSocket connection, and waits for
// the reconnect loop to exit. All pending operations return
// ErrClientClosed.
func (c *ControlClient) Shutdown() error {
	c.closed.Store(true)

	if c.cancel != nil {
		c.cancel()
	}

	// Cancel connection-level goroutines
	c.connMu.Lock()
	if c.connCancel != nil {
		c.connCancel()
	}
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()

	if conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"),
			time.Now().Add(time.Second))
		conn.Close()
	}

	c.pendingMu.Lock()
	for k, op := range c.pending {
		if op != nil {
			select {
			case op.ch <- errorOrMsg{err: ErrClientClosed}:
			default:
			}
		}
		delete(c.pending, k)
	}
	c.pendingMu.Unlock()

	<-c.done

	c.connected.Store(false)
	return nil
}

// Metrics returns accumulated counters for the control client's
// reconnect and restoration behaviour.
func (c *ControlClient) Metrics() ControlClientMetrics {
	return ControlClientMetrics{
		Reconnects:         c.Reconnects.Load(),
		RoutesRestored:     c.RoutesRestored.Load(),
		FrameHandlerErrors: c.frameHandlerErrors.Load(),
	}
}

// FrameHandlerErrors returns the number of inbound frames whose
// registered frame handler returned an error (e.g. queue-full drops).
func (c *ControlClient) FrameHandlerErrors() int64 {
	return c.frameHandlerErrors.Load()
}

// ControlClientMetrics exposes observable counters.
type ControlClientMetrics struct {
	Reconnects         int64
	RoutesRestored     int64
	FrameHandlerErrors int64
}

// IsConnected reports whether the client currently has an established
// and registered control tunnel.
func (c *ControlClient) IsConnected() bool {
	return c.connected.Load()
}
