//go:build e2e

// Core 4 / M6.4 — Better-Path Discovery via ProbeListener.
//
// The relay exposes a ReachProbe endpoint on its control server. The Body
// sends a probe packet; the relay checks credentials and returns an
// authenticated path quality measurement.
//
// Keeps the active tunnel working throughout probing. Proves candidate
// viability with real WireGuard (handshake verification, not just socket
// reachability).

package integration

import (
	"context"
	"testing"
	"time"

	body "github.com/Neon-Dolls/neondoll/Body"
	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

func TestM64_ProbeReachability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	t.Parallel()

	log := discardLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Setup topology (M4.6 + route 1 with credentials).
	tp := setupM46Topology(t, ctx, log, "m64-probe", func(cfg *relay.ClientConfig) {})
	defer t.Cleanup(tp.cleanup)

	// Start a ProbeListener against the relay's registry.
	pl := relay.NewProbeListener(relay.ProbeListenerConfig{}, tp.relaySvc.Registry(), log)
	if err := pl.Start(); err != nil {
		t.Fatalf("ProbeListener.Start: %v", err)
	}
	defer pl.Stop()
	probeAddr := pl.Addr().String()

	// Wait for Body's WireGuard handshake through the relay before
	// the overlay proofs below need an established tunnel.
	proveWgHandshake(t, ctx, tp)

	// Verify: probe to route 1 with correct credentials succeeds.
	t.Logf("probing %s for route 1 with correct credentials", probeAddr)
	r := body.ReachProbe(ctx, body.ReachProbeConfig{
		Target:     probeAddr,
		RouteID:    1,
		Credential: tp.routeCredential,
		Timeout:    3 * time.Second,
	})
	if !r.Reachable {
		t.Fatalf("route 1 probe: expected reachable, got error: %v (latency: %v)", r.Error, r.Duration)
	}
	t.Logf("route 1 reachable in %v", r.Duration)

	// Verify: probe to dead port times out.
	const deadAddr = "127.0.0.1:19599"
	r2 := body.ReachProbe(ctx, body.ReachProbeConfig{
		Target:     deadAddr,
		RouteID:    1,
		Credential: "test-route-credential",
		Timeout:    100 * time.Millisecond,
	})
	if r2.Reachable {
		t.Fatal("dead port probe: expected NOT reachable")
	}
	t.Logf("dead port correctly unreachable: %v (latency: %v)", r2.Error, r2.Duration)

	// Verify: probe with wrong credentials fails.
	r3 := body.ReachProbe(ctx, body.ReachProbeConfig{
		Target:     probeAddr,
		RouteID:    1,
		Credential: "wrong-credential",
		Timeout:    1 * time.Second,
	})
	if r3.Reachable {
		t.Fatal("wrong credential probe: expected NOT reachable")
	}
	t.Logf("wrong credential correctly rejected: %v (latency: %v)", r3.Error, r3.Duration)

	// Verify: the active tunnel is undisturbed.
	proveOverlayEcho(t, ctx, log, tp, "post-probe")

	// Verify: probe to unknown route is transparent to the overlay.
	r4 := body.ReachProbe(ctx, body.ReachProbeConfig{
		Target:     probeAddr,
		RouteID:    99,
		Credential: "doesnt-matter-no-route",
		Timeout:    300 * time.Millisecond,
	})
	if r4.Reachable {
		t.Fatal("unknown route probe: expected NOT reachable")
	}
	t.Logf("unknown route correctly rejected: %v (latency: %v)", r4.Error, r4.Duration)

	proveOverlayEcho(t, ctx, log, tp, "post-unknown-route")
}