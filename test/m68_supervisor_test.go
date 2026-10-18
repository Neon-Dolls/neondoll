//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

// TestM68_InitialWSS_DiscoveryAndMigration verifies that when the Body
// initially connects via WSS/relay (because the direct path is unavailable),
// the Path Supervisor automatically discovers and migrates to the direct
// path once it becomes available.
//
// Scenario:
//  1. Core A (direct) NOT available — WG device stopped
//  2. Body selects WSS fallback (relay → Core behind relay)
//  3. Supervisor starts with discovery enabled on the fallback path
//  4. Core A comes online + probe responder available
//  5. Discovery detects the direct path and migrates
//  6. Verify direct path handshake, identity, and epoch
func TestM68_InitialWSS_DiscoveryAndMigration(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	// ── Keys and identities ──
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	bodyPriv := hexToKey(t, bodyKeys.privHex)
	corePub := hexToKey(t, coreKeys.pubHex)

	const routeID = relay.RouteID(44)
	const credential = "m68-init-wss-cred"
	const token = "m68-init-wss-token"

	bodyOverlay := netip.MustParseAddr("fd00::2")
	coreOverlay := netip.MustParseAddr("fd00::1")
	overlayPrefix := netip.MustParsePrefix("fd00::/112")

	// ── 1. WSS relay infrastructure ──
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

	svcCtx, svcCancel := context.WithCancel(ctx)
	defer svcCancel()
	if err := svc.Start(svcCtx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}

	cs := relay.NewControlServer(svc, relayAddr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// ── 2. Probe responder (initially UNAVAILABLE) ──
	responder := startCoreProbeResponder(t, routeID, credential)
	probeAddr := fmt.Sprintf("127.0.0.1:%d", responder.port)
	responder.SetAvailable(false)
	t.Logf("probe responder on %s (unavailable)", probeAddr)

	// ── 3. Core behind relay (always available, WSS fallback target) ──
	clientCfg := relay.DefaultClientConfig()
	clientCfg.RelayURL = "ws://" + relayAddr + "/relay"
	clientCfg.RegistrationToken = token
	clientCfg.HandshakeTimeout = 5 * time.Second
	clientCfg.ReadTimeout = 15 * time.Second
	clientCfg.PingInterval = 5 * time.Second

	coreClient := relay.NewControlClient(clientCfg)
	if err := coreClient.Start(svcCtx); err != nil {
		t.Fatalf("control client start: %v", err)
	}
	waitClientConnected(t, coreClient, "core-behind-relay")

	// Open the route so WSS can reach this Core.
	routeOpened, err := coreClient.OpenRoute(svcCtx, routeID, relay.RouteCredentials{
		Token: credential,
	})
	if err != nil {
		t.Fatalf("OpenRoute: %v", err)
	}
	_ = routeOpened
	t.Log("core route opened for WSS fallback")

	// Override route credentials to match what the body will send.
	regID, ok := svc.Registry().RouteRegistration(routeID)
	if !ok {
		t.Fatal("route registration not found after OpenRoute")
	}
	svc.Registry().SetRouteCredentials(regID, routeID, relay.RouteCredentials{Token: credential})

	// Create relay transport + WG device for the Core behind the relay.
	tr := relay.NewRelayTransport(coreClient)
	defer tr.Close()
	_, _ = startWgDevice(t, tr, coreKeys, coreOverlay, bodyKeys.pubHex, "relay:1", "fd00::2/128")

	// ── 4. Allocate a free UDP port for Core A's direct WG device ──
	// Start a listener to find a free port, then close it. The WG device
	// will start on this port later.
	freePort := allocateFreeUDPPort(t)
	directEndpoint := fmt.Sprintf("127.0.0.1:%d", freePort)
	t.Logf("direct endpoint reserved: %s (not yet listening)", directEndpoint)

	// ── 5. Path selector config — direct endpoint available but NOT listening ──
	pathCfg := body.PathSelectorConfig{
		PrivateKey:        bodyPriv,
		CorePublicKey:     corePub,
		OverlayAddress:    bodyOverlay,
		OverlayPrefix:     overlayPrefix,
		DirectEndpoint:    directEndpoint,
		RelayWSSURL:       relayAddr,
		RouteID:           routeID,
		RouteCredential:   credential,
		PerAttemptTimeout: 5 * time.Second,
	}

	// ── 6. Select initial path — should fall back to WSS ──
	initialResult := body.SelectInitialPath(ctx, pathCfg, log)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	initialTunnel := initialResult.Tunnel

	if initialTunnel.Config().Bind == nil {
		t.Fatal("expected WSS fallback tunnel (Bind should be non-nil)")
	}
	t.Log("initial path: WSS (fallback)")

	if err := initialTunnel.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("WSS tunnel handshake: %v", err)
	}
	t.Log("WSS tunnel handshake established")

	// ── 7. Shared epoch + supervisor with discovery enabled ──
	epoch := body.NewRecoveryEpoch(initialTunnel)

	supCfg := body.SupervisorConfig{
		PathSelector: pathCfg,
		LossObserver: body.LossObserverConfig{
			WGLivenessTimeout: 4 * time.Second,
			PollInterval:      500 * time.Millisecond,
		},
		RecoverTimeout: 10 * time.Second,
		SharedEpoch:    epoch,
		Discovery: body.DiscoveryConfig{
			Enabled:          true,
			ProbeTarget:      probeAddr,
			MinInterval:      1 * time.Second,
			ProbeTimeout:     3 * time.Second,
			HandshakeTimeout: 10 * time.Second,
		},
	}

	handle, err := body.StartPathSupervisor(ctx, initialTunnel, supCfg, log)
	if err != nil {
		t.Fatalf("StartPathSupervisor: %v", err)
	}
	defer handle.Stop()
	t.Log("supervisor started")

	// ── 8. Let discovery probe fail for a few cycles (responder unavailable) ──
	time.Sleep(4 * time.Second)

	// Verify epoch is still on WSS — failed probes must not disturb
	// the working fallback path.
	assertActiveTunnel(t, epoch, initialTunnel, "still on WSS after failed probes")
	t.Log("still on WSS after failed probes — correct")

	// ── 9. Start Core A (direct) WG device and enable probe responder ──
	responder.SetAvailable(true)
	t.Log("probe responder available")

	coreDevA := startCoreDeviceOnPort(t, uint16(freePort), coreKeys, bodyKeys.pubHex, coreOverlay)
	defer coreDevA.Close()
	t.Log("Core A direct WG device started")

	// ── 10. Wait for discovery to migrate to direct path ──
	t.Log("waiting for auto-discovery + migration to direct...")
	var directTunnel *body.BodyTunnel
	migrateStart := time.Now()
	migrateDeadline := time.Now().Add(60 * time.Second)
	for {
		active := epoch.Active()
		if active != nil && active != initialTunnel {
			cfg := active.Config()
			if cfg.Bind == nil {
				directTunnel = active
				break
			}
		}
		if time.Now().After(migrateDeadline) {
			t.Fatal("auto-discovery + migration did not complete within deadline")
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("migrated to direct path in %.1fs", time.Since(migrateStart).Seconds())

	// ── 11. Verify migrated tunnel ──
	if directTunnel == nil {
		t.Fatal("expected non-nil direct tunnel after migration")
	}
	if err := directTunnel.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("direct tunnel handshake: %v", err)
	}
	t.Log("direct path handshake established")

	// Verify Body identity is preserved across migration.
	if directTunnel.Config().PrivateKey != bodyPriv {
		t.Fatal("body private key changed after migration")
	}
	t.Log("body identity preserved across migration")
}

// allocateFreeUDPPort finds a free UDP port by opening a listener.
func allocateFreeUDPPort(t *testing.T) int {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve UDP addr: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}
