//go:build e2e

// M4.7 E2E reconnect test on the REAL M4.6 topology:
//
//	Body → WG → Relay → RelayTransport → Core WG → Doll Network → Doll Link
//
// The Core ↔ Relay WS control tunnel is interrupted (ControlServer context
// cancelled — the relay SERVICE and its UDP sockets stay alive), a new
// ControlServer is started on the same port, and the ControlClient must
// reconnect, restore its route on the same UDP endpoint, and prove:
//
//   - WG path still works (overlay echo through the relay)
//   - Doll Link still works (WS event round-trip over the overlay)
//   - identity invariants unchanged (NetworkID, PeerIDs, WG keys,
//     overlay IPv6, membership, invitation state)
//   - no re-pairing (membership count still 1, invitation still consumed)
package integration

import (
	"context"
	"testing"
	"time"

	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

func TestM47_ReconnectE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	log := discardLogger()

	// Real M4.6 topology with fast disconnect detection + reconnect tuning.
	tp := setupM46Topology(t, ctx, log, "reconnect", withReconnectTuning())

	// Baseline proofs on the real stack.
	proveWgHandshake(t, ctx, tp)
	time.Sleep(500 * time.Millisecond)
	proveOverlayEcho(t, ctx, log, tp, "baseline")
	proveDollLink(t, ctx, log, tp, "baseline", "m47-reconnect-baseline")

	before := captureIdentityState(t, ctx, tp)
	m0 := tp.ctrlClient.Metrics()
	t.Logf("Before interruption: reconnects=%d routes_restored=%d connected=%v",
		m0.Reconnects, m0.RoutesRestored, tp.ctrlClient.IsConnected())

	// ── Interrupt Core ↔ Relay WS. Relay service stays alive. ──
	tp.csCancel()
	waitConnected(t, tp, false, 10*time.Second)

	// Restart the ControlServer on the same WS port over the SAME relay
	// service. The ControlClient's reconnectLoop must pick it up.
	tp.restartControlServer(t, ctx, log)
	waitConnected(t, tp, true, 20*time.Second)

	// Route 1 must be restored on the SAME UDP endpoint the Body is
	// already using — no endpoint migration, no new registration needed
	// on the Body side.
	waitRouteOpen(t, ctx, tp, tp.relaySvc, relay.RouteID(1), tp.routeEndpoint, 15*time.Second)

	m1 := tp.ctrlClient.Metrics()
	if m1.Reconnects <= m0.Reconnects {
		t.Fatalf("Reconnects did not increase: before=%d after=%d", m0.Reconnects, m1.Reconnects)
	}
	if m1.RoutesRestored < 1 {
		t.Fatalf("RoutesRestored = %d, want >= 1", m1.RoutesRestored)
	}
	t.Logf("After reconnect: reconnects=%d routes_restored=%d connected=%v",
		m1.Reconnects, m1.RoutesRestored, tp.ctrlClient.IsConnected())

	// ── Proofs after reconnect ──
	proveOverlayEcho(t, ctx, log, tp, "after reconnect")
	proveDollLink(t, ctx, log, tp, "after reconnect", "m47-reconnect-after")

	// ── Identity invariants must be UNCHANGED ──
	after := captureIdentityState(t, ctx, tp)
	assertIdentityUnchanged(t, "after reconnect", before, after)

	// No re-pairing: the membership snapshot above proves count==1 with
	// identical PeerID/WG key/overlay/endpoint/status, and the invitation
	// is still consumed — a Body cannot re-pair against it.
	if !after.invConsumed {
		t.Fatal("invitation is no longer consumed — a re-pairing window opened")
	}
	t.Log("No re-pairing: membership unchanged, invitation still consumed")

	t.Log("=== M4.7 Reconnect E2E: ALL PASS ===")
}

// TestM47_FailureE2E proves control-plane failure handling on the REAL M4.6
// topology (no standalone harness):
//
//   - WS interruption: ControlServer cancelled → client detects disconnect
//     → new ControlServer on the same port → client reconnects, restores
//     its route, overlay path works again
//   - closed route: CloseRoute removes the route from the registry AND
//     releases its UDP endpoint
//   - empty route token: OpenRoute with an empty token is rejected
func TestM47_FailureE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	log := discardLogger()

	// Real M4.6 topology with fast disconnect detection + reconnect tuning.
	tp := setupM46Topology(t, ctx, log, "failure", withReconnectTuning())

	proveWgHandshake(t, ctx, tp)
	time.Sleep(500 * time.Millisecond)
	proveOverlayEcho(t, ctx, log, tp, "baseline")

	// ── 1. WS interruption → reconnect on the real topology ──
	m0 := tp.ctrlClient.Metrics()
	tp.csCancel()
	waitConnected(t, tp, false, 10*time.Second)
	tp.restartControlServer(t, ctx, log)
	waitConnected(t, tp, true, 20*time.Second)
	waitRouteOpen(t, ctx, tp, tp.relaySvc, relay.RouteID(1), tp.routeEndpoint, 15*time.Second)
	m1 := tp.ctrlClient.Metrics()
	if m1.Reconnects <= m0.Reconnects {
		t.Fatalf("Reconnects did not increase after WS interruption: before=%d after=%d", m0.Reconnects, m1.Reconnects)
	}
	proveOverlayEcho(t, ctx, log, tp, "after WS interruption reconnect")
	t.Log("WS interruption → reconnect → route restored → overlay path recovered: PASS")

	// ── 2. Closed route: registry entry gone + UDP endpoint released ──
	routesBefore := tp.relaySvc.Registry().RouteCount()
	opened3, err := tp.ctrlClient.OpenRoute(ctx, relay.RouteID(3), relay.RouteCredentials{Token: "test-route-credential-3"})
	if err != nil {
		t.Fatalf("OpenRoute(3): %v", err)
	}
	if opened3.AllocatedEndpoint == "" {
		t.Fatal("OpenRoute(3) allocated no endpoint")
	}
	if got := tp.relaySvc.Registry().RouteCount(); got != routesBefore+1 {
		t.Fatalf("RouteCount after OpenRoute(3) = %d, want %d", got, routesBefore+1)
	}
	if err := tp.ctrlClient.CloseRoute(ctx, relay.RouteID(3)); err != nil {
		t.Fatalf("CloseRoute(3): %v", err)
	}
	if r, ok := tp.relaySvc.Registry().Route(relay.RouteID(3)); ok && r.State == relay.RouteStateOpen {
		t.Fatalf("route 3 still open after CloseRoute: %+v", r)
	}
	if _, ok := tp.relaySvc.UDP().LastSeen(relay.RouteID(3)); ok {
		t.Fatal("route 3's UDP endpoint still present after CloseRoute — endpoint not released")
	}
	if got := tp.relaySvc.Registry().RouteCount(); got != routesBefore {
		t.Fatalf("RouteCount after CloseRoute(3) = %d, want %d", got, routesBefore)
	}
	t.Logf("Closed route 3 (endpoint %s): registry entry removed, UDP endpoint released, route count back to %d", opened3.AllocatedEndpoint, routesBefore)

	// ── 3. Empty route token → rejected ──
	if _, err := tp.ctrlClient.OpenRoute(ctx, relay.RouteID(4), relay.RouteCredentials{Token: ""}); err == nil {
		t.Fatal("OpenRoute with empty token did not fail")
	} else {
		t.Logf("OpenRoute with empty token rejected: %v", err)
	}

	t.Log("=== M4.7 Failure E2E: ALL PASS ===")
}
