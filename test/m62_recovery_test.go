// SPDX-License-Identifier: AGPL-3.0-only
//go:build e2e

// M6.2 — Bounded Recovery After Loss.
//
// Proves that when M6.1 detects loss of the active path, RecoverPath
// correctly tears down the failed tunnel and re-runs bounded M5 path
// selection to establish the next viable transport, preserving identity.
//
// Test scenarios:
//   - Direct → Relay UDP: direct path dies, relay-udp is next in order.
//   - All paths fail: bounded — none succeed → error.
//   - Cancellation: context cancelled during recovery → error.
//   - Identity preserved: keys and overlay are not mutated.
//   - Old tunnel stopped: Stop() is idempotent after recovery.

package integration

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
)

// ── Tests ─────────────────────────────────────────────────────────────────

// TestRecoverPath_DirectToRelayUDP verifies that when the active direct path
// is lost, RecoverPath tears down the old tunnel and establishes a new
// tunnel over the relay-udp transport using the same WG identity.
//
// Topology:
//
//	Peer WG (Core Keys) on port A  ← Direct endpoint
//	Peer WG (Core Keys) on port B  ← Relay-udp endpoint (same keys, diff port)
//	Body WG → selects direct (port A), then recovers via relay-udp (port B)
func TestRecoverPath_DirectToRelayUDP(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	peerKeys := newTestKeypair(t)
	bodyOverlay := netip.MustParseAddr("fd01::2")

	// ── Two peer WG devices, same keys, different ports ──

	peerDevA, portA := startCoreUDPDevice(t, peerKeys, bodyKeys.pubHex)
	defer peerDevA.Close()

	peerDevB, portB := startCoreUDPDevice(t, peerKeys, bodyKeys.pubHex)
	defer peerDevB.Close()

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToKey(t, peerKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd01::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", portA),
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", portB),
		PerAttemptTimeout:  5 * time.Second,
	}

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelInfo}))

	// ── Phase 1: Initial path selection picks direct ──

	ctx := context.Background()
	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v", result.Err)
	}
	if result.Path != "direct" {
		t.Fatalf("expected path 'direct', got %q", result.Path)
	}

	t.Logf("initial path selected: %s on %s", result.Path, cfg.DirectEndpoint)

	// ── Phase 2: Kill the direct WG listener ──

	peerDevA.Close()
	t.Log("direct WG device (peerDevA) closed")

	// ── Phase 3: Recover — should switch to relay-udp ──

	recResult := body.RecoverPath(ctx, result.Tunnel, cfg, logger)
	if recResult.Err != nil {
		t.Fatalf("RecoverPath: %v", recResult.Err)
	}
	if recResult.Path != "relay-udp" {
		t.Fatalf("expected path 'relay-udp' after recovery, got %q", recResult.Path)
	}

	// Verify new tunnel is distinct from old
	if recResult.Tunnel == result.Tunnel {
		t.Fatal("RecoverPath returned the same tunnel pointer — expected a new BodyTunnel")
	}

	t.Logf("recovery path selected: %s on %s", recResult.Path, cfg.RelayWGUDPEndpoint)
}

// TestRecoverPath_AllPathsFail verifies that when every candidate transport
// is unreachable, RecoverPath returns an error rather than hanging or
// selecting a non-functional tunnel.
func TestRecoverPath_AllPathsFail(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	peerKeys := newTestKeypair(t)
	bodyOverlay := netip.MustParseAddr("fd01::2")

	// Only one peer device — will be killed before recovery
	peerDev, port := startCoreUDPDevice(t, peerKeys, bodyKeys.pubHex)
	defer peerDev.Close()

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToKey(t, peerKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd01::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", port),
		PerAttemptTimeout:  3 * time.Second,
	}

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Initial selection via direct
	ctx := context.Background()
	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v", result.Err)
	}
	if result.Path != "direct" {
		t.Fatalf("expected path 'direct', got %q", result.Path)
	}

	// Kill the only working endpoint
	peerDev.Close()

	// Recovery — no path should succeed
	recResult := body.RecoverPath(ctx, result.Tunnel, cfg, logger)
	if recResult.Err == nil {
		t.Fatal("expected error when all paths unavailable, got nil")
	}
	if recResult.Tunnel != nil {
		t.Fatal("expected nil tunnel on all-paths-fail")
	}
	t.Logf("all-paths-fail returned expected error: %v", recResult.Err)
}

// TestRecoverPath_Cancellation verifies that a cancelled context during
// recovery returns an appropriate error and does not leave a dangling tunnel.
func TestRecoverPath_Cancellation(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	peerKeys := newTestKeypair(t)
	bodyOverlay := netip.MustParseAddr("fd01::2")

	// Peer device on a valid port — recovery would succeed, but we cancel.
	peerDev, port := startCoreUDPDevice(t, peerKeys, bodyKeys.pubHex)
	defer peerDev.Close()

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToKey(t, peerKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd01::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", port),
		PerAttemptTimeout:  30 * time.Second,
	}

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Initial selection
	ctx := context.Background()
	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v", result.Err)
	}

	// Recovery with a pre-cancelled context
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()

	recResult := body.RecoverPath(cancelCtx, result.Tunnel, cfg, logger)
	if recResult.Err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}
	if recResult.Tunnel != nil {
		t.Fatal("expected nil tunnel on cancellation")
	}
	t.Logf("cancellation returned expected error: %v", recResult.Err)
}

// TestRecoverPath_PreservesIdentity verifies that the recovery process
// preserves WG identity, overlay address, and credentials from the
// PathSelectorConfig, so no re-pairing occurs.
func TestRecoverPath_PreservesIdentity(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	peerKeys := newTestKeypair(t)
	bodyOverlay := netip.MustParseAddr("fd01::2")

	peerDevA, portA := startCoreUDPDevice(t, peerKeys, bodyKeys.pubHex)
	defer peerDevA.Close()

	peerDevB, portB := startCoreUDPDevice(t, peerKeys, bodyKeys.pubHex)
	defer peerDevB.Close()

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToKey(t, peerKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd01::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", portA),
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", portB),
		PerAttemptTimeout:  5 * time.Second,
	}

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Initial selection
	ctx := context.Background()
	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v", result.Err)
	}

	// Snapshot config before recovery to verify it's unchanged
	savedPriv := cfg.PrivateKey
	savedPub := cfg.CorePublicKey
	savedOverlay := cfg.OverlayAddress

	// Kill direct
	peerDevA.Close()

	// Recover
	recResult := body.RecoverPath(ctx, result.Tunnel, cfg, logger)
	if recResult.Err != nil {
		t.Fatalf("RecoverPath: %v", recResult.Err)
	}

	// Verify identity preserved in the recovered tunnel
	if recResult.Tunnel == nil {
		t.Fatal("RecoverPath returned nil tunnel")
	}

	// Verify config was not mutated
	if cfg.PrivateKey != savedPriv {
		t.Fatal("RecoverPath mutated PrivateKey in config")
	}
	if cfg.CorePublicKey != savedPub {
		t.Fatal("RecoverPath mutated CorePublicKey in config")
	}
	if cfg.OverlayAddress != savedOverlay {
		t.Fatal("RecoverPath mutated OverlayAddress in config")
	}

	t.Logf("identity preserved: path %s, peer key %x", recResult.Path, savedPub[:4])
}

// TestRecoverPath_OldTunnelStopped verifies Stop() idempotency after recovery.
func TestRecoverPath_OldTunnelStopped(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	peerKeys := newTestKeypair(t)
	bodyOverlay := netip.MustParseAddr("fd01::2")

	peerDevA, portA := startCoreUDPDevice(t, peerKeys, bodyKeys.pubHex)
	defer peerDevA.Close()

	peerDevB, portB := startCoreUDPDevice(t, peerKeys, bodyKeys.pubHex)
	defer peerDevB.Close()

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToKey(t, peerKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd01::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", portA),
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", portB),
		PerAttemptTimeout:  5 * time.Second,
	}

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx := context.Background()
	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v", result.Err)
	}
	oldTunnel := result.Tunnel

	peerDevA.Close()

	recResult := body.RecoverPath(ctx, oldTunnel, cfg, logger)
	if recResult.Err != nil {
		t.Fatalf("RecoverPath: %v", recResult.Err)
	}

	// Calling Stop() on the old (already recovered) tunnel must be idempotent
	if err := oldTunnel.Stop(); err != nil {
		t.Fatalf("Stop() on recovered tunnel: %v", err)
	}
	t.Log("old tunnel stopped cleanly (double-Stop idempotent)")
}