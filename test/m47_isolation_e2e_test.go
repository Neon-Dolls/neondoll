//go:build e2e

// M4.7 E2E route isolation test on the REAL M4.6 topology:
//
//	Body → WG → Relay → RelayTransport → Core WG → Doll Network → Doll Link
//
// A SECOND Core registration (second ControlClient + RelayTransport +
// route 2) is created on the same relay. Route isolation is proven with
// ACTUAL UDP packets, not merely distinct endpoints:
//
//   - inbound separation: a UDP packet sent to route B's endpoint arrives
//     at Core B tagged route 2, byte-exact, while route A's registration
//     metrics show the A-side path is independent
//   - outbound separation: a packet Core B sends through route 2 leaves
//     route B's socket and arrives at route B's peer; route A's probe
//     socket receives nothing
//   - cross-delivery negative: a UDP packet sent to route A's endpoint
//     is delivered to Core A's registration (forwarded metric +1) and
//     never appears at Core B
//   - close isolation: closing route A releases route A's endpoint and
//     registry entry while route B keeps its endpoint and keeps carrying
//     packets both ways
package integration

import (
	"context"
	"net"
	"testing"
	"time"

	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

func TestM47_IsolationE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	log := discardLogger()

	// Real M4.6 topology (route 1 = Body ↔ Core A path). No reconnect
	// tuning: this test interrupts nothing, so the control tunnel must
	// not churn.
	tp := setupM46Topology(t, ctx, log, "isolation")

	// Baseline: route 1 carries the real WG path.
	proveWgHandshake(t, ctx, tp)
	time.Sleep(500 * time.Millisecond)
	proveOverlayEcho(t, ctx, log, tp, "baseline")

	// ── Second Core registration on the same relay ──
	ctrlB := relay.NewControlClient(relay.ClientConfig{
		RelayURL:          tp.relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "test-reg-token",
	})
	if err := ctrlB.Start(ctx); err != nil {
		t.Fatalf("ControlClient B Start: %v", err)
	}
	defer ctrlB.Shutdown()

	openedB, err := ctrlB.OpenRoute(ctx, relay.RouteID(2), relay.RouteCredentials{Token: "test-route-credential-b"})
	if err != nil {
		t.Fatalf("OpenRoute B: %v", err)
	}
	epA := tp.routeEndpoint
	epB := openedB.AllocatedEndpoint
	if epA == epB {
		t.Fatalf("routes share one UDP endpoint %q — isolation impossible", epA)
	}
	t.Logf("Route 1 (Core A / Body): endpoint=%s | Route 2 (Core B): endpoint=%s", epA, epB)

	// Two registrations on the relay, each with its own UDP endpoint.
	if got := tp.cs.ActiveRegistrations(); got != 2 {
		t.Fatalf("ActiveRegistrations = %d, want 2", got)
	}
	r1, ok1 := tp.relaySvc.Registry().Route(relay.RouteID(1))
	r2, ok2 := tp.relaySvc.Registry().Route(relay.RouteID(2))
	if !ok1 || !ok2 {
		t.Fatalf("registry missing routes: r1=%v r2=%v", ok1, ok2)
	}
	if r1.Endpoint != epA || r2.Endpoint != epB {
		t.Fatalf("registry endpoints = %s / %s, want %s / %s", r1.Endpoint, r2.Endpoint, epA, epB)
	}
	if r1.State != relay.RouteStateOpen || r2.State != relay.RouteStateOpen {
		t.Fatalf("registry route states = %v / %v, want open/open", r1.State, r2.State)
	}

	// Core B's real RelayTransport (production intake path for inbound
	// relayed packets).
	rtB := relay.NewRelayTransport(ctrlB)
	recvFns, _, err := rtB.Open(0)
	if err != nil {
		t.Fatalf("RelayTransport B Open: %v", err)
	}
	if recvFns == nil || len(recvFns) == 0 {
		t.Fatal("RelayTransport B Open returned no receive funcs")
	}
	defer rtB.Close()

	// Observe Core B's inbound frames at the ControlClient binary-frame
	// dispatch — the exact boundary RelayTransport.Open consumes (the
	// transport registers the same kind of handler). Frames carry the
	// route ID the relay tagged them with.
	type inboundFrame struct {
		routeID relay.RouteID
		payload string
	}
	observed := make(chan inboundFrame, 64)
	ctrlB.SetFrameHandler(func(f *relay.Frame) error {
		select {
		case observed <- inboundFrame{routeID: f.RouteID, payload: string(f.Payload)}:
		default:
		}
		return nil
	})

	expectObserved := func(wantRoute relay.RouteID, wantPayload string, within time.Duration) inboundFrame {
		t.Helper()
		select {
		case f := <-observed:
			if f.routeID != wantRoute || f.payload != wantPayload {
				t.Fatalf("inbound frame = (route %d, %q), want (route %d, %q)", f.routeID, f.payload, wantRoute, wantPayload)
			}
			return f
		case <-time.After(within):
			t.Fatalf("no UDP packet delivered to Core B within %s (want route %d, %q)", within, wantRoute, wantPayload)
			return inboundFrame{}
		}
	}
	expectNothingObserved := func(d time.Duration, context string) {
		t.Helper()
		select {
		case f := <-observed:
			t.Fatalf("%s: Core B unexpectedly received (route %d, %q)", context, f.routeID, f.payload)
		case <-time.After(d):
		}
	}

	// Probes: plain UDP sockets standing in for remote peers.
	probeA, err := net.Dial("udp", epA)
	if err != nil {
		t.Fatalf("dial route A endpoint: %v", err)
	}
	defer probeA.Close()
	probeB, err := net.Dial("udp", epB)
	if err != nil {
		t.Fatalf("dial route B endpoint: %v", err)
	}
	defer probeB.Close()

	// ── Phase 1: inbound separation — a real UDP packet sent to route B's
	// endpoint arrives at Core B, tagged route 2, byte-exact. ──
	if _, err := probeB.Write([]byte("pkt-b1")); err != nil {
		t.Fatalf("probeB write: %v", err)
	}
	expectObserved(relay.RouteID(2), "pkt-b1", 3*time.Second)
	t.Logf("INBOUND: UDP packet to %s arrived at Core B tagged route 2 (byte-exact)", epB)

	// ── Phase 2: outbound separation — a packet Core B sends through route
	// 2 leaves route B's socket and arrives at route B's peer, sourced from
	// route B's endpoint. Route A's probe socket receives nothing. ──
	ep2, err := relay.ParseRelayEndpoint(relay.RelayEndpointString(relay.RouteID(2)))
	if err != nil {
		t.Fatalf("ParseRelayEndpoint: %v", err)
	}
	if err := rtB.Send([][]byte{[]byte("core-b-1")}, ep2); err != nil {
		t.Fatalf("RelayTransport B Send: %v", err)
	}
	probeA.SetReadDeadline(time.Now().Add(2 * time.Second))
	bufA := make([]byte, 2048)
	if n, err := probeA.Read(bufA); err == nil {
		t.Fatalf("route A probe received %d bytes from route B's outbound traffic: %q", n, string(bufA[:n]))
	}
	probeB.SetReadDeadline(time.Now().Add(2 * time.Second))
	bufB := make([]byte, 2048)
	n, err := probeB.Read(bufB)
	if err != nil {
		t.Fatalf("route B probe did not receive Core B's outbound packet: %v", err)
	}
	if got := string(bufB[:n]); got != "core-b-1" {
		t.Fatalf("route B probe received %q, want %q", got, "core-b-1")
	}
	if src := probeB.RemoteAddr().String(); src != epB {
		t.Fatalf("route B packet source = %s, want route B's endpoint %s", src, epB)
	}
	expectNothingObserved(300*time.Millisecond, "route B outbound")
	t.Logf("OUTBOUND: Core B packet left route B's socket (%s) and arrived at route B's peer; route A saw nothing", epB)

	// ── Phase 3: cross-delivery negative — a UDP packet sent to route A's
	// endpoint is delivered to Core A's registration (the only sink on
	// route 1) and NEVER appears at Core B. ──
	forwardedBefore := tp.relaySvc.Metrics().UDPDatagramsForwarded()
	unknownBefore := tp.relaySvc.Metrics().UDPDatagramsDroppedUnknownRoute()
	if _, err := probeA.Write([]byte("pkt-a1")); err != nil {
		t.Fatalf("probeA write: %v", err)
	}
	// Route A's sink is Core A's WS registration — delivery is counted on
	// the relay metrics as forwarded.
	waitCondition(t, "route A packet forwarded to Core A's registration", 3*time.Second, func() bool {
		return tp.relaySvc.Metrics().UDPDatagramsForwarded() >= forwardedBefore+1
	})
	forwardedAfter := tp.relaySvc.Metrics().UDPDatagramsForwarded()
	unknownAfter := tp.relaySvc.Metrics().UDPDatagramsDroppedUnknownRoute()
	if unknownAfter != unknownBefore {
		t.Fatalf("packet to route A was dropped as unknown route (before=%d after=%d)", unknownBefore, unknownAfter)
	}
	expectNothingObserved(1500*time.Millisecond, "route A inbound cross-delivery")
	probeB.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := probeB.Read(bufB); err == nil {
		t.Fatalf("route B probe saw route A's packet: %q", string(bufB[:n]))
	}
	t.Logf("CROSS-DELIVERY: packet to %s forwarded to Core A (forwarded %d→%d, unknown-route drops unchanged) and absent at Core B", epA, forwardedBefore, forwardedAfter)

	// The Body A ↔ Core A path is independent of everything above: the
	// overlay echo still traverses route 1 end-to-end after route B
	// traffic and route A probing.
	proveOverlayEcho(t, ctx, log, tp, "after route B traffic + probing")

	// ── Phase 4: close isolation — closing route A releases route A's
	// endpoint and registry entry; route B keeps its endpoint and keeps
	// carrying packets both directions. ──
	if err := tp.ctrlClient.CloseRoute(ctx, relay.RouteID(1)); err != nil {
		t.Fatalf("CloseRoute(1): %v", err)
	}
	if r, ok := tp.relaySvc.Registry().Route(relay.RouteID(1)); ok && r.State == relay.RouteStateOpen {
		t.Fatalf("route 1 still open in registry after CloseRoute: %+v", r)
	}
	if _, ok := tp.relaySvc.UDP().LastSeen(relay.RouteID(1)); ok {
		t.Fatal("route 1's UDP endpoint still present after CloseRoute — endpoint not released")
	}
	r2after, ok := tp.relaySvc.Registry().Route(relay.RouteID(2))
	if !ok {
		t.Fatal("route 2 missing from registry after closing route 1 — close was not isolated")
	}
	if r2after.Endpoint != epB || r2after.State != relay.RouteStateOpen {
		t.Fatalf("route 2 after closing route 1: state=%v endpoint=%s (want open with %s)", r2after.State, r2after.Endpoint, epB)
	}
	t.Logf("CLOSE ISOLATION: route 1 closed (registry entry gone, UDP endpoint released); route 2 unchanged at %s", epB)

	// Route B still carries packets both directions after route A is gone.
	if _, err := probeB.Write([]byte("pkt-b3")); err != nil {
		t.Fatalf("probeB write after close: %v", err)
	}
	expectObserved(relay.RouteID(2), "pkt-b3", 3*time.Second)
	if err := rtB.Send([][]byte{[]byte("core-b-3")}, ep2); err != nil {
		t.Fatalf("RelayTransport B Send after close: %v", err)
	}
	probeB.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = probeB.Read(bufB)
	if err != nil {
		t.Fatalf("route B probe did not receive after route A close: %v", err)
	}
	if got := string(bufB[:n]); got != "core-b-3" {
		t.Fatalf("route B probe received %q after close, want %q", got, "core-b-3")
	}
	expectNothingObserved(300*time.Millisecond, "route B after route A close")
	t.Logf("ROUTE B UNAFFECTED after closing route A: inbound pkt-b3 delivered (route 2), outbound core-b-3 arrived at %s", epB)

	t.Log("=== M4.7 Isolation E2E: ALL PASS ===")
}
