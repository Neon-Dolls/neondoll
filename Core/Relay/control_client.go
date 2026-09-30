package relay

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
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

// connect dials the Relay WSS endpoint and performs the registration exchange.
// Registration is synchronous (before readLoop starts). On success it starts
// new readLoop and pingLoop goroutines for this connection.
func (c *ControlClient) connect(ctx context.Context) error {
	// Cancel any previous connection's goroutines and close the old WebSocket
	// before dialing a new one. This prevents accumulation of active sockets
	// and goroutines across reconnect cycles.
	c.connMu.Lock()
	if c.connCancel != nil {
		c.connCancel()
	}
	oldConn := c.conn
	c.conn = nil
	connCtx, connCancel := context.WithCancel(c.ctx)
	c.connCancel = connCancel
	c.connMu.Unlock()

	if oldConn != nil {
		oldConn.Close()
	}

	// Fail all pending operations from the old connection promptly.
	// This prevents stale operations from surviving a dead WebSocket.
	c.failPending(ErrConnectionLost)

	// Bump the connection generation so that any old readLoop still running
	// cannot dispatch responses to new pending operations.
	gen := c.connGen.Add(1)

	maxMsgSize := int64(MaxControlMessageSize)
	if maxMsgSize <= 0 {
		maxMsgSize = 65536
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: c.config.HandshakeTimeout,
	}

	header := http.Header{}
	header.Set("User-Agent", "NeonDoll-Core/0.1")

	conn, _, err := dialer.DialContext(ctx, c.config.RelayURL, header)
	if err != nil {
		connCancel()
		return fmt.Errorf("relay: WSS dial %q: %w", c.config.RelayURL, err)
	}

	conn.SetReadLimit(maxMsgSize)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
	})

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	// Send Register synchronously
	regData, err := MarshalControl(&Register{
		Type:  CmdRegister,
		Token: c.config.RegistrationToken,
	})
	if err != nil {
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: marshal Register: %w", err)
	}

	if err := conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout)); err != nil {
		c.closeConn(conn)
		connCancel()
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, regData); err != nil {
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: write Register: %w", err)
	}

	// Read response synchronously (with handshake timeout deadline)
	_ = conn.SetReadDeadline(time.Now().Add(c.config.HandshakeTimeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: read Register response: %w", err)
	}

	// Reset to longer read deadline for normal operation
	_ = conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))

	resp, err := UnmarshalControl(raw)
	if err != nil {
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: unmarshal Register response: %w", err)
	}

	switch m := resp.(type) {
	case *Registered:
		// Validate the received control message before accepting it.
		// A malformed Registered (e.g. empty RelayID) must never be
		// treated as success.
		if err := ValidateControl(m); err != nil {
			c.closeConn(conn)
			connCancel()
			return fmt.Errorf("relay: invalid Registered from Relay: %w", err)
		}
		c.relayID.Store(string(m.RelayID))
		c.connected.Store(true)

		// Start per-connection goroutines, passing the generation so
		// dispatch is scoped to this connection.
		go c.readLoop(connCtx, conn, gen)
		go c.pingLoop(connCtx, conn)

		return nil
	case *RelayError:
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("%w: code=%q message=%q", ErrRegistrationFailed, m.Code, m.Message)
	default:
		c.closeConn(conn)
		connCancel()
		return fmt.Errorf("relay: unexpected registration response type: %T", resp)
	}
}

// closeConn is a helper to clean up a connection reference.
func (c *ControlClient) closeConn(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	c.connMu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	c.connMu.Unlock()
	conn.Close()
}

// failPending fails all outstanding pending operations with the given error
// and removes them from the pending map. Uses non-blocking sends so it never
// blocks on a full channel (the exchange's caller may have already consumed
// the channel value).
func (c *ControlClient) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for k, op := range c.pending {
		if op != nil {
			select {
			case op.ch <- errorOrMsg{err: err}:
			default:
			}
		}
		delete(c.pending, k)
	}
}

// exchange sends a control message and waits for its response.
func (c *ControlClient) exchange(ctx context.Context, opKey string, msg any) (any, error) {
	data, err := MarshalControl(msg)
	if err != nil {
		return nil, fmt.Errorf("relay: marshal %s: %w", opKey, err)
	}

	ch := make(chan errorOrMsg, 1)

	gen := c.connGen.Load()

	c.pendingMu.Lock()
	// If a stale pending op exists under the same key (from a prior
	// connection generation that wasn't cleaned up), fail it preemptively
	// to prevent leaking a goroutine blocked on a channel that will
	// never be consumed.
	if existing := c.pending[opKey]; existing != nil {
		select {
		case existing.ch <- errorOrMsg{err: ErrConnectionLost}:
		default:
		}
	}
	c.pending[opKey] = &pendingOp{ch: ch, connGen: gen}
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, opKey)
		c.pendingMu.Unlock()
	}()

	if err := c.writeMsg(data); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, ErrClientClosed
	case eom := <-ch:
		return eom.msg, eom.err
	}
}

// writeMsg writes a JSON control message on the WebSocket.
func (c *ControlClient) writeMsg(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}

	if err := conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// OpenRoute requests a new relay route from the Relay.
func (c *ControlClient) OpenRoute(ctx context.Context, routeID RouteID, creds RouteCredentials) (*RouteOpened, error) {
	if routeID == 0 {
		return nil, errors.New("relay: routeID must be nonzero")
	}
	if creds.Token == "" {
		return nil, errors.New("relay: RouteCredentials.Token is required")
	}
	if !c.connected.Load() {
		return nil, ErrNotConnected
	}

	opKey := routeOpKey(routeID)
	resp, err := c.exchange(ctx, opKey, &RouteOpen{
		Type:        CmdRouteOpen,
		RouteID:     routeID,
		Credentials: creds,
	})
	if err != nil {
		return nil, fmt.Errorf("relay: open route %d: %w", routeID, err)
	}

	switch m := resp.(type) {
	case *RouteOpened:
		// Validate the received control message before accepting it.
		// A malformed RouteOpened (e.g. missing AllocatedEndpoint)
		// must never be treated as success.
		if err := ValidateControl(m); err != nil {
			return nil, fmt.Errorf("relay: invalid RouteOpened: %w", err)
		}
		c.routesMu.Lock()
		c.routes[routeID] = &managedRoute{
			Credentials:       creds,
			AllocatedEndpoint: m.AllocatedEndpoint,
		}
		c.routesMu.Unlock()
		return m, nil
	case *RelayError:
		return nil, fmt.Errorf("relay: route %d rejected: code=%q message=%q",
			routeID, m.Code, m.Message)
	default:
		return nil, fmt.Errorf("relay: unexpected route open response type: %T", resp)
	}
}

// CloseRoute requests closing a route on the Relay.
func (c *ControlClient) CloseRoute(ctx context.Context, routeID RouteID) error {
	if routeID == 0 {
		return errors.New("relay: routeID must be nonzero")
	}
	if !c.connected.Load() {
		return ErrNotConnected
	}

	opKey := routeOpKey(routeID)
	resp, err := c.exchange(ctx, opKey, &RouteClose{
		Type:    CmdRouteClose,
		RouteID: routeID,
	})
	if err != nil {
		return fmt.Errorf("relay: close route %d: %w", routeID, err)
	}

	switch m := resp.(type) {
	case *RouteClosed:
		// Validate the received control message before accepting it.
		// A malformed RouteClosed (e.g. missing RouteID) must never
		// be treated as success.
		if err := ValidateControl(m); err != nil {
			return fmt.Errorf("relay: invalid RouteClosed: %w", err)
		}
		c.routesMu.Lock()
		delete(c.routes, routeID)
		c.routesMu.Unlock()
		return nil
	case *RelayError:
		return fmt.Errorf("relay: close route %d rejected: code=%q message=%q",
			routeID, m.Code, m.Message)
	default:
		return fmt.Errorf("relay: unexpected route close response type: %T", resp)
	}
}

// readLoop reads control messages from the WebSocket and dispatches
// them to the appropriate pending operation's channel.
func (c *ControlClient) readLoop(ctx context.Context, conn *websocket.Conn, gen int64) {
	defer func() {
		// Only set connected=false if this is the current connection.
		// In the window between a failed readLoop exit and the new
		// connection being established, connected stays false — the
		// reconnectLoop handles this correctly.
		c.connMu.Lock()
		isCurrent := c.conn == conn
		c.connMu.Unlock()
		if isCurrent {
			c.connected.Store(false)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout)); err != nil {
			return
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		msg, err := UnmarshalControl(raw)
		if err != nil {
			continue
		}

		// Validate every received control message before dispatching.
		// Malformed messages are silently dropped — the caller will
		// time out or observe an error via other means. This ensures
		// fail-closed behaviour even when the Relay is compromised or
		// misbehaving.
		if err := ValidateControl(msg); err != nil {
			continue
		}

		c.dispatch(msg, gen)
	}
}

// dispatch routes a control response to its pending operation, but only
// if the pending operation belongs to the same connection generation as
// the readLoop that received it. This prevents stale readLoop goroutines
// from colliding with operations from a newer connection.
func (c *ControlClient) dispatch(msg any, gen int64) {
	switch m := msg.(type) {
	case *Registered:
		c.pendingMu.Lock()
		op := c.pending["register"]
		c.pendingMu.Unlock()
		if op != nil && op.connGen == gen {
			select {
			case op.ch <- errorOrMsg{msg: m}:
			default:
			}
		}

	case *RouteOpened:
		c.dispatchByRoute(m.RouteID, msg, nil, gen)

	case *RouteClosed:
		c.dispatchByRoute(m.RouteID, msg, nil, gen)

	case *RelayError:
		if m.RouteID != 0 {
			c.dispatchByRoute(m.RouteID, msg, nil, gen)
		} else {
			// Register-scoped error; dispatch to "register" pending key.
			c.pendingMu.Lock()
			op := c.pending["register"]
			c.pendingMu.Unlock()
			if op != nil && op.connGen == gen {
				select {
				case op.ch <- errorOrMsg{msg: m}:
				default:
				}
			}
		}

	default:
	}
}

func (c *ControlClient) dispatchByRoute(routeID RouteID, msg any, msgErr error, gen int64) {
	key := routeOpKey(routeID)
	c.pendingMu.Lock()
	op := c.pending[key]
	c.pendingMu.Unlock()
	if op != nil && op.connGen == gen {
		select {
		case op.ch <- errorOrMsg{msg: msg, err: msgErr}:
		default:
		}
	}
}

func routeOpKey(routeID RouteID) string {
	return fmt.Sprintf("route:%d", routeID)
}

// pingLoop periodically sends WebSocket-level ping frames.
func (c *ControlClient) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(c.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.connMu.Lock()
			cur := c.conn
			c.connMu.Unlock()
			if cur != conn {
				// This is an old connection; exit and let the new one's loop handle pings
				return
			}
			c.writeMu.Lock()
			_ = conn.WriteControl(websocket.PingMessage, nil,
				time.Now().Add(c.config.WriteTimeout))
			c.writeMu.Unlock()
		}
	}
}

// reconnectLoop watches for connection loss and reconnects with
// exponential backoff. After reconnection it re-registers and
// restores all active routes.
func (c *ControlClient) reconnectLoop() {
	defer close(c.done)

	poll := 100 * time.Millisecond
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		// Wait until the connection drops, checking for close signal each cycle
		for c.connected.Load() {
			if c.closed.Load() {
				return
			}
			timer.Reset(poll)
			select {
			case <-c.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}

		if c.closed.Load() {
			return
		}

		backoff := c.config.ReconnectInitial

		for {
			select {
			case <-c.ctx.Done():
				return
			default:
			}

			jitter := time.Duration(0)
			if c.config.ReconnectJitter > 0 {
				n, err := rand.Int(rand.Reader, big.NewInt(int64(c.config.ReconnectJitter)))
				if err == nil {
					jitter = time.Duration(n.Int64())
				}
			}

			timer.Reset(backoff + jitter)
			select {
			case <-c.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}

			if err := c.connect(c.ctx); err != nil {
				c.LastConnectError.Store(err.Error())
				next := time.Duration(float64(backoff) * c.config.ReconnectMultiplier)
				if next > c.config.ReconnectMax {
					next = c.config.ReconnectMax
				}
				backoff = next
				continue
			}

			c.LastConnectError.Store("")
			c.Reconnects.Add(1)

			if err := c.restoreRoutes(context.Background()); err != nil {
				_ = err
			}
			break
		}
	}
}

// restoreRoutes re-opens all tracked routes on the new connection.
func (c *ControlClient) restoreRoutes(ctx context.Context) error {
	c.routesMu.RLock()
	type routeEntry struct {
		ID RouteID
		Mr *managedRoute
	}
	routes := make([]routeEntry, 0, len(c.routes))
	for id, mr := range c.routes {
		routes = append(routes, routeEntry{id, mr})
	}
	c.routesMu.RUnlock()

	if len(routes) == 0 {
		return nil
	}

	var firstErr error
	for _, r := range routes {
		resp, err := c.exchange(ctx, routeOpKey(r.ID), &RouteOpen{
			Type:        CmdRouteOpen,
			RouteID:     r.ID,
			Credentials: r.Mr.Credentials,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		switch m := resp.(type) {
		case *RouteOpened:
			if err := ValidateControl(m); err != nil {
				// Invalid RouteOpened from Relay during restore;
				// treat as a route loss, not a fatal error.
				if firstErr == nil {
					firstErr = fmt.Errorf("relay: restore route %d: invalid RouteOpened: %w", r.ID, err)
				}
				break
			}
			c.routesMu.Lock()
			if existing, ok := c.routes[r.ID]; ok {
				existing.AllocatedEndpoint = m.AllocatedEndpoint
			}
			c.routesMu.Unlock()
			c.RoutesRestored.Add(1)
		case *RelayError:
			c.routesMu.Lock()
			delete(c.routes, r.ID)
			c.routesMu.Unlock()
			if firstErr == nil {
				firstErr = fmt.Errorf("relay: restore route %d rejected: code=%q", r.ID, m.Code)
			}
		}
	}

	return firstErr
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
		Reconnects:     c.Reconnects.Load(),
		RoutesRestored: c.RoutesRestored.Load(),
	}
}

// ControlClientMetrics exposes observable counters.
type ControlClientMetrics struct {
	Reconnects     int64
	RoutesRestored int64
}

// IsConnected reports whether the client currently has an established
// and registered control tunnel.
func (c *ControlClient) IsConnected() bool {
	return c.connected.Load()
}
