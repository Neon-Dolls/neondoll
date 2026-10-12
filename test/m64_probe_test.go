// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

// Core 4 / M6.4 — Better-Path Discovery
//
// Integration tests verifying:
//   - ProbeBetter finds a direct candidate when active is relay-UDP
//   - Failed probes leave the active path untouched
//   - Cancellation during probe cleans up resources

package integration

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
)

// TestProbeBetter_RelayUDP_to_Direct verifies:
//   - active tunnel on relay-udp path
//   - ProbeBetter discovers the direct path as "better"
//   - returned ProbeResult has the proven tunnel (real WG handshake)
//   - active tunnel is NOT disturbed by probing
func TestProbeBetter_RelayUDP_to_Direct(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	bodyOverlay := netip.MustParseAddr("fd00::2")
	coreOverlay := netip.MustParseAddr("fd00::1")

	logger := testLogger(t)

	// ── Core side: two devices sharing the same keypair ──
	// Core device A represents the "relay-udp" endpoint.
	coreDevA, coreNetA, corePortA := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	// Core device B represents the "direct" endpoint (better path).
	coreDevB, coreNetB, corePortB := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	_ = coreDevB // used later for waitHandshake on direct path

	// ── Body side: select initial path via relay-udp (Core A) ──
	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     "127.0.0.1:1", // dead so relay-udp is selected
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWSSURL:        "",
		PerAttemptTimeout:  5 * time.Second,
	}

	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath (relay-udp): %v", initialResult.Err)
	}
	if initialResult.Tunnel == nil {
		t.Fatal("nil Tunnel on success")
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	// Wait for handshake on relay-udp path
	waitHandshake(t, coreDevA, "Core A (relay-udp)", 10*time.Second)
	if err := initialResult.Tunnel.WaitHandshake(selCtx, 10*time.Second); err != nil {
		t.Fatalf("relay-udp handshake: %v", err)
	}

	// Prove traffic through relay-udp path
	proveBidirectionalTraffic(t,
		initialResult.Tunnel.Netstack(), coreNetA,
		bodyOverlay, coreOverlay,
		"relay-udp-pre-probe",
	)

	// ── ProbeBetter: find the direct path candidate ──
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer probeCancel()

	// Probe config: direct endpoint points to the real Core B port.
	probeCfg := cfg
	probeCfg.DirectEndpoint = fmt.Sprintf("127.0.0.1:%d", corePortB)
	probeCfg.PerAttemptTimeout = 5 * time.Second

	probeResult := body.ProbeBetter(probeCtx, probeCfg, body.PathRelayUDP, body.ProbeConfig{
		PerAttemptTimeout: 5 * time.Second,
		MinBackoff:        1 * time.Second,
		MaxBackoff:        3 * time.Second,
		BackoffFactor:     2.0,
	}, logger)
	if probeResult == nil {
		t.Fatal("ProbeBetter returned nil, expected a direct-path candidate")
	}
	if !probeResult.HandshakeOK {
		t.Fatalf("ProbeBetter direct probe: handshake failed: %v", probeResult.Error)
	}
	if probeResult.Path != body.PathDirect {
		t.Fatalf("ProbeBetter: want path 'direct', got %q", probeResult.Path)
	}
	if probeResult.Tunnel == nil {
		t.Fatal("ProbeBetter: returned nil Tunnel in successful result")
	}
	t.Cleanup(func() {
		probeResult.Tunnel.Stop()
		if probeResult.WSSBind != nil {
			probeResult.WSSBind.Close()
		}
	})

	// Prove the new probe path actually works with real traffic
	if err := probeResult.Tunnel.WaitHandshake(probeCtx, 10*time.Second); err != nil {
		t.Fatalf("probe tunnel (direct) handshake: %v", err)
	}
	proveBidirectionalTraffic(t,
		probeResult.Tunnel.Netstack(), coreNetB,
		bodyOverlay, coreOverlay,
		"probe-direct",
	)

	// ── Active tunnel still works ──
	proveBidirectionalTraffic(t,
		initialResult.Tunnel.Netstack(), coreNetA,
		bodyOverlay, coreOverlay,
		"active-relay-udp-after-probe",
	)

	t.Logf("ProbeBetter: found direct path in %v", probeResult.Duration)
}

// TestProbeBetter_FailedProbeLeavesActiveUntouched verifies that a probe
// against a non-existent endpoint fails gracefully and the active tunnel
// continues working normally.
//
// The test sets up an active tunnel on the direct path, then probes for
// candidates that don't exist (all endpoints dead). All probe attempts
// fail and ProbeBetter returns nil. The active tunnel is verified to
// still carry traffic after the probe.
func TestProbeBetter_FailedProbeLeavesActiveUntouched(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	bodyOverlay := netip.MustParseAddr("fd00::2")
	coreOverlay := netip.MustParseAddr("fd00::1")

	logger := testLogger(t)

	// Single Core device for the active path (direct)
	coreDev, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePort),
		RelayWGUDPEndpoint: "",
		RelayWSSURL:        "",
		PerAttemptTimeout:  5 * time.Second,
	}

	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	waitHandshake(t, coreDev, "Core active", 10*time.Second)
	if err := initialResult.Tunnel.WaitHandshake(selCtx, 10*time.Second); err != nil {
		t.Fatalf("active tunnel handshake: %v", err)
	}

	proveBidirectionalTraffic(t,
		initialResult.Tunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay,
		"active-pre-probe",
	)

	// ── ProbeBetter from relay-wss (candidates: direct, relay-udp).
	// Both endpoints are dead → all probes fail → nil result. ──
	probeCfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     "127.0.0.1:1", // dead
		RelayWGUDPEndpoint: "127.0.0.1:1", // dead
		RelayWSSURL:        "",
		PerAttemptTimeout:  3 * time.Second,
	}

	probeResult := body.ProbeBetter(context.Background(), probeCfg, body.PathRelayWSS, body.ProbeConfig{
		PerAttemptTimeout: 3 * time.Second,
		MinBackoff:        1 * time.Second,
		MaxBackoff:        3 * time.Second,
		BackoffFactor:     2.0,
	}, logger)
	if probeResult != nil {
		t.Fatal("ProbeBetter should return nil when all candidates fail")
	}

	// ── Active tunnel still works ──
	proveBidirectionalTraffic(t,
		initialResult.Tunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay,
		"active-after-failed-probe",
	)
}

// TestProbeBetter_Cancellation verifies that when the context is cancelled
// before probing starts, ProbeBetter returns nil without creating a tunnel
// and the active tunnel remains unaffected.
func TestProbeBetter_Cancellation(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	bodyOverlay := netip.MustParseAddr("fd00::2")
	coreOverlay := netip.MustParseAddr("fd00::1")

	logger := testLogger(t)

	// Core device for the active path
	coreDev, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePort),
		RelayWGUDPEndpoint: "",
		RelayWSSURL:        "",
		PerAttemptTimeout:  5 * time.Second,
	}

	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	waitHandshake(t, coreDev, "Core active", 10*time.Second)
	if err := initialResult.Tunnel.WaitHandshake(selCtx, 10*time.Second); err != nil {
		t.Fatalf("active tunnel handshake: %v", err)
	}

	proveBidirectionalTraffic(t,
		initialResult.Tunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay,
		"active-pre-cancel",
	)

	// ── ProbeBetter with immediately cancelled context ──
	// Set currentPath to relay-wss so candidates ([direct, relay-udp])
	// are evaluated. Cancel immediately to test cancellation path.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	probeCfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePort), // would work
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", corePort), // would work
		RelayWSSURL:        "",
		PerAttemptTimeout:  30 * time.Second,
	}

	probeResult := body.ProbeBetter(cancelCtx, probeCfg, body.PathRelayWSS, body.ProbeConfig{
		PerAttemptTimeout: 30 * time.Second,
		MinBackoff:        1 * time.Second,
		MaxBackoff:        3 * time.Second,
		BackoffFactor:     2.0,
	}, logger)
	if probeResult != nil {
		t.Fatal("ProbeBetter should return nil when context is cancelled")
	}

	// ── Active tunnel still works after cancellation ──
	proveBidirectionalTraffic(t,
		initialResult.Tunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay,
		"active-after-cancel",
	)
}
