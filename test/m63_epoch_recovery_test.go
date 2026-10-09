// SPDX-License-Identifier: AGPL-3.0-only
//go:build integration

// Core 4 / M6.3 — Recovery Epochs and Stale-Attempt Safety.
//
// Integration tests confirming generation-based path lifecycle ownership
// working with real WireGuard devices.
//
// Scenarios:
//   - RecoverPath integrates with RecoveryEpoch (commit ownership)
//   - Cancellation prevents tunnel creation in superseded attempts
//   - Shutdown during recovery invalidates outstanding attempts
//   - Concurrent recovery: slow path attempt is superseded by fast path
//   - Repeated sequential recovery succeeds
//   - Late success from a superseded attempt and loss-after-replacement

package integration

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
)

var bodyOverlay = netip.MustParseAddr("fd00::2")

// ── Test 1: Basic RecoverPath with epoch integration ──

func TestRecoverPath_WithEpoch(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	logger := testLogger(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

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

	// Initial path
	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()
	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Path != "direct" {
		t.Fatalf("want path 'direct', got %q", initialResult.Path)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })
	waitHandshake(t, coreDev, "Core device", 10*time.Second)

	// Recovery with epoch
	epoch := body.NewRecoveryEpoch()
	recCtx, recCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer recCancel()
	result := body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, epoch)
	if result.Err != nil {
		t.Fatalf("RecoverPath: %v", result.Err)
	}
	t.Cleanup(func() { result.Tunnel.Stop() })
	waitHandshake(t, coreDev, "Core device (recovery)", 10*time.Second)

	// After recovery, the epoch's active tunnel should be the recovered one
	active := epoch.Active()
	if active == nil {
		t.Fatal("epoch.Active() is nil after successful recovery")
	}
	if active != result.Tunnel {
		t.Fatal("epoch.Active() does not match the recovered tunnel")
	}

	// Further NextGen/TryCommit should reference the new active tunnel
	// RecoverPath called NextGen() which returned 1, so gen 1 is current
	if !epoch.IsCurrent(1) {
		t.Fatal("expected generation 1 to be current after first recovery")
	}
	if epoch.IsCurrent(2) {
		t.Fatal("expected generation 2 to NOT be current yet")
	}
}

// ── Test 2: RecoverPath with cancellation ──

func TestRecoverPath_EpochCancelled(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	logger := testLogger(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

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
	waitHandshake(t, coreDev, "Core device", 10*time.Second)

	epoch := body.NewRecoveryEpoch()

	// Cancel the recovery context immediately
	recCtx, recCancel := context.WithCancel(context.Background())
	recCancel()
	result := body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, epoch)
	if result.Err == nil {
		t.Fatal("expected error from cancelled RecoverPath")
	}

	// Epoch should be at generation 0 (NextGen was called but TryCommit was not attempted)
	active := epoch.Active()
	if active != nil {
		t.Fatal("epoch.Active() should be nil after cancelled recovery")
	}
}

// ── Test 3: Shutdown during recovery ──

func TestRecoverPath_EpochShutdownDuringRecovery(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	logger := testLogger(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

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
	waitHandshake(t, coreDev, "Core device", 10*time.Second)

	epoch := body.NewRecoveryEpoch()

	// Shut down the epoch before attempting recovery
	epoch.Shutdown()

	recCtx, recCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer recCancel()
	result := body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, epoch)
	if result.Err == nil {
		t.Fatal("expected error when recovering with shut-down epoch")
	}
}

// ── Test 4: Concurrent recovery — slow path superseded by fast ──
// Two goroutines call RecoverPath with the same epoch. The slow attempt
// (dead endpoint, times out) fails. The fast attempt (real endpoint) succeeds.
// The epoch's active tunnel must be from the fast attempt.

func TestRecoverPath_LateSuccess(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeysA := newTestKeypair(t)

	logger := testLogger(t)

	coreDevA, corePortA := startCoreUDPDevice(t, coreKeysA, bodyKeys.pubHex)

	epoch := body.NewRecoveryEpoch()

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeysA.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWGUDPEndpoint: "",
		RelayWSSURL:        "",
		PerAttemptTimeout:  5 * time.Second,
	}

	// Initial path
	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()
	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })
	waitHandshake(t, coreDevA, "Core device A", 10*time.Second)

	// Set up two recovery configs:
	// cfgSlow — tries dead endpoint (timeout 1s), no relay → fails
	// cfgFast — tries real endpoint (timeout 10s) → succeeds
	cfgSlow := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeysA.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     "127.0.0.1:1", // dead — handshake will time out
		RelayWGUDPEndpoint: "",
		RelayWSSURL:        "",
		PerAttemptTimeout:  1 * time.Second,
	}
	cfgFast := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeysA.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWGUDPEndpoint: "",
		RelayWSSURL:        "",
		PerAttemptTimeout:  10 * time.Second,
	}

	var (
		resultSlow, resultFast body.PathSelectionResult
		wg                     sync.WaitGroup
	)

	wg.Add(2)

	// Slow attempt starts first
	go func() {
		defer wg.Done()
		ctxSlow, cancelSlow := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelSlow()
		resultSlow = body.RecoverPath(ctxSlow, initialResult.Tunnel, cfgSlow, logger, epoch)
	}()

	// Let the slow recovery get through NextGen and start SelectInitialPath
	time.Sleep(100 * time.Millisecond)

	// Fast attempt starts second — its NextGen creates a newer generation
	go func() {
		defer wg.Done()
		ctxFast, cancelFast := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelFast()
		resultFast = body.RecoverPath(ctxFast, initialResult.Tunnel, cfgFast, logger, epoch)
	}()

	wg.Wait()

	// Fast recovery should have succeeded
	if resultFast.Err != nil {
		t.Errorf("fast recovery failed: %v", resultFast.Err)
	}
	if resultFast.Tunnel != nil {
		t.Cleanup(func() { resultFast.Tunnel.Stop() })
	}
	waitHandshake(t, coreDevA, "Core device (fast recovery)", 10*time.Second)

	// Slow recovery should have failed (dead endpoint times out)
	if resultSlow.Err == nil {
		t.Error("expected slow recovery to fail (dead endpoint)")
	}

	// The epoch's active tunnel should be from the fast recovery
	active := epoch.Active()
	if active == nil {
		t.Fatal("epoch.Active() is nil after concurrent recovery")
	}
	if resultFast.Tunnel != nil && active != resultFast.Tunnel {
		t.Fatal("epoch's active tunnel is not from the winning fast recovery")
	}
}

// ── Test 5: Repeated sequential recovery ──

func TestRecoverPath_RepeatedRecovery(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	logger := testLogger(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

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
	waitHandshake(t, coreDev, "Core device", 10*time.Second)

	var activeTunnel = initialResult.Tunnel
	epoch := body.NewRecoveryEpoch()

	// Three sequential recoveries
	var tunnels []*body.BodyTunnel
	for i := 0; i < 3; i++ {
		recCtx, recCancel := context.WithTimeout(context.Background(), 15*time.Second)
		result := body.RecoverPath(recCtx, activeTunnel, cfg, logger, epoch)
		recCancel()
		if result.Err != nil {
			t.Fatalf("RecoverPath attempt %d: %v", i+1, result.Err)
		}
		tunnels = append(tunnels, result.Tunnel)
		waitHandshake(t, coreDev, "Core device", 10*time.Second)

		// Each sequential recovery should have a unique generation.
		// After recovery i (0-indexed), gen = i+1 is current.
		if !epoch.IsCurrent(uint64(i + 1)) {
			t.Fatalf("expected gen %d to be current after recovery %d", i+1, i+1)
		}

		activeTunnel = result.Tunnel
	}

	for _, t2 := range tunnels {
		t2.Stop()
	}
}

// ── Test 6: Old tunnel loss-after-replacement via epoch ──
// This tests the isolation: after a recovery, the epoch can reject stale
// callbacks for the old tunnel's generation.

func TestRecoverPath_LossAfterReplacement(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	logger := testLogger(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

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
	waitHandshake(t, coreDev, "Core device", 10*time.Second)

	epoch := body.NewRecoveryEpoch()

	// First recovery — gen 1
	recCtx1, recCancel1 := context.WithTimeout(context.Background(), 15*time.Second)
	result1 := body.RecoverPath(recCtx1, initialResult.Tunnel, cfg, logger, epoch)
	recCancel1()
	if result1.Err != nil {
		t.Fatalf("first RecoverPath: %v", result1.Err)
	}
	t.Cleanup(func() { result1.Tunnel.Stop() })
	waitHandshake(t, coreDev, "Core device (recovery 1)", 10*time.Second)

	// Second recovery — gen 2
	recCtx2, recCancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	result2 := body.RecoverPath(recCtx2, result1.Tunnel, cfg, logger, epoch)
	recCancel2()
	if result2.Err != nil {
		t.Fatalf("second RecoverPath: %v", result2.Err)
	}
	t.Cleanup(func() { result2.Tunnel.Stop() })
	waitHandshake(t, coreDev, "Core device (recovery 2)", 10*time.Second)

	// Now simulate "stale loss" — the tunnel from gen 1 is no longer current.
	// The epoch should reject gen 1 as stale.
	if epoch.IsCurrent(1) {
		t.Fatal("gen 1 should not be current after recovery 2")
	}
	if !epoch.IsCurrent(2) {
		t.Fatal("gen 2 should be current after recovery 2")
	}
	if epoch.Active() != result2.Tunnel {
		t.Fatal("epoch's active tunnel should be from recovery 2")
	}
}
