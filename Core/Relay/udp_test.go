package relay

import (
	"context"
	"net"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

// --- helpers -------------------------------------------------------------

const (
	testUDPListenAddr = "127.0.0.1"
	testUDPTimeout    = 5 * time.Second
)

// newUDPTestEnv builds a registry with the given registrations/routes and a
// UDPListener configured for localhost testing.
func newUDPTestEnv(t *testing.T, routes map[RegistrationID][]RouteID, queueDepth int) (*Registry, *Metrics, *UDPListener) {
	t.Helper()
	r := NewRegistry(100, 1000)
	m := NewMetrics()
	for regID, routeIDs := range routes {
		if err := r.AddRegistration(regID, "hash-"+string(regID)); err != nil {
			t.Fatalf("AddRegistration(%s): %v", regID, err)
		}
		for _, rid := range routeIDs {
			if _, err := r.AllocateRoute(regID, rid); err != nil {
				t.Fatalf("AllocateRoute(%s,%d): %v", regID, rid, err)
			}
		}
	}
	if queueDepth <= 0 {
		queueDepth = 64
	}
	cfg := UDPConfig{ListenAddress: testUDPListenAddr, MaxPacketSize: MaxFramePayloadSize, MaxQueueDepth: queueDepth}
	u := NewUDPListener(cfg, r, m)
	t.Cleanup(func() { _ = u.Shutdown() })
	return r, m, u
}

// bindSink binds a test sink to a registration and opens endpoints for all
// its routes; returns sink and endpoints keyed by route.
func bindSink(t *testing.T, u *UDPListener, regID RegistrationID, routeIDs []RouteID) (*TestSink, map[RouteID]UDPEndpoint) {
	t.Helper()
	sink := NewTestSink()
	u.SetSink(regID, sink)
	eps := make(map[RouteID]UDPEndpoint, len(routeIDs))
	for _, rid := range routeIDs {
		ep, err := u.Bind(rid)
		if err != nil {
			t.Fatalf("Bind(%d): %v", rid, err)
		}
		eps[rid] = ep
	}
	return sink, eps
}

// sendUDP fires one datagram at endpoint and returns the local source addr.
func sendUDP(t *testing.T, endpoint string, payload []byte) string {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		t.Fatalf("ResolveUDPAddr(%s): %v", endpoint, err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatalf("DialUDP(%s): %v", endpoint, err)
	}
	defer conn.Close()
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("Write(%d bytes): %v", len(payload), err)
	}
	return conn.LocalAddr().String()
}

// --- M4.4 proofs ---------------------------------------------------------

// Proof: datagrams entering route A emerge only at route A's owning Core
// boundary (the registration's sink), never at another registration's.
func TestUDP_RouteDispatchToOwningRegistration(t *testing.T) {
	t.Parallel()
	_, _, u := newUDPTestEnv(t, map[RegistrationID][]RouteID{
		"reg-a": {RouteID(1)},
		"reg-b": {RouteID(2)},
	}, 64)
	sinkA, epsA := bindSink(t, u, "reg-a", []RouteID{RouteID(1)})
	sinkB, epsB := bindSink(t, u, "reg-b", []RouteID{RouteID(2)})

	payloadA := []byte("hello from A")
	payloadB := []byte("hello from B")
	sendUDP(t, string(epsA[RouteID(1)]), payloadA)
	sendUDP(t, string(epsB[RouteID(2)]), payloadB)

	gotA := sinkA.WaitForRoute(RouteID(1), 1, testUDPTimeout)
	gotB := sinkB.WaitForRoute(RouteID(2), 1, testUDPTimeout)
	if len(gotA) != 1 || len(gotB) != 1 {
		t.Fatalf("delivery counts: A=%d B=%d; want 1 each", len(gotA), len(gotB))
	}
	if gotA[0].RouteID != RouteID(1) || gotB[0].RouteID != RouteID(2) {
		t.Fatalf("datagrams delivered to wrong routes: A->%d B->%d", gotA[0].RouteID, gotB[0].RouteID)
	}
	// Cross-delivery must be zero: sinkA has never seen B's payload and vice versa.
	if sinkA.Count() != 1 || sinkB.Count() != 1 {
		t.Fatalf("leak: sinkA=%d sinkB=%d; want 1 each", sinkA.Count(), sinkB.Count())
	}
}

// Proof: payload bytes are byte-identical end to end (one UDP datagram is
// preserved as one opaque packet, unmodified).
func TestUDP_ByteIdenticalPayload(t *testing.T) {
	t.Parallel()
	_, _, u := newUDPTestEnv(t, map[RegistrationID][]RouteID{"reg-a": {RouteID(1)}}, 64)
	sink, eps := bindSink(t, u, "reg-a", []RouteID{RouteID(1)})

	// Known bytes including zero bytes, high bytes, and no trailing newline.
	payload := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00, 0x7f, 0x80, 'W', 'G', 0x00, 0x42}
	sendUDP(t, string(eps[RouteID(1)]), payload)

	got := sink.WaitForRoute(RouteID(1), 1, testUDPTimeout)
	if len(got) != 1 {
		t.Fatalf("delivered %d; want 1", len(got))
	}
	if !reflect.DeepEqual(got[0].Payload, payload) {
		t.Fatalf("payload mutated: got %v want %v", got[0].Payload, payload)
	}
}

// Proof: one UDP datagram = one opaque packet — two separate datagrams are
// delivered as two separate packets, never coalesced.
func TestUDP_DatagramBoundaryPreservation(t *testing.T) {
	t.Parallel()
	_, _, u := newUDPTestEnv(t, map[RegistrationID][]RouteID{"reg-a": {RouteID(1)}}, 64)
	sink, eps := bindSink(t, u, "reg-a", []RouteID{RouteID(1)})

	first := []byte("packet-one")
	second := []byte("packet-two")
	sendUDP(t, string(eps[RouteID(1)]), first)
	sendUDP(t, string(eps[RouteID(1)]), second)

	got := sink.WaitForRoute(RouteID(1), 2, testUDPTimeout)
	if len(got) != 2 {
		t.Fatalf("delivered %d; want 2", len(got))
	}
	if string(got[0].Payload) != string(first) || string(got[1].Payload) != string(second) {
		t.Fatalf("boundaries lost: got %q then %q", got[0].Payload, got[1].Payload)
	}
}

// Proof: route A cannot leak into route B / another registration even when
// both routes are open concurrently and interleaved.
func TestUDP_StrictRouteIsolation(t *testing.T) {
	t.Parallel()
	_, _, u := newUDPTestEnv(t, map[RegistrationID][]RouteID{
		"reg-a": {RouteID(1)},
		"reg-b": {RouteID(2)},
	}, 64)
	sinkA, epsA := bindSink(t, u, "reg-a", []RouteID{RouteID(1)})
	sinkB, epsB := bindSink(t, u, "reg-b", []RouteID{RouteID(2)})
	_ = epsB

	for i := 0; i < 5; i++ {
		sendUDP(t, string(epsA[RouteID(1)]), []byte("A-data"))
		sendUDP(t, string(epsB[RouteID(2)]), []byte("B-data"))
	}

	gotA := sinkA.WaitForRoute(RouteID(1), 5, testUDPTimeout)
	gotB := sinkB.WaitForRoute(RouteID(2), 5, testUDPTimeout)
	if len(gotA) != 5 || len(gotB) != 5 {
		t.Fatalf("delivery: A=%d B=%d; want 5 each", len(gotA), len(gotB))
	}
	for _, d := range gotA {
		if string(d.Payload) != "A-data" {
			t.Fatalf("route A leaked datagram %q into reg B's sink", d.Payload)
		}
	}
	for _, d := range gotB {
		if string(d.Payload) != "B-data" {
			t.Fatalf("route B leaked datagram %q into reg A's sink", d.Payload)
		}
	}
	// No cross-registration delivery under any circumstances.
	if sinkA.Count() != 5 || sinkB.Count() != 5 {
		t.Fatalf("cross-leak: sinkA=%d sinkB=%d; want 5 each", sinkA.Count(), sinkB.Count())
	}
}

// Proof: closed routes do not forward. After Close, a datagram sent to the
// (released) endpoint is not delivered — and a datagram arriving on a
// handle whose route no longer exists is dropped with the unknown-route
// counter.
func TestUDP_ClosedRouteDropsAndCounts(t *testing.T) {
	t.Parallel()
	r, m, u := newUDPTestEnv(t, map[RegistrationID][]RouteID{"reg-a": {RouteID(1), RouteID(2)}}, 64)
	sink, eps := bindSink(t, u, "reg-a", []RouteID{RouteID(1), RouteID(2)})

	// Close route 1's endpoint.
	if err := u.Close(RouteID(1)); err != nil {
		t.Fatalf("Close(1): %v", err)
	}

	// A datagram sent to the released endpoint must not be delivered.
	sendUDP(t, string(eps[RouteID(1)]), []byte("to closed route"))
	time.Sleep(100 * time.Millisecond)
	if sink.Count() != 0 {
		t.Fatalf("closed route forwarded %d datagrams; want 0", sink.Count())
	}

	// The same route ID cannot be re-bound until the registry route is
	// re-opened; and dispatch on a handle whose registry route vanished is
	// dropped + counted. Simulate the stale-handle case directly:
	// remove the UDP endpoint, then remove route 2 from the registry behind
	// the listener's back, then dispatch — must drop + count.
	if err := u.Close(RouteID(2)); err != nil {
		t.Fatalf("Close Udp(2): %v", err)
	}
	if _, err := r.CloseRoute("reg-a", RouteID(2)); err != nil {
		t.Fatalf("CloseRoute(reg-a, 2): %v", err)
	}
	h := &routeHandle{routeID: RouteID(2), queue: make(chan Datagram, 64), done: make(chan struct{})}
	u.dispatch(h, []byte("stale"), SourceEndpoint{IP: "127.0.0.1", Port: 9999})
	if got := m.UDPDatagramsDroppedUnknownRoute(); got != 1 {
		t.Fatalf("dropped_unknown_route = %d; want 1", got)
	}

	// Restore route 2 in the registry and bound handle: re-dispatch with a
	// live handle must enqueue normally.
	if _, err := r.AllocateRoute("reg-a", RouteID(2)); err != nil {
		t.Fatalf("reallocate route 2: %v", err)
	}
	ep2, err := u.Bind(RouteID(2))
	if err != nil {
		t.Fatalf("re-Bind(2): %v", err)
	}
	sendUDP(t, string(ep2), []byte("after reopen"))
	got := sink.WaitForRoute(RouteID(2), 1, testUDPTimeout)
	if len(got) != 1 || string(got[0].Payload) != "after reopen" {
		t.Fatalf("route 2 did not deliver after reopen: %d datagrams", len(got))
	}
}

// Proof: oversized payloads are rejected explicitly with the drop counter,
// and nothing is forwarded. A zero-length datagram is likewise rejected.
func TestUDP_OversizedAndZeroLengthRejected(t *testing.T) {
	t.Parallel()
	_, m, u := newUDPTestEnv(t, map[RegistrationID][]RouteID{"reg-a": {RouteID(1)}}, 64)
	// Cap packet size below MaxFramePayloadSize so an oversized payload can
	// actually be sent over a real UDP socket (IPv4 payload max 65507).
	u.cfg.MaxPacketSize = 1024
	sink, eps := bindSink(t, u, "reg-a", []RouteID{RouteID(1)})

	oversized := make([]byte, 2048)
	for i := range oversized {
		oversized[i] = byte(i)
	}
	sendUDP(t, string(eps[RouteID(1)]), oversized)

	// Zero-length UDP datagram.
	conn, err := net.DialUDP("udp", nil, mustResolve(t, string(eps[RouteID(1)])))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	if _, err := conn.Write([]byte{}); err != nil {
		t.Fatalf("zero-length Write: %v", err)
	}
	conn.Close()

	waitForCounter(t, m.UDPDatagramsDroppedOversized, 1)
	waitForCounter(t, m.UDPDatagramsDroppedZeroLength, 1)
	if sink.Count() != 0 {
		t.Fatalf("sink received %d; want 0 for rejected datagrams", sink.Count())
	}
	// Accounting: 2 datagrams hit the socket, 0 forwarded, both dropped.
	if got := m.UDPDatagramsReceived(); got != 2 {
		t.Fatalf("received = %d; want 2", got)
	}
}

// Proof: per-route queues are bounded: with a slow sink, excess datagrams
// are dropped with the queue-full counter and never delivered.
func TestUDP_BoundedQueueBackpressure(t *testing.T) {
	t.Parallel()
	// Queue depth 4: deliverLoop is blocked, so only 4 datagrams may be
	// queued; the rest are dropped and counted.
	_, m, u := newUDPTestEnv(t, map[RegistrationID][]RouteID{"reg-a": {RouteID(1)}}, 4)

	held := make(chan struct{})
	release := make(chan struct{})
	sink := &blockingSink{held: held, release: release}
	u.SetSink("reg-a", sink)
	ep, err := u.Bind(RouteID(1))
	if err != nil {
		t.Fatalf("Bind(1): %v", err)
	}

	const total = 100
	for i := 0; i < total; i++ {
		sendUDP(t, string(ep), []byte("pressure"))
	}

	// Let the read loop run until the queue is visibly full.
	waitForCounter(t, m.UDPDatagramsDroppedQueueFull, 1)
	close(release)
	<-held // sink unblocked; begin drain

	// Wait for accounting to converge: every received datagram must be
	// counted as either forwarded or dropped queue-full.  This is a
	// moving target because the read loop discovers more datagrams from
	// the kernel buffer concurrently with drain progress, so we poll the
	// invariant directly rather than a static target.
	deadline := time.Now().Add(testUDPTimeout)
	for time.Now().Before(deadline) {
		r := m.UDPDatagramsReceived()
		f := m.UDPDatagramsForwarded()
		d := m.UDPDatagramsDroppedQueueFull()
		if f+d >= r {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	delivered := m.UDPDatagramsForwarded()
	droppedQueueFull := m.UDPDatagramsDroppedQueueFull()
	if delivered <= 0 {
		t.Fatalf("forwarded = %d; want > 0", delivered)
	}
	if droppedQueueFull == 0 {
		t.Fatalf("queue-full drops = 0; want > 0 for bounded queue under pressure")
	}
	// Accounting identity: every received datagram was either forwarded or
	// dropped for a counted reason (no other drop paths active here).
	received := m.UDPDatagramsReceived()
	if received != delivered+droppedQueueFull {
		t.Fatalf("accounting mismatch: received=%d forwarded=%d queueFull=%d",
			received, delivered, droppedQueueFull)
	}
}

// Proof: the source endpoint is runtime topology only — mutating it never
// changes routing/identity; the last-seen source is updated for return
// routing while delivery still resolves to the same owning registration.
func TestUDP_SourceEndpointIsTopologyOnly(t *testing.T) {
	t.Parallel()
	_, m, u := newUDPTestEnv(t, map[RegistrationID][]RouteID{
		"reg-a": {RouteID(1)},
		"reg-b": {RouteID(2)},
	}, 64)
	sinkA, eps := bindSink(t, u, "reg-a", []RouteID{RouteID(1)})
	_, _ = bindSink(t, u, "reg-b", []RouteID{RouteID(2)})

	// Two different source sockets talk to the same route A.
	sendUDP(t, string(eps[RouteID(1)]), []byte("from src1"))
	src2 := sendUDP(t, string(eps[RouteID(1)]), []byte("from src2"))

	got := sinkA.WaitForRoute(RouteID(1), 2, testUDPTimeout)
	if len(got) != 2 {
		t.Fatalf("delivered %d; want 2", len(got))
	}
	// Last-seen reflects the most recent source (runtime topology).
	last, ok := u.LastSeen(RouteID(1))
	if !ok {
		t.Fatal("LastSeen(1): not found")
	}
	wantLast := splitAddr(t, src2)
	if last != wantLast {
		t.Fatalf("LastSeen = %+v; want %+v", last, wantLast)
	}
	// The datagram from src1 was delivered to reg-a's sink despite the
	// second source having updated LastSeen — identity never changed.
	if string(got[0].Payload) != "from src1" || string(got[1].Payload) != "from src2" {
		t.Fatalf("payloads out of order or lost: %v", got)
	}
	if sinkA.Count() != 2 {
		t.Fatalf("sinkA = %d; want 2", sinkA.Count())
	}
	// Route → registration resolution stays exact regardless of source.
	if reg, ok := u.registry.RouteRegistration(RouteID(1)); !ok || reg != "reg-a" {
		t.Fatalf("route 1 resolved to reg %q ok=%v; want reg-a", reg, ok)
	}
	// No unknown-route drops occurred: identity was never source-derived.
	if got := m.UDPDatagramsDroppedUnknownRoute(); got != 0 {
		t.Fatalf("dropped_unknown_route = %d; want 0", got)
	}
}

// Proof: teardown releases UDP resources. Closing a route frees its port
// for immediate reuse; Shutdown closes every remaining listener and blocks
// until all goroutines exit.
func TestUDP_TeardownReleasesPorts(t *testing.T) {
	t.Parallel()
	// Use a fixed port range of one port so reuse is observable.
	reserve, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(testUDPListenAddr), Port: 0})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	port := reserve.LocalAddr().(*net.UDPAddr).Port
	reserve.Close()

	r := NewRegistry(100, 1000)
	m := NewMetrics()
	if err := r.AddRegistration("reg-a", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AllocateRoute("reg-a", RouteID(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AllocateRoute("reg-a", RouteID(2)); err != nil {
		t.Fatal(err)
	}
	u := NewUDPListener(UDPConfig{
		ListenAddress: testUDPListenAddr,
		PortMin:       port,
		PortMax:       port,
		MaxPacketSize: MaxFramePayloadSize,
		MaxQueueDepth: 16,
	}, r, m)

	// Route 1 takes the single port in the range.
	ep1, err := u.Bind(RouteID(1))
	if err != nil {
		t.Fatalf("Bind(1): %v", err)
	}
	if got := u.ActiveRoutes(); got != 1 {
		t.Fatalf("ActiveRoutes = %d; want 1", got)
	}

	// Route 2 cannot bind while route 1 holds the only port.
	if _, err := u.Bind(RouteID(2)); err == nil {
		t.Fatal("Bind(2) succeeded while route 1 held the only port; want failure")
	}

	// Close route 1: the port must be released immediately.
	if err := u.Close(RouteID(1)); err != nil {
		t.Fatalf("Close(1): %v", err)
	}
	ep2, err := u.Bind(RouteID(2))
	if err != nil {
		t.Fatalf("Bind(2) after close: %v", err)
	}
	if string(ep1) != string(ep2) {
		t.Fatalf("port not reused: ep1=%s ep2=%s", ep1, ep2)
	}

	// Shutdown closes every remaining listener (route 2) and waits.
	if err := u.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := u.ActiveRoutes(); got != 0 {
		t.Fatalf("ActiveRoutes after Shutdown = %d; want 0", got)
	}
	if _, err := u.Bind(RouteID(2)); err == nil {
		t.Fatal("Bind after Shutdown succeeded; want ErrUDPClosed")
	}
}

// Proof: service-level integration — OpenRouteEndpoint + teardown through
// the Service lifecycle.
func TestUDP_ServiceIntegration(t *testing.T) {
	t.Parallel()
	cfg := DefaultServiceConfig()
	cfg.UDP.ListenAddress = testUDPListenAddr
	svc, err := NewService(cfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(ctx) })

	if err := svc.Registry().AddRegistration("reg-a", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Registry().AllocateRoute("reg-a", RouteID(1)); err != nil {
		t.Fatal(err)
	}
	sink := NewTestSink()
	svc.SetPacketSink("reg-a", sink)

	ep, err := svc.OpenRouteEndpoint("reg-a", RouteID(1))
	if err != nil {
		t.Fatalf("OpenRouteEndpoint: %v", err)
	}
	sendUDP(t, string(ep), []byte("via service"))
	if got := sink.WaitForRoute(RouteID(1), 1, testUDPTimeout); len(got) != 1 {
		t.Fatalf("service path delivered %d; want 1", len(got))
	}

	// Close the route endpoint: subsequent datagrams must not forward.
	if err := svc.CloseRouteEndpoint("reg-a", RouteID(1)); err != nil {
		t.Fatalf("CloseRouteEndpoint: %v", err)
	}
	sendUDP(t, string(ep), []byte("after close"))
	time.Sleep(100 * time.Millisecond)
	if sink.Count() != 1 {
		t.Fatalf("delivered after close = %d; want 1 (no forwarding)", sink.Count())
	}
}

// --- auxiliary test types ------------------------------------------------

// blockingSink blocks every Deliver until release is closed. It satisfies
// PacketSink so queue pressure can be induced deterministically.
type blockingSink struct {
	held, release chan struct{}
	once          sync.Once
}

func (b *blockingSink) Deliver(RouteID, []byte, SourceEndpoint) {
	b.once.Do(func() { close(b.held) })
	<-b.release
}

// waitForCounter polls until the getter returns >= want.
func waitForCounter(t *testing.T, get func() int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(testUDPTimeout)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("counter did not reach %d (got %d)", want, get())
}

func mustResolve(t *testing.T, endpoint string) *net.UDPAddr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		t.Fatalf("ResolveUDPAddr(%s): %v", endpoint, err)
	}
	return addr
}

// splitAddr parses "host:port" into a SourceEndpoint.
func splitAddr(t *testing.T, addr string) SourceEndpoint {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%s): %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %s: %v", portStr, err)
	}
	return SourceEndpoint{IP: host, Port: port}
}
