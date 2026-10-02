//go:build e2e

// M4.7 E2E reconstruction test on the REAL M4.6 topology:
//
//	Body → WG → Relay → RelayTransport → Core WG → Doll Network → Doll Link
//
// The ENTIRE relay is shut down (relay.Service + ControlServer), then a
// fresh relay.Service + ControlServer are started on the same ports. The
// ControlClient must reconnect, route 1 must come back on the SAME UDP
// endpoint, and the Body — which never re-paired and never changed its
// configuration — must prove:
//
//   - WG path works again (overlay echo through the new relay)
//   - Doll Link works again (WS event round-trip over the overlay)
//   - identity invariants unchanged (NetworkID, PeerIDs, WG keys,
//     overlay IPv6, membership, invitation state)
//   - no re-pairing
package integration

import (
	"context"
	"testing"
	"time"

	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

func TestM47_ReconstructionE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	log := discardLogger()

	// Real M4.6 topology with fast disconnect detection + reconnect tuning.
	tp := setupM46Topology(t, ctx, log, "reconstruction", withReconnectTuning())

	// Baseline proofs on the real stack.
	proveWgHandshake(t, ctx, tp)
	time.Sleep(500 * time.Millisecond)
	proveOverlayEcho(t, ctx, log, tp, "baseline")
	proveDollLink(t, ctx, log, tp, "baseline", "m47-recon-baseline")

	before := captureIdentityState(t, ctx, tp)
	m0 := tp.ctrlClient.Metrics()
	t.Logf("Before shutdown: reconnects=%d routes_restored=%d connected=%v",
		m0.Reconnects, m0.RoutesRestored, tp.ctrlClient.IsConnected())

	// ── Full shutdown: relay service + control server. ──
	// Explicit Shutdown closes the relay's UDP listener, releasing
	// relayPortSvc so the replacement relay can bind the same port — the
	// Body's WG endpoint never changes.
	if err := tp.relaySvc.Shutdown(ctx); err != nil {
		t.Fatalf("relaySvc.Shutdown: %v", err)
	}
	tp.csCancel()
	waitConnected(t, tp, false, 10*time.Second)
	t.Log("Relay service + ControlServer shut down")

	// ── Reconstruction: new relay.Service + ControlServer, same ports. ──
	tp.startRelayService(t, ctx, log)
	tp.restartControlServer(t, ctx, log)

	waitConnected(t, tp, true, 20*time.Second)
	// Route 1 must return on the SAME endpoint the Body already uses.
	waitRouteOpen(t, ctx, tp, tp.relaySvc, relay.RouteID(1), tp.routeEndpoint, 15*time.Second)

	m1 := tp.ctrlClient.Metrics()
	if m1.Reconnects <= m0.Reconnects {
		t.Fatalf("Reconnects did not increase: before=%d after=%d", m0.Reconnects, m1.Reconnects)
	}
	if m1.RoutesRestored < 1 {
		t.Fatalf("RoutesRestored = %d, want >= 1", m1.RoutesRestored)
	}
	t.Logf("After reconstruction: reconnects=%d routes_restored=%d connected=%v",
		m1.Reconnects, m1.RoutesRestored, tp.ctrlClient.IsConnected())

	// ── Proofs after reconstruction ──
	proveOverlayEcho(t, ctx, log, tp, "after reconstruction")
	proveDollLink(t, ctx, log, tp, "after reconstruction", "m47-recon-after")

	// ── Identity invariants must be UNCHANGED ──
	after := captureIdentityState(t, ctx, tp)
	assertIdentityUnchanged(t, "after reconstruction", before, after)

	// No re-pairing: membership identical, invitation still consumed.
	if !after.invConsumed {
		t.Fatal("invitation is no longer consumed — a re-pairing window opened")
	}
	t.Log("No re-pairing: membership unchanged, invitation still consumed")

	t.Log("=== M4.7 Reconstruction E2E: ALL PASS ===")
}
