//go:build e2e

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Pairing"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	runtime "github.com/Neon-Dolls/neondoll/Core/Runtime"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"
)

// TestM74BodyReconnectAfterCoreReconstruction — M7.4 acceptance test.
//
// Proves that a Body paired with a Core runtime can reconnect via a fresh
// WireGuard handshake after the Core is destroyed and reconstructed from
// durable state, without re-pairing or restarting the Body tunnel.
//
// Acceptance:
//  1. initial tunnel establishment          (pairing + WG handshake)
//  2. Core destruction                      (runtime.Stop, Done)
//  3. Core reconstruction from durable SQL  (Runtime.Reconstruct)
//  4. fresh WG handshake                    (IpcGet timestamp advances)
//  5. overlay connectivity                  (TCP echo via Netstack)
//  6. Doll Link round-trip                  (WS over overlay)
func TestM74BodyReconnectAfterCoreReconstruction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// ─────────────────────────────────────────────────────────────────────
	// Phase 1 — Identity and key generation
	// ─────────────────────────────────────────────────────────────────────

	netID := network.NetworkID("m74-reconnect-test")
	m1Net, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	t.Logf("Core network: net_id=%s peer_id=%s overlay=%s prefix=%s",
		m1Net.NetworkID, m1Net.Core.PeerID,
		m1Net.Core.OverlayAddress, m1Net.Core.OverlayPrefix)

	bMeta := body.BodyMetadata{
		Implementation: "neondoll-e2e-m74",
		Platform:       "linux",
		Arch:           "amd64",
	}
	bID, err := body.NewIdentityState("M74TestBody", bMeta)
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	bKP, err := body.GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	t.Logf("Body identity: body_id=%s wg_pub=%s",
		bID.Identity.BodyID, bKP.PublicKeyBase64())

	// ─────────────────────────────────────────────────────────────────────
	// Phase 2 — Invitation + pairing (same as production pairing flow)
	// ─────────────────────────────────────────────────────────────────────

	invSvc := invitation.NewService(invitation.NewMemoryStore(), stubClock{})

	dbPath := filepath.Join(t.TempDir(), "m74.db")
	ns, err := persistence.NewNetworkStore(dbPath)
	if err != nil {
		t.Fatalf("NewNetworkStore: %v", err)
	}
	defer func() {
		if c, ok := ns.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}()

	if err := ns.SaveNetwork(ctx, m1Net); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	endpoints := dollnetwork.Endpoints{{URL: "relay://core-m74:51820"}}
	pairSvc := pairing.NewService(
		m1Net, ns, invSvc,
		pairing.AllowAuthorizer{},
		stubClock{},
		endpoints,
	)

	ci, err := invSvc.Create(ctx, invitation.CreateParams{
		Lifetime:           invitation.DefaultLifetime,
		BootstrapEndpoints: endpoints,
	})
	if err != nil {
		t.Fatalf("Create invitation: %v", err)
	}
	inv := &dollnetwork.Invitation{
		Version:            dollnetwork.ProtocolVersion,
		InvitationID:       ci.ID,
		InvitationSecret:   ci.Secret,
		ExpiresAt:          ci.ExpiresAt.Format(time.RFC3339),
		BootstrapEndpoints: ci.BootstrapEndpoints,
	}

	preq := body.BuildPairRequest(
		inv.InvitationID, inv.InvitationSecret, bID, bKP,
	)
	presp, errResp := pairSvc.HandlePairing(ctx, &preq)
	if errResp != nil {
		t.Fatalf("HandlePairing error response: %+v", errResp)
	}
	if presp == nil {
		t.Fatal("HandlePairing returned nil")
	}
	t.Logf("Pairing: body_peer_id=%s body_addr=%v core_peer_id=%s",
		presp.BodyPeerID, presp.BodyAddresses, presp.CorePeerID)

	// The pairing service has created and saved the membership.
	mems, err := ns.ListMemberships(ctx)
	if err != nil {
		t.Fatalf("ListMemberships after pairing: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1 membership after pairing, got %d", len(mems))
	}
	bodyMembership := mems[0]
	t.Logf("Membership loaded: peer_id=%s body_id=%s status=%v",
		bodyMembership.PeerID, bodyMembership.BodyID, bodyMembership.Status)

	// ─────────────────────────────────────────────────────────────────────
	// Phase 3 — Port assignment + initial Core runtime
	// ─────────────────────────────────────────────────────────────────────

	corePort := pickPort(t)
	bodyPort := pickPort(t)
	if corePort == bodyPort {
		bodyPort = pickPort(t) // very unlikely but cheap to guard
	}

	// Start the INITIAL Core runtime via the production Reconstruct path.
	rt1, err := runtime.Reconstruct(
		ctx, m1Net, ns, log,
		runtime.WithListenPort(corePort),
	)
	if err != nil {
		t.Fatalf("Runtime.Reconstruct (initial): %v", err)
	}
	t.Logf("Initial Core runtime started on port %d", corePort)

	coreRunning := true
	defer func() {
		if coreRunning {
			_ = rt1.Stop()
		}
	}()

	// ─────────────────────────────────────────────────────────────────────
	// Phase 4 — Body tunnel + initial WireGuard handshake
	// ─────────────────────────────────────────────────────────────────────

	corePubBytes, err := dollnetwork.DecodeWgPublicKey(presp.CoreWGPublicKey)
	if err != nil {
		t.Fatalf("DecodeWgPublicKey: %v", err)
	}
	var corePub [32]byte
	copy(corePub[:], corePubBytes)

	bodyOverlay := netip.MustParseAddr(presp.BodyAddresses[0])

	var privBytes [32]byte
	pk := bKP.PrivateKeyBytes()
	if len(pk) != 32 {
		t.Fatalf("private key length %d != 32", len(pk))
	}
	copy(privBytes[:], pk)

	bTun := body.NewBodyTunnel(
		body.BodyTunnelConfig{
			PrivateKey:     privBytes,
			CorePublicKey:  corePub,
			CoreEndpoint:   fmt.Sprintf("127.0.0.1:%d", corePort),
			OverlayAddress: bodyOverlay,
			OverlayPrefix:  m1Net.Core.OverlayPrefix,
			ListenPort:     bodyPort,
			MTU:            1280,
		},
		log,
	)
	if err := bTun.Start(ctx); err != nil {
		t.Fatalf("Body tunnel Start: %v", err)
	}
	defer bTun.Stop()
	t.Log("Body tunnel started")

	// Wait for initial WireGuard handshake
	if err := bTun.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("WaitHandshake (initial): %v", err)
	}
	t.Log("Initial WireGuard handshake established")

	// Capture the handshake timestamp FOR the initial tunnel
	ipcOut, ipcErr := bTun.IpcGet()
	if ipcErr != nil {
		t.Fatalf("IpcGet (initial): %v", ipcErr)
	}
	t1, err := parseHandshakeTimestamp(ipcOut)
	if err != nil {
		t.Fatalf("parsing initial handshake timestamp: %v", err)
	}
	if t1.IsZero() {
		t.Fatal("initial handshake timestamp is zero")
	}
	t.Logf("Initial handshake timestamp: %v", t1)

	// Prove initial overlay connectivity
	proveDirectOverlayEcho(
		t, ctx, rt1.Tunnel().Netstack(), bTun.Netstack(),
		m1Net.Core.OverlayAddress, "initial-connectivity",
	)

	// ─────────────────────────────────────────────────────────────────────
	// Phase 5 — Destroy the Core runtime
	// ─────────────────────────────────────────────────────────────────────

	t.Log("Destroying Core runtime ...")
	if err := rt1.Stop(); err != nil {
		t.Fatalf("Runtime stop: %v", err)
	}
	coreRunning = false
	select {
	case <-rt1.Done():
		t.Log("Core runtime fully stopped")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for Runtime.Done")
	}
	t.Log("Core runtime destroyed — Body tunnel stays alive")

	// ─────────────────────────────────────────────────────────────────────
	// Phase 6 — Reconstruct Core from durable state
	// ─────────────────────────────────────────────────────────────────────

	reconNet, err := ns.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("LoadNetwork: %v", err)
	}
	t.Logf(
		"Network reloaded: net_id=%s core=%s core_overlay=%s",
		reconNet.NetworkID, reconNet.Core.PeerID,
		reconNet.Core.OverlayAddress,
	)

	rt2, err := runtime.Reconstruct(
		ctx, reconNet, ns, log,
		runtime.WithListenPort(corePort),
	)
	if err != nil {
		t.Fatalf("Runtime.Reconstruct (restart): %v", err)
	}
	defer rt2.Stop()
	t.Log("Core runtime reconstructed from durable SQLite state")

	// ─────────────────────────────────────────────────────────────────────
	// Phase 7 — Prove a FRESH WireGuard handshake
	//
	// The Body WG tunnel was NEVER restarted.  The reconstructed Core has
	// no prior handshake state (new WireGuard device), so when the Body's
	// persistent keepalive (25 s) arrives, the Core sees an unknown SPI
	// and initiates a new handshake.  We detect this by polling IpcGet
	// and checking the handshake timestamp advances.
	// ─────────────────────────────────────────────────────────────────────

	t.Logf("Waiting for a FRESH handshake (t1=%v) ...", t1)
	// The Body keeps its pre-restart WireGuard session until the normal rekey
	// interval expires. Waiting for that protocol-driven rekey proves recovery
	// without rewriting either peer's endpoint.
	deadline := time.Now().Add(30 * time.Second)
	var t2 time.Time
	for {
		ipcOut, ipcErr := bTun.IpcGet()
		if ipcErr == nil {
			ts, err := parseHandshakeTimestamp(ipcOut)
			if err == nil && ts.After(t1) {
				t2 = ts
				break
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if t2.IsZero() {
		t.Fatalf(
			"timed out waiting for a FRESH handshake after "+
				"reconstruction (t1=%v, deadline=%v)", t1, deadline,
		)
	}
	t.Logf("Fresh WireGuard handshake: t1=%v -> t2=%v", t1, t2)

	// ─────────────────────────────────────────────────────────────────────
	// Phase 8 — Prove overlay connectivity (TCP echo)
	// ─────────────────────────────────────────────────────────────────────

	proveDirectOverlayEcho(
		t, ctx, rt2.Tunnel().Netstack(), bTun.Netstack(),
		reconNet.Core.OverlayAddress, "reconnect-ok",
	)

	// ─────────────────────────────────────────────────────────────────────
	// Phase 9 — Prove Doll Link (WebSocket round-trip over overlay)
	// ─────────────────────────────────────────────────────────────────────

	proveWSOverlay(t, ctx, rt2.Tunnel().Netstack(), bTun.Netstack(),
		reconNet.Core.OverlayAddress, log)

	t.Log("PASS: M7.4 — Body reconnection after Core reconstruction verified")
}
