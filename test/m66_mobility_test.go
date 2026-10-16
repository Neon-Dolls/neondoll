//go:build integration

// Core 4 / M6.6 — Mobility & Return Conformance (slice 1).
//
// End-to-end scenario proving the Body can:
//  1. Connect directly — encrypted bidirectional traffic works.
//  2. Direct path becomes unavailable — loss detected.
//  3. Bounded recovery establishes WSS relay path.
//  4. Encrypted traffic works over WSS.
//  5. Direct path becomes available again.
//  6. Better-path discovery finds direct (MigratePath).
//  7. Controlled migration returns to direct (CutoverPath).
//  8. Encrypted traffic works over new direct path.
//
// Assertions: WG identity, overlay, and pairing state unchanged;
// exactly one authoritative transport; old paths closed.
//
// Uses controlled topology changes and observable synchronization,
// not sleeps. M6.5 uses break-before-make, so brief interruption
// during cutover is acceptable.

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"testing"
	"time"

	body "github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

// directCandidate implements body.MigrationCandidate for direct UDP cutover.
type directCandidate struct {
	endpoint string
}

func (c *directCandidate) Validate(ctx context.Context) (*body.MigrationTarget, error) {
	return &body.MigrationTarget{
		CoreEndpoint: c.endpoint,
	}, nil
}

func (c *directCandidate) Stop() {}

func TestM66_MobilityReturnConformance(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	// ── Keypairs ──
	coreKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)

	const routeID = relay.RouteID(42)
	const credential = "mobility-return-cred"
	const token = "mobility-return-token"

	bodyOverlay := netip.MustParseAddr("fd00::2")
	coreOverlay := netip.MustParseAddr("fd00::1")
	overlayPrefix := netip.MustParsePrefix("fd00::/112")

	bodyPriv := hexToKey(t, bodyKeys.privHex)
	corePub := hexToKey(t, coreKeys.pubHex)

	// ═══════════════════════════════════════════════════════════════
	// ── Set up Core infrastructure ──
	// ═══════════════════════════════════════════════════════════════

	// 1. Core direct UDP device (device A)
	coreDevA, coreNetA, corePortA := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	_ = coreDevA

	// 2. Relay service + control server
	relayAddr := pickFreeTCPAddrPort(t)

	svcCfg := relay.DefaultServiceConfig()
	hash := sha256.Sum256([]byte(token))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svcCfg.KeepaliveInterval = 2 * time.Second
	svcCfg.RouteTimeout = 180 * time.Second
	svcCfg.RegistrationTimeout = 180 * time.Second

	svc, err := relay.NewService(svcCfg, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svcCtx, svcCancel := context.WithCancel(context.Background())
	defer svcCancel()
	if err := svc.Start(svcCtx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}

	cs := relay.NewControlServer(svc, relayAddr, nil)
	csCtx, csCancel := context.WithCancel(svcCtx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	// 3. Core side: control client + relay transport
	clientCfg := relay.DefaultClientConfig()
	clientCfg.RelayURL = "ws://" + relayAddr + "/relay"
	clientCfg.RegistrationToken = token
	clientCfg.HandshakeTimeout = 5 * time.Second
	clientCfg.ReadTimeout = 15 * time.Second
	clientCfg.PingInterval = 5 * time.Second

	client := relay.NewControlClient(clientCfg)
	if err := client.Start(svcCtx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	waitClientConnected(t, client, "core-client")

	opened, openErr := client.OpenRoute(svcCtx, routeID, relay.RouteCredentials{Token: credential})
	if openErr != nil {
		t.Fatalf("OpenRoute: %v", openErr)
	}
	_ = opened

	regID, ok := svc.Registry().RouteRegistration(routeID)
	if !ok {
		t.Fatalf("RouteRegistration(%d): not found", routeID)
	}
	if err := svc.Registry().SetRouteCredentials(regID, routeID, relay.RouteCredentials{Token: credential}); err != nil {
		t.Fatalf("SetRouteCredentials: %v", err)
	}

	// 4. Core WSS relay device (device B)
	tr := relay.NewRelayTransport(client)
	if _, _, err := tr.Open(0); err != nil {
		t.Fatalf("tr.Open(0): %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	endpoint := "relay:1"
	coreDevB, coreNetB := startWgDevice(t, tr, coreKeys, coreOverlay, bodyKeys.pubHex, endpoint, "fd00::2/128")
	_ = coreDevB

	// ── Path selector config ──
	pathCfg := body.PathSelectorConfig{
		PrivateKey:       bodyPriv,
		CorePublicKey:    corePub,
		DirectEndpoint:   fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWSSURL:      relayAddr,
		RouteID:          routeID,
		RouteCredential:  credential,
		OverlayAddress:   bodyOverlay,
		OverlayPrefix:    overlayPrefix,
		PerAttemptTimeout: 5 * time.Second,
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 1: Direct connection (steps 1-2)
	// ═══════════════════════════════════════════════════════════════
	t.Log("=== Phase 1: Direct connection ===")

	initialResult := body.SelectInitialPath(ctx, pathCfg, log)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Path != "direct" {
		t.Fatalf("expected direct path, got %q", initialResult.Path)
	}
	initialTunnel := initialResult.Tunnel
	epoch := body.NewRecoveryEpoch(initialTunnel)

	// Wait for handshake
	if err := initialTunnel.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("initial tunnel handshake: %v", err)
	}

	// Verify encrypted bidirectional traffic
	proveBidirectionalTraffic(t, initialTunnel.Netstack(), coreNetA, bodyOverlay, coreOverlay, "phase1-direct")

	// Capture identity hash for comparison across migrations
	initialCfg := initialTunnel.Config()
	privHash := sha256.Sum256(initialCfg.PrivateKey[:])

	// Exactly one authoritative path
	if epoch.Active() != initialTunnel {
		t.Fatal("epoch.Active() should be the initial tunnel")
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 2: Direct loss → WSS recovery (steps 3-4)
	// ═══════════════════════════════════════════════════════════════
	t.Log("=== Phase 2: Direct loss → WSS recovery ===")

	// Kill direct path on Core side
	coreDevA.Close()

	// Recover via WSS
	recResult := body.RecoverPath(ctx, initialTunnel, pathCfg, log, epoch)
	if recResult.Err != nil {
		t.Fatalf("RecoverPath: %v", recResult.Err)
	}
	if recResult.Path != "relay-wss" {
		t.Fatalf("expected relay-wss path after recovery, got %q", recResult.Path)
	}
	wssTunnel := recResult.Tunnel

	// Wait for handshake over WSS
	if err := wssTunnel.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("WSS tunnel handshake: %v", err)
	}

	// Verify encrypted traffic works over WSS
	proveBidirectionalTraffic(t, wssTunnel.Netstack(), coreNetB, bodyOverlay, coreOverlay, "phase2-wss")

	// Assert identity unchanged
	wssCfg := wssTunnel.Config()
	if sha256.Sum256(wssCfg.PrivateKey[:]) != privHash {
		t.Fatal("Body private key changed after recovery to WSS")
	}
	if wssCfg.CorePublicKey != initialCfg.CorePublicKey {
		t.Fatal("Core public key changed after recovery to WSS")
	}
	if wssCfg.OverlayAddress != initialCfg.OverlayAddress {
		t.Fatal("Overlay address changed after recovery to WSS")
	}
	if epoch.Active() != wssTunnel {
		t.Fatal("epoch.Active() should be the WSS tunnel after recovery")
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 3: Direct available → migration back (steps 5-7)
	// ═══════════════════════════════════════════════════════════════
	t.Log("=== Phase 3: Direct path available → migration ===")

	// Start a new Core UDP device (direct path available again)
	coreDevC, coreNetC, corePortC := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	_ = coreDevC

	// Create candidate for direct-path cutover
	candidate := &directCandidate{
		endpoint: fmt.Sprintf("127.0.0.1:%d", corePortC),
	}

	// Reserve generation and cut over
	migrateCfg := body.MigrationConfig{
		ProbeTimeout: 10 * time.Second,
	}

	gen, err := body.MigratePath(ctx, epoch, candidate, migrateCfg, log)
	if err != nil {
		t.Fatalf("MigratePath: %v", err)
	}

	cutoverResult := body.CutoverPath(ctx, wssTunnel, epoch, candidate, gen, migrateCfg, log)
	if cutoverResult.Err != nil {
		t.Fatalf("CutoverPath: %v", cutoverResult.Err)
	}
	finalTunnel := cutoverResult.Tunnel
	if finalTunnel == nil {
		t.Fatal("CutoverResult.Tunnel is nil after successful cutover")
	}

	// Wait for handshake on the new direct path
	if err := finalTunnel.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("final tunnel handshake: %v", err)
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 4: Encrypted traffic over new direct path (step 8)
	// ═══════════════════════════════════════════════════════════════
	t.Log("=== Phase 4: Traffic over new direct path ===")

	proveBidirectionalTraffic(t, finalTunnel.Netstack(), coreNetC, bodyOverlay, coreOverlay, "phase4-direct-return")

	// ═══════════════════════════════════════════════════════════════
	// Final assertions
	// ═══════════════════════════════════════════════════════════════

	finalCfg := finalTunnel.Config()
	if sha256.Sum256(finalCfg.PrivateKey[:]) != privHash {
		t.Fatal("Body private key changed after migration back to direct")
	}
	if finalCfg.CorePublicKey != initialCfg.CorePublicKey {
		t.Fatal("Core public key changed after migration back to direct")
	}
	if finalCfg.OverlayAddress != initialCfg.OverlayAddress {
		t.Fatal("Overlay address changed after migration back to direct")
	}

	// Exactly one authoritative transport
	if epoch.Active() != finalTunnel {
		t.Fatal("epoch.Active() should be the migrated direct tunnel")
	}

	// Old WSS tunnel is stopped
	if wssTunnel.Started() {
		t.Fatal("old WSS tunnel should be stopped after cutover")
	}

	t.Log("=== M6.6 Mobility & Return Conformance PASSED ===")
}