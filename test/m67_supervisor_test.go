//go:build integration

// Core 4 / M6.7 — Body Path Supervisor (slice 1).
//
// Integration tests confirming:
//   - Supervisor detects loss and automatically recovers via relay
//   - Supervisor shuts down cleanly when context is cancelled during recovery

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
	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

func TestM67_LossAndRecovery(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	// ── Keypairs ──
	coreKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)

	const routeID = relay.RouteID(42)
	const credential = "supervisor-loss-recovery-cred"
	const token = "supervisor-loss-recovery-token"

	bodyOverlay := netip.MustParseAddr("fd00::2")
	coreOverlay := netip.MustParseAddr("fd00::1")
	overlayPrefix := netip.MustParsePrefix("fd00::/112")

	bodyPriv := hexToKey(t, bodyKeys.privHex)
	corePub := hexToKey(t, coreKeys.pubHex)

	// ═════════════════════════════════════════════════
	// Core infrastructure
	// ═════════════════════════════════════════════════

	// 1. Core direct UDP device (path A)
	coreDevA, coreNetA, corePortA := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)

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

	// 4. Core WSS relay device (path B)
	tr := relay.NewRelayTransport(client)
	if _, _, err := tr.Open(0); err != nil {
		t.Fatalf("tr.Open(0): %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	coreDevB, coreNetB := startWgDevice(t, tr, coreKeys, coreOverlay, bodyKeys.pubHex, "relay:1", "fd00::2/128")
	_ = coreDevB

	// ── Path selector config ──
	pathCfg := body.PathSelectorConfig{
		PrivateKey:        bodyPriv,
		CorePublicKey:     corePub,
		DirectEndpoint:    fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWSSURL:       relayAddr,
		RouteID:           routeID,
		RouteCredential:   credential,
		OverlayAddress:    bodyOverlay,
		OverlayPrefix:     overlayPrefix,
		PerAttemptTimeout: 5 * time.Second,
	}

	// ═════════════════════════════════════════════════
	// Phase 1: Initial direct connection
	// ═════════════════════════════════════════════════

	initialResult := body.SelectInitialPath(ctx, pathCfg, log)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Path != "direct" {
		t.Fatalf("expected direct path, got %q", initialResult.Path)
	}
	initialTunnel := initialResult.Tunnel

	if err := initialTunnel.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("initial tunnel handshake: %v", err)
	}

	proveBidirectionalTraffic(t, initialTunnel.Netstack(), coreNetA, bodyOverlay, coreOverlay, "phase1-direct")

	initialCfg := initialTunnel.Config()
	privHash := sha256.Sum256(initialCfg.PrivateKey[:])

	// ═════════════════════════════════════════════════
	// Phase 2: Start supervisor, kill direct, verify auto-recovery
	// ═════════════════════════════════════════════════

	epoch := body.NewRecoveryEpoch(initialTunnel)

	supCfg := body.SupervisorConfig{
		PathSelector: pathCfg,
		LossObserver: body.LossObserverConfig{
			WGLivenessTimeout: 6 * time.Second,
			PollInterval:      500 * time.Millisecond,
		},
		RecoverTimeout: 10 * time.Second,
		SharedEpoch:    epoch,
	}

	supCtx, supCancel := context.WithCancel(ctx)
	defer supCancel()

	sup, err := body.StartPathSupervisor(supCtx, initialTunnel, supCfg, log)
	if err != nil {
		t.Fatalf("StartPathSupervisor: %v", err)
	}

	if epoch.Active() != initialTunnel {
		t.Fatal("epoch.Active() should still be initial tunnel before loss")
	}

	// Kill the direct path
	t.Logf("killing direct Core device (port %d)...", corePortA)
	coreDevA.Close()

	// Wait for supervisor to detect loss and recover.  The supervisor
	// internally starts a loss observer, then on loss calls RecoverPath
	// which commits the new tunnel to the epoch.
	timeout := 30 * time.Second
	deadline := time.Now().Add(timeout)

	var recoveredTunnel *body.BodyTunnel
	for time.Now().Before(deadline) {
		recoveredTunnel = epoch.Active()
		if recoveredTunnel != nil && recoveredTunnel != initialTunnel {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if recoveredTunnel == nil || recoveredTunnel == initialTunnel {
		t.Fatalf("supervisor did not recover within %v", timeout)
	}

	t.Log("supervisor recovered — epoch active tunnel changed")

	// Verify identity preserved
	recoveredCfg := recoveredTunnel.Config()
	recoveredHash := sha256.Sum256(recoveredCfg.PrivateKey[:])
	if privHash != recoveredHash {
		t.Fatal("recovered tunnel private key changed! identity not preserved")
	}

	// Verify traffic over recovered path
	if err := recoveredTunnel.WaitHandshake(ctx, 10*time.Second); err != nil {
		t.Fatalf("recovered tunnel handshake: %v", err)
	}

	proveBidirectionalTraffic(t, recoveredTunnel.Netstack(), coreNetB, bodyOverlay, coreOverlay, "phase2-recovered")

	// Supervisor should still be running
	select {
	case <-sup.Done():
		t.Fatal("supervisor exited unexpectedly after recovery")
	default:
	}
}

func TestM67_ShutdownDuringRecovery(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	coreKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)

	const routeID = relay.RouteID(43)
	const credential = "shutdown-recovery-cred"
	const token = "shutdown-recovery-token"

	bodyOverlay := netip.MustParseAddr("fd00::4")
	overlayPrefix := netip.MustParsePrefix("fd00::/112")

	bodyPriv := hexToKey(t, bodyKeys.privHex)
	corePub := hexToKey(t, coreKeys.pubHex)

	// Direct-only setup — no real relay.  The supervisor's recovery
	// will be given a bad relay WSS URL so recovery keeps retrying
	// (ErrAllPathsFailed → retryable → backoff → retry).  We cancel
	// the context while recovery is in the backoff-retry cycle.
	coreDevA, coreNetA, corePortA := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	_ = coreNetA

	// Initial path selector for direct connection only
	pathCfg := body.PathSelectorConfig{
		PrivateKey:        bodyPriv,
		CorePublicKey:     corePub,
		DirectEndpoint:    fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWSSURL:       "127.0.0.1:1", // nothing listens here → recovery fails fast
		RouteID:           routeID,
		RouteCredential:   credential,
		OverlayAddress:    bodyOverlay,
		OverlayPrefix:     overlayPrefix,
		PerAttemptTimeout: 2 * time.Second,
	}

	initialResult := body.SelectInitialPath(ctx, pathCfg, log)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Path != "direct" {
		t.Fatalf("expected direct path, got %q", initialResult.Path)
	}
	initialTunnel := initialResult.Tunnel

	if err := initialTunnel.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("initial tunnel handshake: %v", err)
	}

	epoch := body.NewRecoveryEpoch(initialTunnel)

	supCfg := body.SupervisorConfig{
		PathSelector: pathCfg,
		LossObserver: body.LossObserverConfig{
			WGLivenessTimeout: 6 * time.Second,
			PollInterval:      500 * time.Millisecond,
		},
		RecoverTimeout: 2 * time.Second,
		SharedEpoch:    epoch,
	}

	supCtx, supCancel := context.WithCancel(ctx)

	sup, err := body.StartPathSupervisor(supCtx, initialTunnel, supCfg, log)
	if err != nil {
		t.Fatalf("StartPathSupervisor: %v", err)
	}

	// Kill the direct path so loss fires → supervisor starts recovery
	// (which will fail repeatedly against the bogus relay)
	coreDevA.Close()

	// Wait for recovery to start (IsRecovering returns true),
	// then cancel while recovery is in-flight.
	t.Log("waiting for recovery to start...")
	recoveryDeadline := time.Now().Add(15 * time.Second)
	for !epoch.IsRecovering() {
		if time.Now().After(recoveryDeadline) {
			t.Fatal("recovery did not start within deadline")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Cancel context while recovery is in-flight (retrying via backoff)
	t.Log("cancelling supervisor context during recovery...")
	supCancel()

	// Verify clean shutdown within 3 seconds
	select {
	case <-sup.Done():
		t.Log("supervisor shut down cleanly during recovery")
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not shut down within 3s after context cancellation during recovery")
	}
}

// TestM67_RecoveryRetry verifies that the supervisor retries recovery
// when the recovery path becomes available during the backoff window.
//
// 1. Relay infrastructure starts but route is NOT open.
// 2. Body establishes a direct-UDP tunnel to Core A.
// 3. Supervisor starts observing.
// 4. Core A is killed → loss detected → first recovery attempt fails
//    (relay route not registered).
// 5. During backoff: route is opened and Core B is started on the relay.
// 6. Supervisor retries → recovery succeeds via relay WSS.
// 7. Identity, handshake, and bidirectional traffic are verified.
func TestM67_RecoveryRetry(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	coreKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)
	var bodyPriv [32]byte
	priv, err := hex.DecodeString(bodyKeys.privHex)
	if err != nil {
		t.Fatalf("decode body priv: %v", err)
	}
	copy(bodyPriv[:], priv)

	corePub := hexToKey(t, coreKeys.pubHex)
	overlayPrefix := netip.MustParsePrefix("fd00::/112")
	bodyOverlay := netip.MustParseAddr("fd00::10")
	coreOverlay := netip.MustParseAddr("fd00::11")

	routeID := relay.RouteID(43)
	credential := "retry-test-cred"

	// ── Relay infrastructure ready but route NOT open ──
	relayAddr := pickFreeTCPAddrPort(t)
	svcCtx, svcCancel := context.WithCancel(ctx)
	csCtx, csCancel := context.WithCancel(ctx)
	defer svcCancel()
	defer csCancel()

	svcCfg := relay.DefaultServiceConfig()
	hash := sha256.Sum256([]byte(credential))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svcCfg.KeepaliveInterval = 2 * time.Second
	svcCfg.RouteTimeout = 180 * time.Second
	svcCfg.RegistrationTimeout = 180 * time.Second

	svc, err := relay.NewService(svcCfg, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Start(svcCtx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}

	cs := relay.NewControlServer(svc, relayAddr, nil)
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	clientCfg := relay.DefaultClientConfig()
	clientCfg.RelayURL = "ws://" + relayAddr + "/relay"
	clientCfg.RegistrationToken = credential
	clientCfg.HandshakeTimeout = 5 * time.Second
	clientCfg.ReadTimeout = 15 * time.Second
	clientCfg.PingInterval = 5 * time.Second

	client := relay.NewControlClient(clientCfg)
	if err := client.Start(svcCtx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	waitClientConnected(t, client, "relay-client")

	// Route is intentionally NOT opened — first recovery will fail.

	// ── Core A (direct UDP) ──
	coreDevA, coreNetA, coreAPort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	_ = coreNetA
	// ── Path selector config (shared: init + supervisor) ──
	pathCfg := body.PathSelectorConfig{
		PrivateKey:        bodyPriv,
		CorePublicKey:     corePub,
		DirectEndpoint:    fmt.Sprintf("127.0.0.1:%d", coreAPort),
		RelayWSSURL:       relayAddr,
		RouteID:           routeID,
		RouteCredential:   credential,
		OverlayAddress:    bodyOverlay,
		OverlayPrefix:     overlayPrefix,
		PerAttemptTimeout: 6 * time.Second,
	}

	// ── Body tunnel via direct path ──
	initial := body.SelectInitialPath(ctx, pathCfg, log)
	if initial.Err != nil {
		t.Fatalf("initial path selection: %v", initial.Err)
	}
	bodyTunnel := initial.Tunnel
	if err := bodyTunnel.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("initial handshake: %v", err)
	}

	// Record initial identity
	initialCfg := bodyTunnel.Config()
	privHash := sha256.Sum256(initialCfg.PrivateKey[:])

	// ── Start supervisor ──
	epoch := body.NewRecoveryEpoch(bodyTunnel)
	supCfg := body.SupervisorConfig{
		PathSelector: pathCfg,
		LossObserver: body.LossObserverConfig{
			WGLivenessTimeout: 6 * time.Second,
			PollInterval:      500 * time.Millisecond,
		},
		RecoverTimeout: 20 * time.Second,
		SharedEpoch:    epoch,
	}

	sup, err := body.StartPathSupervisor(ctx, bodyTunnel, supCfg, log)
	if err != nil {
		t.Fatalf("StartPathSupervisor: %v", err)
	}
	defer func() {
		sup.Stop()
		select {
		case <-sup.Done():
		case <-time.After(3 * time.Second):
			t.Error("supervisor did not shut down within 3s")
		}
	}()

	// ── Kill Core A → trigger loss → first recovery fails (route closed) ──
	t.Log("killing Core A...")
	coreDevA.Close()

	// Wait for first recovery to start (IsRecovering = true)
	t.Log("waiting for first recovery attempt to start...")
	deadline := time.Now().Add(15 * time.Second)
	for !epoch.IsRecovering() {
		if time.Now().After(deadline) {
			t.Fatal("recovery did not start within deadline")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Wait for first recovery to fail (route not registered on relay)
	t.Log("waiting for first recovery to fail...")
	deadline = time.Now().Add(30 * time.Second)
	for epoch.IsRecovering() {
		if time.Now().After(deadline) {
			t.Fatal("first recovery did not fail within deadline")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("first recovery failed — entering backoff window")

	// ── During backoff: open route + start Core B ──
	t.Log("opening relay route and starting Core B...")
	_, routeErr := client.OpenRoute(svcCtx, routeID, relay.RouteCredentials{Token: credential})
	if routeErr != nil {
		t.Fatalf("OpenRoute: %v", routeErr)
	}
	regID, ok := svc.Registry().RouteRegistration(routeID)
	if !ok {
		t.Fatal("route registration not found after OpenRoute")
	}
	svc.Registry().SetRouteCredentials(regID, routeID, relay.RouteCredentials{Token: credential})

	tr := relay.NewRelayTransport(client)
	tr.Open(0)
	coreDevB, coreNetB := startWgDevice(t, tr, coreKeys, coreOverlay, bodyKeys.pubHex, "relay:1", "fd00::10/128")
	defer coreDevB.Close()
	_ = coreNetB

	// ── Wait for supervisor to retry and succeed ──
	t.Log("waiting for recovery to succeed via relay WSS...")
	deadline = time.Now().Add(20 * time.Second)
	var recoveredTunnel *body.BodyTunnel
	for {
		active := epoch.Active()
		if active != bodyTunnel && active != nil {
			recoveredTunnel = active
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery did not succeed via relay within deadline")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("recovery succeeded via relay WSS")

	// ── Verify identity preserved ──
	t.Log("verifying identity...")
	recoveredCfg := recoveredTunnel.Config()
	recoveredHash := sha256.Sum256(recoveredCfg.PrivateKey[:])
	if privHash != recoveredHash {
		t.Fatal("recovered tunnel private key changed! identity not preserved")
	}

	// ── Verify WG handshake ──
	if err := recoveredTunnel.WaitHandshake(ctx, 10*time.Second); err != nil {
		t.Fatalf("recovered tunnel handshake: %v", err)
	}

	// ── Verify bidirectional traffic ──
	proveBidirectionalTraffic(t, recoveredTunnel.Netstack(), coreNetB, bodyOverlay, coreOverlay, "retry-recovered")
}