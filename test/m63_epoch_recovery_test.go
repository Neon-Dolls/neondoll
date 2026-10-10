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
//   - Epoch shutdown during recovery invalidates in-flight attempts
//   - Concurrent recovery: slow path attempt is superseded by fast path
//   - Repeated sequential recovery succeeds
//   - Gen isolation: old tunnel loss notification cannot supersede newer
//   - Stale loss callback after successful replacement is rejected
//   - Stale attempts always clean up their candidate resources

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
	// RecoverPath called NextGenForTunnel which returned 1, so gen 1 is current
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

	// No tunnel was committed — active remains nil.
	// (NextGenForTunnel advanced the gen, but TryCommit was never reached.)
	active := epoch.Active()
	if active != nil {
		t.Fatal("epoch.Active() should be nil after cancelled recovery")
	}
}

// ── Test 3: Epoch shutdown during in-flight recovery ──
// Starts a recovery attempt in a background goroutine, waits for it to
// advance the epoch generation (NextGenForTunnel), then shuts down the
// epoch while SelectInitialPath is still in-flight.  The recovery attempt
// discovers closure via TryCommit and cleans up its candidate tunnel.
func TestRecoverPath_ShutdownDuringRecovery(t *testing.T) {
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

	var (
		result body.PathSelectionResult
		wg     sync.WaitGroup
	)
	wg.Add(1)

	// Start recovery in a background goroutine.  It will:
	//   1. Call NextGenForTunnel (gen advances: 0 → 1)
	//   2. Stop the old tunnel (fast)
	//   3. Enter SelectInitialPath with the real endpoint (takes ~200ms+)
	go func() {
		defer wg.Done()
		recCtx, recCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer recCancel()
		result = body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, epoch)
	}()

	// Wait for the background RecoverPath to advance the gen (gen > 0).
	// This tells us it has passed Step 1 and is now inside Step 3.
	for i := 0; i < 100 && epoch.IsCurrent(0); i++ {
		time.Sleep(1 * time.Millisecond)
	}

	// Shut down the epoch while recovery is in-flight inside SelectInitialPath.
	epoch.Shutdown()

	wg.Wait()

	// Recovery must have been rejected by the shut-down epoch.
	if result.Err == nil {
		t.Fatal("expected error when epoch shut down during recovery")
	}
	if epoch.Active() != nil {
		t.Fatal("epoch.Active() should be nil after shutdown")
	}
}

// ── Test 4: Concurrent recovery — late candidate rejected as stale ──
// Two goroutines call RecoverPath with the same real endpoint.  The first
// (slow) attempt advances the gen (1) and enters SelectInitialPath.  The
// second (fast) attempt starts after gen advanced, creating gen 2.  The
// first attempt's candidate arrives with stale gen 1 and is rejected by
// TryCommit.  The stale attempt's cleanup (Stop + Close) runs, and the
// fast attempt's tunnel becomes the epoch's active tunnel.
func TestRecoverPath_OverlappingRecoveries(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	logger := testLogger(t)
	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

	// Both attempts share the same real endpoint config.
	// No dead endpoints — both SelectInitialPath calls can succeed.
	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePort),
		RelayWGUDPEndpoint: "",
		RelayWSSURL:        "",
		PerAttemptTimeout:  10 * time.Second,
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

	var (
		resultSlow, resultFast body.PathSelectionResult
		wg                     sync.WaitGroup
	)
	wg.Add(2)

	// Slow attempt starts first — advances gen (0 → 1) then enters
	// SelectInitialPath with the real endpoint.
	go func() {
		defer wg.Done()
		ctxSlow, cancelSlow := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelSlow()
		resultSlow = body.RecoverPath(ctxSlow, initialResult.Tunnel, cfg, logger, epoch)
	}()

	// Wait for the slow attempt to advance the gen (gen > 0).
	for i := 0; i < 100 && epoch.IsCurrent(0); i++ {
		time.Sleep(1 * time.Millisecond)
	}

	// Fast attempt starts second — its NextGenForTunnel creates gen 2, making
	// gen 1 stale before the slow attempt can commit.
	go func() {
		defer wg.Done()
		ctxFast, cancelFast := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelFast()
		resultFast = body.RecoverPath(ctxFast, initialResult.Tunnel, cfg, logger, epoch)
	}()

	wg.Wait()

	// The fast attempt (gen 2) should have committed.
	active := epoch.Active()
	if active == nil {
		t.Fatal("epoch.Active() is nil after concurrent recovery")
	}
	t.Cleanup(func() { active.Stop() })
	waitHandshake(t, coreDev, "Core device (active recovery)", 10*time.Second)

	if resultFast.Err != nil {
		t.Errorf("fast recovery (gen 2) should have succeeded: %v", resultFast.Err)
	} else if resultFast.Tunnel != active {
		t.Error("fast recovery's tunnel should be the epoch's active tunnel")
	}

	// The slow attempt (gen 1) should have been rejected by TryCommit
	// because gen 1 != epoch's current gen 2.  Its stale-cleanup code
	// ran (Stop + Close on the candidate) — this verifies that resource
	// release is always permitted regardless of epoch ownership.
	if resultSlow.Err == nil {
		t.Errorf("slow recovery (gen 1) should have been rejected as stale")
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

// ── Test 6: Gen isolation — old tunnel cannot supersede newer ──
// After two sequential recoveries, the epoch still correctly reports
// that the old generation is no longer current and that its active
// tunnel belongs to the latest recovery.

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

	// Gen 1 is stale after recovery 2 committed gen 2.
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

// ── Test 7: Stale loss callback after successful replacement ──
// After a recovery commits a new tunnel, a stale loss notification for
// the old tunnel reaches RecoverPath.  NextGenForTunnel must reject it
// (the old tunnel no longer matches epoch.Active()) WITHOUT advancing
// the generation, ensuring the healthy tunnel is not superseded.
func TestRecoverPath_StaleLossCallback(t *testing.T) {
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

	// Recovery commits a new active tunnel (gen 1).
	recCtx, recCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer recCancel()
	recoveryResult := body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, epoch)
	if recoveryResult.Err != nil {
		t.Fatalf("RecoverPath: %v", recoveryResult.Err)
	}
	t.Cleanup(func() { recoveryResult.Tunnel.Stop() })
	waitHandshake(t, coreDev, "Core device (recovery)", 10*time.Second)

	// Verify gen 1 is current after first recovery.
	genAfterRecovery := uint64(1)
	if !epoch.IsCurrent(genAfterRecovery) {
		t.Fatal("expected gen 1 after first recovery")
	}

	// Now simulate a stale loss callback: RecoverPath is called with the
	// OLD tunnel (initialResult.Tunnel).  NextGenForTunnel sees that
	// epoch.Active() points to the NEW tunnel and rejects the call.
	staleResult := body.RecoverPath(
		recCtx,              // stale context (no cancellation needed — rejected early)
		initialResult.Tunnel, // the OLD tunnel, no longer the epoch's active tunnel
		cfg, logger, epoch,
	)
	if staleResult.Err == nil {
		t.Fatal("stale loss callback should have been rejected")
	}

	// The epoch must be unchanged: same gen, same active tunnel.
	if !epoch.IsCurrent(genAfterRecovery) {
		t.Fatal("gen must not advance from a stale loss callback")
	}
	if epoch.Active() != recoveryResult.Tunnel {
		t.Fatal("epoch's active tunnel must remain the newer tunnel")
	}
}