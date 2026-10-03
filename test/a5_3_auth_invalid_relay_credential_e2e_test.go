//go:build e2e

// A5.3 E2E test — Invalid Relay Credential Sanity Proof.
//
// Proves that the real M4 Relay topology cannot become usable when the
// Core presents an invalid Relay registration credential:
//
//  1. Registration is rejected through the real ControlServer.
//  2. No active Relay registration exists.
//  3. No Relay route is created/restored.
//  4. No registration-scoped UDP endpoint/runtime topology becomes available.
//  5. The failed attempt does not disturb existing persistent Body/Doll Network
//     identity or pairing state (which is present in the fixture).
//
// This is NOT an adversarial/property-testing suite — that is A4's domain.
// This is a focused sanity proof that the credential-enforcement path works.
// No reconnect/reconstruction scenarios are added (A5.2 proves those).
// No deliberately timing-out Doll Link attempt is added — absence of
// registration/route/runtime topology already proves the path cannot exist.
package integration

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	network "github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Pairing"
	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"
)

func TestA53_InvalidRelayCredential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	log := discardLogger()

	tp := &m46Topology{}

	// ── 1. M1: Identity creation ──
	// Creates Core + Body identity state that proves we have "existing
	// persistent identity" for the disturbance check (requirement 5).
	tp.netID = network.NetworkID("e2e-a53")
	m1Net, err := network.NewNetwork(tp.netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	alloc := network.NewIPv6Allocator(tp.netID)
	coreAddr, err := alloc.CoreAddress()
	if err != nil {
		t.Fatalf("CoreAddress: %v", err)
	}
	overlayPrefix := alloc.Prefix()
	m1Net.Core.OverlayAddress = coreAddr
	m1Net.Core.OverlayPrefix = overlayPrefix
	tp.m1Net = m1Net
	t.Logf("Core network: net_id=%s peer_id=%s overlay=%s prefix=%s",
		m1Net.NetworkID, m1Net.Core.PeerID, coreAddr, overlayPrefix)

	bMeta := body.BodyMetadata{
		Implementation: "neondoll-e2e",
		Platform:       "linux",
		Arch:           "amd64",
	}
	bID, err := body.NewIdentityState("M47TestBody-a53", bMeta)
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	bKP, err := body.GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	tp.bodyID, tp.bodyKP = bID, bKP
	t.Logf("Body WG pubkey: %s", bKP.PublicKeyBase64())

	// ── 2. M2: Invitation + Pairing ──
	tp.invStore = invitation.NewMemoryStore()
	tp.invSvc = invitation.NewService(tp.invStore, stubClock{})

	tp.memStore = newMemNetStore()
	if err := tp.memStore.SaveNetwork(ctx, m1Net); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	endpoints := dollnetwork.Endpoints{{URL: "relay://core-a53:51820"}}
	tp.pairSvc = pairing.NewService(m1Net, tp.memStore, tp.invSvc,
		pairing.AllowAuthorizer{}, stubClock{}, endpoints)

	ci, err := tp.invSvc.Create(ctx, invitation.CreateParams{
		Lifetime:           invitation.DefaultLifetime,
		BootstrapEndpoints: endpoints,
	})
	if err != nil {
		t.Fatalf("Create invitation: %v", err)
	}
	tp.invID = ci.ID
	inv := &dollnetwork.Invitation{
		Version:            dollnetwork.ProtocolVersion,
		InvitationID:       ci.ID,
		InvitationSecret:   ci.Secret,
		ExpiresAt:          ci.ExpiresAt.Format(time.RFC3339),
		BootstrapEndpoints: ci.BootstrapEndpoints,
	}

	preq := body.BuildPairRequest(inv.InvitationID, inv.InvitationSecret, bID, bKP)
	presp, errResp := tp.pairSvc.HandlePairing(ctx, &preq)
	if errResp != nil {
		t.Fatalf("HandlePairing error: %+v", errResp)
	}
	if presp == nil {
		t.Fatal("HandlePairing returned nil response")
	}
	t.Logf("Pairing: body_peer_id=%s body_addr=%v core_wg_key=%s",
		presp.BodyPeerID, presp.BodyAddresses, presp.CoreWGPublicKey[:16]+"...")

	if got, want := presp.NetworkID, string(m1Net.NetworkID); got != want {
		t.Fatalf("network_id = %q, want %q", got, want)
	}

	bodyOverlayAddr := netip.MustParseAddr(presp.BodyAddresses[0])
	coreOverlayAddr := netip.MustParseAddr(presp.CoreAddresses[0])
	t.Logf("Paired: coreOverlay=%s bodyOverlay=%s", coreOverlayAddr, bodyOverlayAddr)

	// ── 3. Relay — Service with valid credential verifier ──
	relayPortSvc := pickPort(t)
	relayPortWS := pickPort(t)

	relayCfg := relay.DefaultServiceConfig()
	// SHA-256("test-reg-token") — the valid credential verifier.
	relayCfg.Credentials = []string{"d89e45dd5a9a2557aa7e36afca04a58e0c47eddd613992f13896c60bef14ac29"}
	relayCfg.UDP.ListenAddress = "127.0.0.1"
	relayCfg.UDP.PortMin = relayPortSvc
	relayCfg.UDP.PortMax = relayPortSvc + 2

	relaySvc, err := relay.NewService(relayCfg, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("relay.NewService: %v", err)
	}
	tp.relayCfg = relayCfg
	tp.relaySvc = relaySvc

	go func() {
		_ = relaySvc.Start(ctx)
	}()
	defer func() {
		_ = relaySvc.Shutdown(context.Background())
	}()

	// ── 4. ControlServer on the relay ──
	relayURL := fmt.Sprintf("ws://127.0.0.1:%d/relay", relayPortWS)
	tp.relayURL = relayURL
	tp.cs, tp.csCancel = startControlServer(t, ctx, log, relaySvc,
		fmt.Sprintf("127.0.0.1:%d", relayPortWS))
	defer tp.csCancel()
	t.Logf("Relay+ControlServer ready: UDP=%d WS=%d", relayPortSvc, relayPortWS)

	// ── 5. Capture identity state BEFORE the failed registration attempt ──
	before := captureIdentityState(t, ctx, tp)
	t.Log("Identity state captured before invalid-credential attempt")

	// ── 6. ControlClient with INVALID RegistrationToken ──
	clientCfg := relay.ClientConfig{
		RelayURL:          relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "wrong-token", // deliberately NOT "test-reg-token"
	}
	ctrlClient := relay.NewControlClient(clientCfg)
	t.Logf("ControlClient configured with invalid token (want 'test-reg-token', got 'wrong-token')")

	// ── 7. Prove registration is rejected through the real ControlServer ──
	err = ctrlClient.Start(ctx)
	if err == nil {
		t.Fatal("ControlClient.Start must fail with invalid RegistrationToken")
	}
	t.Logf("Registration rejected as expected: %v", err)

	// NOTE: Shutdown() is intentionally NOT called here. When Start()
	// fails (registration rejected), the reconnectLoop goroutine was never
	// spawned, so Shutdown() would block forever waiting on the done channel.
	// The WebSocket connection was already closed by connect() on error,
	// and the client context will be cleaned up when the test context cancels.

	// ── 8. Prove no active Relay registration exists ──
	if n := tp.cs.ActiveRegistrations(); n != 0 {
		t.Fatalf("ControlServer.ActiveRegistrations = %d, want 0", n)
	}
	if n := relaySvc.Registry().RegistrationCount(); n != 0 {
		t.Fatalf("Registry.RegistrationCount = %d, want 0", n)
	}
	t.Log("ActiveRegistrations = 0, RegistrationCount = 0 — no registration created")

	// ── 9. Prove no Relay route was created/restored ──
	if n := relaySvc.Registry().RouteCount(); n != 0 {
		t.Fatalf("Registry.RouteCount = %d, want 0", n)
	}
	if _, ok := relaySvc.Registry().Route(relay.RouteID(1)); ok {
		t.Fatal("Route 1 exists in registry — route created without valid registration")
	}
	t.Log("RouteCount = 0, Route(1) absent — no route created/restored")

	// ── 10. Prove no registration-scoped UDP endpoint/runtime topology ──
	if _, ok := relaySvc.UDP().LastSeen(relay.RouteID(1)); ok {
		t.Fatal("UDP.LastSeen for route 1 returned an endpoint — allocated without valid registration")
	}
	if n := relaySvc.UDP().ActiveRoutes(); n != 0 {
		t.Fatalf("UDP.ActiveRoutes = %d, want 0", n)
	}
	t.Log("No UDP endpoint for route 1, ActiveRoutes = 0 — no runtime topology")

	// ── 11. Prove client has no connected state ──
	if ctrlClient.IsConnected() {
		t.Fatal("ControlClient.IsConnected = true after failed registration")
	}
	if rid := ctrlClient.RelayID(); rid != "" {
		t.Fatalf("ControlClient.RelayID = %q, want empty after failed registration", rid)
	}
	t.Log("ControlClient: disconnected, RelayID empty — no identity received from Relay")

	// ── 12. Prove identity invariants unchanged after failed attempt ──
	after := captureIdentityState(t, ctx, tp)
	assertIdentityUnchanged(t, "after invalid-credential attempt", before, after)

	if !after.invConsumed {
		t.Fatal("invitation no longer consumed — re-pairing window opened")
	}
	t.Log("Identity invariants preserved: membership, PeerIDs, WG keys, overlays, invitation consumption unchanged")

	t.Log("=== A5.3 Invalid Relay Credential Sanity: ALL PASS ===")
}
