//go:build e2e

package integration

// M4.6 E2E test: proves the full real path on actual local networking —
// Body → WireGuard → Relay → RelayTransport → Core WireGuard → Doll Network
// → Doll Link.
//
// The topology construction now lives in setupM46Topology (test/m47_helpers.go)
// so the M4.7 reconnect / reconstruction / isolation / failure E2E tests
// operate on this exact same real stack instead of a parallel harness.
// This test keeps the original M4.6 verification sequence: handshake,
// overlay TCP echo through the relayed tunnel, and the Doll Link
// WebSocket event proof.

import (
	"context"
	"testing"
	"time"
)

func TestM46E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	log := discardLogger()

	// Build the full real topology (M1 identity, M2 pairing, M4.6
	// relay-aware Core + Body + Doll Link echo server on the overlay).
	// setupM46Topology asserts: network identity derived, pairing works,
	// Core has NO direct WG port (RelayTransport.Open(0) → port 0), and
	// route 1 opened with credentials on the relay.
	tp := setupM46Topology(t, ctx, log, "m46")

	// Body's WireGuard handshake goes through the relay, not direct UDP.
	proveWgHandshake(t, ctx, tp)

	// Give relayed handshake time to settle before starting data traffic.
	time.Sleep(500 * time.Millisecond)

	// Prove actual relayed data path: Body overlay TCP echo to Core overlay.
	proveOverlayEcho(t, ctx, log, tp, "m46")

	// Prove Doll Link over the overlay: WebSocket upgrade + event
	// round-trip through the relayed WG tunnel, with correlation proof.
	proveDollLink(t, ctx, log, tp, "m46", "m46-ping-001")

	t.Log("=== M4.6 E2E: ALL PASS ===")
}
