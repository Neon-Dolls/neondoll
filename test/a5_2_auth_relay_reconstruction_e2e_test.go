//go:build e2e

// A5.2 E2E test — Authenticated Relay Reconstruction Proof.
//
// Proves the authenticated Relay topology survives a deliberate
// Core ↔ Relay control WebSocket disconnect:
//
//  1. Baseline authenticated topology: WG handshake, overlay echo,
//     Doll Link round-trip.
//  2. Capture the current runtime Route RegistrationID.
//  3. Deliberately close the Core ↔ Relay control WebSocket
//     (cancel ControlServer — the relay Service stays alive).
//  4. The reconnectLoop detects the disconnect, reconnects,
//     re-authenticates with a new RegistrationID, and restores routes.
//  5. Prove a new registration was created (RegistrationCount 0 → 1)
//     with a different RegistrationID than before the disconnect.
//  6. Prove the route was reconstructed on the same UDP endpoint.
//  7. Prove Doll Link round-trip through the rebuilt path.
//  8. Prove identity invariants unchanged — no re-pairing.
package integration

import (
	"context"
	"testing"
	"time"

	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

func TestA52_AuthenticatedRelayReconstruction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	log := discardLogger()

	// Real M4.6 topology with fast disconnect detection + reconnect tuning.
	tp := setupM46Topology(t, ctx, log, "a52", withReconnectTuning())

	// ── 1. Baseline: prove the authenticated stack works ──
	proveWgHandshake(t, ctx, tp)
	time.Sleep(500 * time.Millisecond)
	proveOverlayEcho(t, ctx, log, tp, "baseline")
	proveDollLink(t, ctx, log, tp, "baseline", "a52-ping-baseline")

	// ── 2. Capture identity + route RegistrationID before disconnect ──
	before := captureIdentityState(t, ctx, tp)

	rBefore, ok := tp.relaySvc.Registry().Route(relay.RouteID(1))
	if !ok {
		t.Fatal("Route 1 not in registry before disconnect")
	}
	oldRegID := rBefore.RegistrationID
	oldEndpoint := rBefore.Endpoint
	if rBefore.State != relay.RouteStateOpen {
		t.Fatalf("Route 1 state before disconnect = %v, want RouteStateOpen", rBefore.State)
	}
	t.Logf("Before disconnect: route 1 RegistrationID=%s endpoint=%s state=%v",
		oldRegID, oldEndpoint, rBefore.State)

	m0 := tp.ctrlClient.Metrics()
	t.Logf("Before disconnect: reconnects=%d routes_restored=%d connected=%v",
		m0.Reconnects, m0.RoutesRestored, tp.ctrlClient.IsConnected())

	// ── 3. Deliberately close Core ↔ Relay control WebSocket ──
	// Cancelling the ControlServer closes the TCP listener, which closes
	// the WebSocket connections. The server-side defer block removes the
	// registration, releases routes, and closes UDP endpoints. The relay
	// Service (UDP listener + route registry) stays alive.
	tp.csCancel()
	waitConnected(t, tp, false, 10*time.Second)

	// Prove de-registration: wait for the server-side cleanup to remove
	// the registration (the client detects disconnect faster than the
	// server goroutine processes the WS error and runs its defer block).
	waitCondition(t, "RegistrationCount == 0 after disconnect", 5*time.Second, func() bool {
		return tp.relaySvc.Registry().RegistrationCount() == 0
	})
	t.Log("Core de-registered: RegistrationCount = 0")

	// Prove the route was removed from the registry during cleanup.
	if _, exists := tp.relaySvc.Registry().Route(relay.RouteID(1)); exists {
		t.Fatal("Route 1 still in registry after disconnect — cleanup did not remove it")
	}
	t.Log("Route 1 removed from registry after disconnect")

	// ── 4. Restart ControlServer on the same port ──
	tp.restartControlServer(t, ctx, log)

	// ── 5. Prove re-authentication ──
	// The ControlClient's reconnectLoop dials the new ControlServer,
	// sends Register{Token: "test-reg-token"}, and the ControlServer
	// verifies the token against the configured SHA-256 credential hash.
	// A new RegistrationID is generated for this connection.
	waitConnected(t, tp, true, 20*time.Second)

	regCount2 := tp.relaySvc.Registry().RegistrationCount()
	if regCount2 != 1 {
		t.Fatalf("RegistrationCount after reconnect = %d, want 1 (new registration)", regCount2)
	}
	t.Logf("Re-authentication: RegistrationCount = %d — Core authenticated again", regCount2)

	// ── 6. Prove route reconstructed with new RegistrationID ──
	// Route 1 must be restored on the SAME UDP endpoint the Body already
	// uses — no endpoint migration.
	waitRouteOpen(t, ctx, tp, tp.relaySvc, relay.RouteID(1), oldEndpoint, 15*time.Second)

	rAfter, ok := tp.relaySvc.Registry().Route(relay.RouteID(1))
	if !ok {
		t.Fatal("Route 1 not in registry after reconnect — reconstruction failed")
	}
	newRegID := rAfter.RegistrationID
	t.Logf("After reconnect: route 1 RegistrationID=%s endpoint=%s state=%v",
		newRegID, rAfter.Endpoint, rAfter.State)

	// Prove RegistrationID changed — confirms full re-auth round-trip.
	if newRegID == oldRegID {
		t.Fatalf("RegistrationID unchanged after reconnect: %s — re-authentication did not occur",
			oldRegID)
	}
	t.Logf("RegistrationID changed: %s → %s (re-authentication proven)", oldRegID, newRegID)

	// Prove route is open.
	if rAfter.State != relay.RouteStateOpen {
		t.Fatalf("Route 1 state after reconnect = %v, want RouteStateOpen", rAfter.State)
	}

	// Prove metrics show the reconnect and route restoration.
	m1 := tp.ctrlClient.Metrics()
	if m1.Reconnects <= m0.Reconnects {
		t.Fatalf("Reconnects did not increase: before=%d after=%d", m0.Reconnects, m1.Reconnects)
	}
	if m1.RoutesRestored < 1 {
		t.Fatalf("RoutesRestored = %d, want >= 1", m1.RoutesRestored)
	}
	t.Logf("After reconstruction: reconnects=%d routes_restored=%d connected=%v",
		m1.Reconnects, m1.RoutesRestored, tp.ctrlClient.IsConnected())

	// ── 7. Prove Doll Link round-trip through reconstructed path ──
	proveDollLink(t, ctx, log, tp, "after reconstruction", "a52-ping-reconstructed")

	// ── 8. Verify identity invariants unchanged ──
	// No re-pairing: membership, PeerIDs, WG keys, overlay addresses,
	// and invitation consumption must be identical to before the disconnect.
	after := captureIdentityState(t, ctx, tp)
	assertIdentityUnchanged(t, "after authenticated reconstruction", before, after)

	if !after.invConsumed {
		t.Fatal("invitation is no longer consumed — re-pairing window opened")
	}
	t.Log("No re-pairing: membership unchanged, invitation still consumed")

	// ── Final metrics — no unexpected frame handler errors ──
	m2 := tp.ctrlClient.Metrics()
	if m2.FrameHandlerErrors != 0 {
		t.Errorf("FrameHandlerErrors = %d after reconstruction E2E, want 0", m2.FrameHandlerErrors)
	}

	t.Log("=== A5.2 Authenticated Relay Reconstruction: ALL PASS ===")
}
