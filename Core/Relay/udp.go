package relay

// M4.4 — Relay UDP ingress and routing.
//
// Design invariants:
//   - Each route owns exactly one public UDP endpoint (a real UDP socket
//     bound to an allocated address). Endpoints are never shared.
//   - Routing is by route ID only: a datagram arriving on route A's
//     endpoint is delivered exclusively to the owning Core registration's
//     packet sink. The route → registration mapping is read from the
//     Registry at dispatch AND at delivery time, so a route can never
//     leak across registrations.
//   - Datagrams are fully opaque: payload bytes are copied and handed to
//     the sink unmodified. The relay never parses or inspects packet
//     contents, retains no keys, and derives no identity from source
//     endpoints.
//   - Source endpoints (addr:port) are observed as runtime topology only:
//     the last-seen source is tracked for return routing but is never
//     treated as a Doll or network identity.
//   - Each route has a bounded receive queue. When full, incoming
//     datagrams are dropped and counted (dropped_queue_full) — explicit
//     backpressure instead of unbounded buffering.
//   - Size limits: zero-length and oversized datagrams are rejected with
//     dedicated drop counters. Maximum payload is MaxFramePayloadSize
//     (65535) unless the config lowers it.
//   - Teardown: closing a route closes its socket and releases the port;
//     stopping the service closes every listener and waits for the read /
//     deliver goroutines to finish.
//
// The Core-tunnel side of M4.4 terminates at a PacketSink; tests observe
// delivery through TestSink.

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrUDPRouteNotFound is returned when tearing down a route that has
	// no UDP endpoint (already closed or never opened).
	ErrUDPRouteNotFound = errors.New("relay: udp route not found")

	// ErrUDPClosed is returned when allocating an endpoint on a listener
	// that has been shut down.
	ErrUDPClosed = errors.New("relay: udp listener is shut down")

	// shutdownTimeout bounds how long Shutdown waits for the read and
	// deliver goroutines to drain.
	shutdownTimeout = 5 * time.Second
)

// PacketSink is the Core-tunnel boundary for datagram delivery. M4.4
// terminates this side at TestSink; future phases replace it with real
// Core packet injection. Implementations must be safe for concurrent use.
type PacketSink interface {
	// Deliver hands one opaque datagram to the owning Core boundary.
	// payload is a fresh copy owned by the sink; the relay retains
	// nothing after the call returns.
	Deliver(routeID RouteID, payload []byte, source SourceEndpoint)
}

// Datagram is one inbound UDP datagram flowing through a route's bounded
// queue.
type Datagram struct {
	Payload []byte
	Source  SourceEndpoint
}

// DeliveredDatagram is one observed delivery at a sink.
type DeliveredDatagram struct {
	RouteID  RouteID
	Payload  []byte
	Source   SourceEndpoint
	Received time.Time
}

// TestSink collects delivered datagrams for test observation. It is safe
// for concurrent use and blocks waiters via a condition variable.
type TestSink struct {
	mu   sync.Mutex
	cond *sync.Cond
	got  []DeliveredDatagram
}

// NewTestSink returns an empty TestSink.
func NewTestSink() *TestSink {
	s := &TestSink{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Deliver appends a copy of the datagram to the sink's collection.
// The payload is copied so the sink's view stays byte-identical even if
// the relay's buffer is reused.
func (s *TestSink) Deliver(routeID RouteID, payload []byte, source SourceEndpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, DeliveredDatagram{
		RouteID:  routeID,
		Payload:  append([]byte(nil), payload...),
		Source:   source,
		Received: time.Now(),
	})
	s.cond.Broadcast()
}

// Count returns the number of datagrams collected so far.
func (s *TestSink) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

// Clear forgets all previously collected datagrams.
func (s *TestSink) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = nil
}

// For returns a copy of all datagrams delivered for the given route.
func (s *TestSink) For(routeID RouteID) []DeliveredDatagram {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []DeliveredDatagram
	for _, d := range s.got {
		if d.RouteID == routeID {
			out = append(out, d)
		}
	}
	return out
}

// WaitFor blocks until at least n datagrams have been delivered overall,
// or the timeout elapses. It returns a copy of the datagrams observed so
// far.
func (s *TestSink) WaitFor(n int, timeout time.Duration) []DeliveredDatagram {
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if len(s.got) >= n {
			return append([]DeliveredDatagram(nil), s.got...)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return append([]DeliveredDatagram(nil), s.got...)
		}
		s.cond.Wait() //nolint:staticcheck // SA2003 unused fields: cond is held by s.mu
	}
}

// WaitForRoute blocks until at least n datagrams for routeID have been
// delivered, or the timeout elapses. It returns a copy of that route's
// datagrams observed so far.
func (s *TestSink) WaitForRoute(routeID RouteID, n int, timeout time.Duration) []DeliveredDatagram {
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	collect := func() []DeliveredDatagram {
		var out []DeliveredDatagram
		for _, d := range s.got {
			if d.RouteID == routeID {
				out = append(out, d)
			}
		}
		return out
	}
	for {
		got := collect()
		if len(got) >= n {
			return got
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return got
		}
		s.cond.Wait() //nolint:staticcheck // SA2003 unused fields: cond is held by s.mu
	}
}

// routeHandle is one route's listener, bounded queue, and delivery loop.
type routeHandle struct {
	routeID RouteID
	conn    *net.UDPConn
	queue   chan Datagram // bounded: cap = UDP.MaxQueueDepth
	done    chan struct{} // closed on teardown; deliver loop exits
	closed  atomic.Bool

	mu       sync.Mutex
	lastSeen SourceEndpoint
}

// UDPListener implements the relay's UDP ingress path: per-route public
// endpoints, opaque datagram ingestion, bounded queues with explicit
// drop-on-full, source-endpoint observation, strict route isolation, and
// full teardown of listeners.
type UDPListener struct {
	cfg      UDPConfig
	registry *Registry
	metrics  *Metrics

	mu     sync.RWMutex
	routes map[RouteID]*routeHandle
	sinks  map[RegistrationID]PacketSink
	closed bool

	wg sync.WaitGroup
}

// NewUDPListener creates the UDP ingress path bound to the given
// registry and metrics. It allocates no sockets until Bind is called.
func NewUDPListener(cfg UDPConfig, registry *Registry, metrics *Metrics) *UDPListener {
	return &UDPListener{
		cfg:      cfg,
		registry: registry,
		metrics:  metrics,
		routes:   make(map[RouteID]*routeHandle),
		sinks:    make(map[RegistrationID]PacketSink),
	}
}

// SetSink binds the Core-tunnel boundary for a registration. Datagrams
// arriving on any route owned by the registration are delivered here.
// Replacing a sink is allowed; the sink itself is never identity.
func (u *UDPListener) SetSink(regID RegistrationID, sink PacketSink) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if sink == nil {
		delete(u.sinks, regID)
		return
	}
	u.sinks[regID] = sink
}

// Bind allocates a new public UDP endpoint for the given route and starts
// its read and deliver loops. Each route gets its own listener; binding
// the same route twice fails. The returned endpoint is the route's
// exclusive public UDP ingress address.
func (u *UDPListener) Bind(routeID RouteID) (UDPEndpoint, error) {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return "", ErrUDPClosed
	}
	if _, ok := u.routes[routeID]; ok {
		u.mu.Unlock()
		return "", fmt.Errorf("relay: route %d already has a udp endpoint", routeID)
	}

	conn, err := u.listenSocket()
	if err != nil {
		u.mu.Unlock()
		return "", fmt.Errorf("relay: bind udp endpoint for route %d: %w", routeID, err)
	}

	h := &routeHandle{
		routeID: routeID,
		conn:    conn,
		queue:   make(chan Datagram, u.cfg.MaxQueueDepth),
		done:    make(chan struct{}),
	}
	// Register the handle and its goroutines under the lock so a
	// concurrent Shutdown cannot observe the route and call wg.Wait()
	// before Add has happened.
	u.wg.Add(2)
	u.routes[routeID] = h
	u.mu.Unlock()

	go u.readLoop(h)
	go u.deliverLoop(h)

	return UDPEndpoint(conn.LocalAddr().String()), nil
}

// listenSocket opens a UDP socket. When a port range is configured the
// first free port in the range is used (the OS is the source of truth for
// availability); otherwise an ephemeral port is assigned.
func (u *UDPListener) listenSocket() (*net.UDPConn, error) {
	ip := net.ParseIP(u.cfg.ListenAddress)
	if ip == nil {
		return nil, fmt.Errorf("relay: invalid udp listen address %q", u.cfg.ListenAddress)
	}
	if u.cfg.PortMin == 0 && u.cfg.PortMax == 0 {
		return net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: 0})
	}
	var lastErr error
	for port := u.cfg.PortMin; port <= u.cfg.PortMax; port++ {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: port})
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("relay: no free udp port in [%d,%d]: %w",
		u.cfg.PortMin, u.cfg.PortMax, lastErr)
}

// readLoop receives datagrams on one route's endpoint, applies size
// limits, and hands valid datagrams to dispatch. It exits when the
// socket is closed.
func (u *UDPListener) readLoop(h *routeHandle) {
	defer u.wg.Done()

	buf := make([]byte, u.cfg.MaxPacketSize+1) // +1 to detect oversize
	for {
		n, addr, err := h.conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed (teardown) or fatal
		}

		u.metrics.incUDPDatagramsReceived()
		u.metrics.addUDPBytesIn(int64(n))

		if n == 0 {
			// Zero-length UDP datagrams are legal at the socket layer but
			// carry no packet; reject explicitly.
			u.metrics.incUDPDatagramsDroppedZeroLength()
			continue
		}
		if n > u.cfg.MaxPacketSize {
			u.metrics.incUDPDatagramsDroppedOversized()
			continue
		}

		src := SourceEndpoint{IP: addr.IP.String(), Port: addr.Port}
		u.dispatch(h, buf[:n], src)
	}
}

// dispatch is the single routing decision point: it resolves the route to
// its handle and forwards the datagram into the route's bounded queue.
// Datagrams for closed/unknown routes are dropped and counted. The
// registry is consulted so a stale socket can never forward a datagram
// for a route that no longer exists.
func (u *UDPListener) dispatch(h *routeHandle, payload []byte, src SourceEndpoint) {
	if h.closed.Load() {
		u.metrics.incUDPDatagramsDroppedUnknownRoute()
		return
	}
	if _, ok := u.registry.RouteRegistration(h.routeID); !ok {
		u.metrics.incUDPDatagramsDroppedUnknownRoute()
		return
	}

	// Refresh route liveness timestamp — every valid inbound datagram
	// for a route refreshes that route, preventing premature expiration.
	_ = u.registry.TouchRoute(h.routeID)

	// Record last-seen source as runtime topology (return-routing hint).
	// This is observation only — identity is the route, never the source.
	// The payload is copied because it aliases the read loop's reused
	// buffer: a queued datagram must remain byte-identical even after the
	// next read overwrites that buffer.
	h.mu.Lock()
	h.lastSeen = src
	select {
	case h.queue <- Datagram{Payload: append([]byte(nil), payload...), Source: src}:
		h.mu.Unlock()
	default:
		// Bounded queue is full: explicit backpressure. Drop and count.
		h.mu.Unlock()
		u.metrics.incUDPDatagramsDroppedQueueFull()
	}
}

// deliverLoop drains a route's bounded queue into the owning
// registration's sink. It exits on teardown; any datagrams still queued
// at that point are dropped (counted as unknown-route: the route is no
// longer open).
func (u *UDPListener) deliverLoop(h *routeHandle) {
	defer u.wg.Done()

	for {
		select {
		case <-h.done:
			// Drain whatever is left so accounting stays consistent:
			// received == forwarded + dropped.
			for {
				select {
				case d := <-h.queue:
					u.metrics.incUDPDatagramsDroppedUnknownRoute()
					_ = d
				default:
					return
				}
			}
		case d, ok := <-h.queue:
			if !ok {
				return // queue closed; should not happen (channel never closed)
			}
			if h.closed.Load() {
				u.metrics.incUDPDatagramsDroppedUnknownRoute()
				continue
			}
			u.deliver(h.routeID, d)
		}
	}
}

// deliver resolves the route's owning registration and hands the datagram
// to that registration's sink — and only that sink. Route isolation is
// strict: the lookup maps route → exactly one registration.
func (u *UDPListener) deliver(routeID RouteID, d Datagram) {
	regID, ok := u.registry.RouteRegistration(routeID)
	if !ok {
		u.metrics.incUDPDatagramsDroppedUnknownRoute()
		return
	}
	u.mu.RLock()
	sink, ok := u.sinks[regID]
	u.mu.RUnlock()
	if !ok {
		// No Core-tunnel boundary attached to this registration yet.
		u.metrics.incUDPDatagramsDroppedNoSink()
		return
	}

	u.metrics.incUDPDatagramsForwarded()
	u.metrics.addUDPBytesOut(int64(len(d.Payload)))
	sink.Deliver(routeID, d.Payload, d.Source)
}

// Close tears down a route's UDP endpoint: the socket is closed (freeing
// the port) and the route's read/deliver loops exit. Closing a route that
// has no endpoint returns ErrUDPRouteNotFound.
func (u *UDPListener) Close(routeID RouteID) error {
	u.mu.Lock()
	h, ok := u.routes[routeID]
	if !ok {
		u.mu.Unlock()
		return ErrUDPRouteNotFound
	}
	delete(u.routes, routeID)
	u.mu.Unlock()

	h.closed.Store(true)
	_ = h.conn.Close()
	close(h.done)
	return nil
}

// SendTo sends a datagram to the last-seen source endpoint for a route.
// The datagram is sent on the route's own UDP socket, preserving the
// relay's source port. Returns ErrUDPRouteNotFound if the route has
// no open UDP endpoint. Returns an error if no source endpoint has been
// observed (no inbound datagrams yet).
func (u *UDPListener) SendTo(routeID RouteID, payload []byte) error {
	u.mu.RLock()
	h, ok := u.routes[routeID]
	u.mu.RUnlock()
	if !ok {
		return ErrUDPRouteNotFound
	}

	h.mu.Lock()
	addr := h.lastSeen
	conn := h.conn
	h.mu.Unlock()

	if addr.IP == "" || addr.Port == 0 {
		return fmt.Errorf("relay: no known source endpoint for route %d", routeID)
	}

	udpAddr := &net.UDPAddr{IP: net.ParseIP(addr.IP), Port: addr.Port}
	_, err := conn.WriteTo(payload, udpAddr)
	return err
}

// LastSeen returns the most recently observed source endpoint for a
// route. It exists for return-routing topology observation only.
func (u *UDPListener) LastSeen(routeID RouteID) (SourceEndpoint, bool) {
	u.mu.RLock()
	h, ok := u.routes[routeID]
	u.mu.RUnlock()
	if !ok {
		return SourceEndpoint{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastSeen, true
}

// ActiveRoutes returns the number of routes with live UDP endpoints.
func (u *UDPListener) ActiveRoutes() int {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return len(u.routes)
}

// Shutdown stops the UDP ingress path: every per-route listener is
// closed and all allocated endpoints are released. It blocks until all
// read and deliver goroutines have exited (bounded by shutdownTimeout).
func (u *UDPListener) Shutdown() error {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return nil
	}
	u.closed = true
	handles := make([]*routeHandle, 0, len(u.routes))
	for _, h := range u.routes {
		handles = append(handles, h)
	}
	u.routes = make(map[RouteID]*routeHandle)
	u.mu.Unlock()

	for _, h := range handles {
		h.closed.Store(true)
		_ = h.conn.Close()
		close(h.done)
	}

	done := make(chan struct{})
	go func() {
		u.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(shutdownTimeout):
		return errors.New("relay: udp shutdown timed out")
	}
}
