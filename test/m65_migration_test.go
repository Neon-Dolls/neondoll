// SPDX-License-Identifier: AGPL-3.0-only
//go:build integration

// Core 4 / M6.5 — Controlled Path Migration.
//
// Integration tests confirming:
//   - MigratePath transitions from old path to candidate on successful
//     validation
//   - Failed candidate (dead endpoint, no WG handshake) keeps old path
//     authoritative
//   - Context cancellation preserves old path
//   - Superseded candidate (epoch generation advanced during validation)
//     is rejected with ErrMigrationSuperseded; old path untouched
//   - Bidirectional encrypted overlay traffic after migration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
)

// --------------------------------------------------------------------------
// Test 1: Basic migration — candidate proven, committed, old stopped
// --------------------------------------------------------------------------

func TestMigratePath_Basic(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	logger := testLogger(t)

	// One Core device — both tunnels peer with it.
	coreDev, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	_ = coreDev

	// ── 1. Build the initial (old) tunnel via SelectInitialPath ──
	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

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

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Path != "direct" {
		t.Fatalf("want path 'direct', got %q", initialResult.Path)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	// Wait for the WireGuard handshake to complete on Core's side
	waitHandshake(t, coreDev, "Core (old tunnel)", 10*time.Second)

	// ── 2. Build the candidate tunnel (same Body keys, same Core) ──
	candidate := body.NewBodyTunnel(body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   fmt.Sprintf("127.0.0.1:%d", corePort),
		MTU:            1280,
	}, logger)

	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	if err := candidate.Start(startCtx); err != nil {
		t.Fatalf("candidate.Start: %v", err)
	}
	t.Cleanup(func() { candidate.Stop() })

	// The candidate's overlay netstack generates traffic → WG handshake.
	waitHandshake(t, coreDev, "Core (candidate)", 10*time.Second)

	// ── 3. Create epoch with old tunnel as active ──
	epoch := body.NewRecoveryEpoch(initialResult.Tunnel)

	if active := epoch.Active(); active != initialResult.Tunnel {
		t.Fatal("epoch.Active() should be the initial tunnel before migration")
	}

	// ── 4. Migrate ──
	migCtx, migCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer migCancel()

	result := body.MigratePath(
		migCtx,
		initialResult.Tunnel,
		candidate,
		body.MigrationConfig{ProbeTimeout: 10 * time.Second},
		logger,
		epoch,
	)
	if result.Err != nil {
		t.Fatalf("MigratePath: %v", result.Err)
	}
	if result.Tunnel != candidate {
		t.Fatal("MigratePath returned wrong tunnel")
	}

	// ── 5. Verify post-migration state ──
	if active := epoch.Active(); active != candidate {
		t.Fatal("epoch.Active() should be the candidate after migration")
	}

	// Bidirectional overlay traffic through the candidate's path
	bodyNet := candidate.Netstack()
	proveBidirectionalTraffic(t, bodyNet, coreNet,
		bodyOverlay, netip.MustParseAddr("fd00::1"),
		"post-migration")
}

// --------------------------------------------------------------------------
// Test 2: Failed candidate (dead endpoint) — old path preserved
// --------------------------------------------------------------------------

func TestMigratePath_FailedCandidate(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	logger := testLogger(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

	// Initial tunnel
	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

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

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })
	waitHandshake(t, coreDev, "Core (old)", 10*time.Second)

	// Candidate pointing to a dead endpoint — Start succeeds (wireguard
	// config), but WG handshake can never complete.
	candidate := body.NewBodyTunnel(body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   "127.0.0.1:1", // dead — no WG listener
		MTU:            1280,
	}, logger)

	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	if err := candidate.Start(startCtx); err != nil {
		t.Fatalf("candidate.Start: %v", err)
	}
	t.Cleanup(func() { candidate.Stop() })

	epoch := body.NewRecoveryEpoch(initialResult.Tunnel)
	if active := epoch.Active(); active != initialResult.Tunnel {
		t.Fatal("epoch.Active() should be the initial tunnel")
	}

	// Migrate with a short probe timeout — candidate will fail.
	migCtx, migCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer migCancel()

	result := body.MigratePath(
		migCtx,
		initialResult.Tunnel,
		candidate,
		body.MigrationConfig{ProbeTimeout: 3 * time.Second},
		logger,
		epoch,
	)
	if result.Err == nil {
		t.Fatal("MigratePath should fail with dead-endpoint candidate")
	}

	// Old tunnel must remain authoritative.
	if active := epoch.Active(); active != initialResult.Tunnel {
		t.Fatal("epoch.Active() must remain the old tunnel after failed candidate")
	}
}

// --------------------------------------------------------------------------
// Test 3: Context cancellation — old path preserved
// --------------------------------------------------------------------------

func TestMigratePath_Cancellation(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	logger := testLogger(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

	// Initial tunnel
	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

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

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })
	waitHandshake(t, coreDev, "Core (old)", 10*time.Second)

	// Build a candidate tunnel — it would succeed, but we cancel before
	// migration validates it.
	candidate := body.NewBodyTunnel(body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   fmt.Sprintf("127.0.0.1:%d", corePort),
		MTU:            1280,
	}, logger)

	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	if err := candidate.Start(startCtx); err != nil {
		t.Fatalf("candidate.Start: %v", err)
	}
	t.Cleanup(func() { candidate.Stop() })

	epoch := body.NewRecoveryEpoch(initialResult.Tunnel)

	// Create an already-cancelled context.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	result := body.MigratePath(
		cancelledCtx,
		initialResult.Tunnel,
		candidate,
		body.MigrationConfig{ProbeTimeout: 10 * time.Second},
		logger,
		epoch,
	)
	if result.Err == nil {
		t.Fatal("MigratePath should fail when context is cancelled")
	}
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", result.Err)
	}

	// Old tunnel remains authoritative.
	if active := epoch.Active(); active != initialResult.Tunnel {
		t.Fatal("epoch.Active() must remain the old tunnel after cancellation")
	}
}

// --------------------------------------------------------------------------
// Test 4: Superseded candidate — epoch gen advances during validation
// --------------------------------------------------------------------------
//
// This test races a goroutine that bumps the epoch generation while
// MigratePath is validating the candidate.  WaitHandshake polls every
// 200ms, and the goroutine fires after 100ms, giving a deterministic
// window for the gen bump before the first poll.  The candidate completes
// its real WG handshake, but TryCommit fails because gen != e.gen.

func TestMigratePath_SupersededDuringValidation(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	logger := testLogger(t)

	coreDev, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	_ = coreNet

	// Initial tunnel
	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

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

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })
	waitHandshake(t, coreDev, "Core (old)", 10*time.Second)

	// Candidate tunnel — real endpoint, will handshake.
	candidate := body.NewBodyTunnel(body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   fmt.Sprintf("127.0.0.1:%d", corePort),
		MTU:            1280,
	}, logger)

	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	if err := candidate.Start(startCtx); err != nil {
		t.Fatalf("candidate.Start: %v", err)
	}
	t.Cleanup(func() { candidate.Stop() })

	epoch := body.NewRecoveryEpoch(initialResult.Tunnel)

	// We'll bump the generation while migration is in-flight.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Wait for MigratePath to enter WaitHandshake (NextGenForTunnel has
		// claimed gen by this point, and WaitHandshake's first tick is 200ms
		// away — ample time for this bump).
		time.Sleep(100 * time.Millisecond)
		epoch.NextGen() // bumps gen past the claimed value
	}()

	migCtx, migCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer migCancel()

	result := body.MigratePath(
		migCtx,
		initialResult.Tunnel,
		candidate,
		body.MigrationConfig{ProbeTimeout: 10 * time.Second},
		logger,
		epoch,
	)
	wg.Wait()

	if result.Err == nil {
		t.Fatal("MigratePath should fail when epoch generation advances")
	}
	if !errors.Is(result.Err, body.ErrMigrationSuperseded) {
		t.Fatalf("expected ErrMigrationSuperseded, got: %v", result.Err)
	}

	// Old tunnel must still be authoritative.
	if active := epoch.Active(); active != initialResult.Tunnel {
		t.Fatal("epoch.Active() must remain the old tunnel after superseded migration")
	}
}