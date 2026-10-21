//go:build e2e

package integration

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	corebody "github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Pairing"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	runtime "github.com/Neon-Dolls/neondoll/Core/Runtime"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
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
//  6. authorized remote execution           (Guard through Body dispatcher)
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
	// Drive endpoint rediscovery from authenticated Body traffic rather than
	// waiting on the production 25-second keepalive boundary. This changes no
	// identity or endpoint and makes reconstruction recovery deterministic.
	if err := bTun.IpcSet(fmt.Sprintf("public_key=%s\npersistent_keepalive_interval=1\n", hex.EncodeToString(corePub[:]))); err != nil {
		t.Fatalf("set migration-test keepalive: %v", err)
	}

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

	// Prove the complete authorized execution path before migration. The
	// explicit credential callback is a trusted test fixture; membership is
	// verified independently and is never used as authentication evidence.
	blockStarted := make(chan struct{})
	blockCancelled := make(chan struct{})
	var blockStartOnce sync.Once
	var blockCancelOnce sync.Once
	dispatcher, err := execution.NewDispatcher(string(bID.Identity.BodyID), execution.LocalPermissionFunc(
		func(context.Context, execution.Request) bool { return true },
	), execution.CapabilityExecutorFunc(func(capCtx context.Context, capability, operation string, arguments map[string]any) (map[string]any, error) {
		if capability != "test.echo" {
			return nil, execution.ErrUnsupported
		}
		if operation == "block" {
			blockStartOnce.Do(func() { close(blockStarted) })
			<-capCtx.Done()
			blockCancelOnce.Do(func() { close(blockCancelled) })
			return nil, capCtx.Err()
		}
		return map[string]any{"message": arguments["message"]}, nil
	}))
	if err != nil {
		t.Fatalf("execution dispatcher: %v", err)
	}
	link1 := startM7ExecutionLink(t, rt1.Tunnel().Netstack(), bTun.Netstack(), m1Net.Core.OverlayAddress, string(bID.Identity.BodyID), dispatcher, log)
	link1Running := true
	defer func() {
		if link1Running {
			_ = link1.stop(5 * time.Second)
		}
	}()
	guard1 := newM7RemoteGuard(t, string(bID.Identity.BodyID), link1.server)
	initialExecution := guard1.ExecuteContext(ctx, m7ExecutionRequest(string(bID.Identity.BodyID), "m7-before-migration", "run"))
	if initialExecution.Status != corebody.StatusSuccess || initialExecution.ExecutionID != "m7-before-migration" || initialExecution.Output != `{"message":"migration-ok"}` {
		t.Fatalf("initial remote execution = %#v", initialExecution)
	}
	t.Log("Initial authorized remote execution succeeded")

	// ─────────────────────────────────────────────────────────────────────
	// Phase 5 — Destroy the Core runtime
	// ─────────────────────────────────────────────────────────────────────

	// Leave one execution pending while Core A shuts down. Its stale session
	// must release the Core waiter and cancel Body-side work.
	pendingResult := make(chan corebody.ExecutionResult, 1)
	go func() {
		pendingResult <- guard1.ExecuteContext(context.Background(), m7ExecutionRequest(string(bID.Identity.BodyID), "m7-stale-pending", "block"))
	}()
	select {
	case <-blockStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("pending Body execution did not start")
	}

	t.Log("Destroying Core runtime ...")
	if err := link1.stop(5 * time.Second); err != nil {
		t.Fatalf("Core A Doll Link shutdown: %v", err)
	}
	link1Running = false
	select {
	case result := <-pendingResult:
		if result.Status != corebody.StatusFailed || result.ExecutionID != "m7-stale-pending" {
			t.Fatalf("stale pending execution = %#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Core A shutdown did not release pending execution")
	}
	select {
	case <-blockCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Core A shutdown did not cancel Body-side execution")
	}
	_, staleErr := link1.server.Execute(ctx, string(bID.Identity.BodyID), "doll-m7", execution.RequestPayload{
		ExecutionID: "stale-session", Capability: "test.echo", Operation: "run", Arguments: map[string]any{},
	})
	if !errors.Is(staleErr, ws.ErrNoAuthenticatedSession) {
		t.Fatalf("stale Core A session remained eligible: %v", staleErr)
	}
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
	if reconNet.NetworkID != m1Net.NetworkID || reconNet.Core.PeerID != m1Net.Core.PeerID || reconNet.Core.PublicKey != m1Net.Core.PublicKey {
		t.Fatal("reconstructed Core identity differs from persisted Core A identity")
	}
	reconMembership := reconNet.GetMembership(bodyMembership.PeerID)
	if reconMembership == nil {
		t.Fatalf("reconstructed membership %q missing", bodyMembership.PeerID)
	}
	keysMatch := reconMembership.WireGuardPublicKey != nil && bodyMembership.WireGuardPublicKey != nil &&
		*reconMembership.WireGuardPublicKey == *bodyMembership.WireGuardPublicKey
	if reconMembership.BodyID != bodyMembership.BodyID || reconMembership.Status != network.MembershipActive || !keysMatch {
		t.Fatalf("reconstructed membership differs: got %#v, want identity/status/key from %#v", reconMembership, bodyMembership)
	}
	if reconMembership.Endpoint != "" {
		t.Fatalf("reconstructed membership retained stale endpoint %q", reconMembership.Endpoint)
	}

	rt2, err := runtime.Reconstruct(
		ctx, reconNet, ns, log,
		runtime.WithListenPort(corePort),
	)
	if err != nil {
		t.Fatalf("Runtime.Reconstruct (restart): %v", err)
	}
	defer rt2.Stop()
	t.Log("Core runtime reconstructed from durable SQLite state")
	// The closed Doll Link tells the Body that the remote Core session is gone.
	// Reset only WireGuard's transient peer session so it initiates a fresh
	// handshake using the same configured endpoint; no endpoint is rewritten.
	if err := bTun.Reconnect(); err != nil {
		t.Fatalf("Body tunnel reconnect: %v", err)
	}

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
	if err := bTun.WaitHandshake(ctx, 15*time.Second); err != nil {
		t.Fatalf("fresh handshake after reconstruction: %v", err)
	}
	ipcOut, err = bTun.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet (reconnected): %v", err)
	}
	t2, err := parseHandshakeTimestamp(ipcOut)
	if err != nil || !t2.After(t1) {
		t.Fatalf("fresh handshake timestamp = %v, want after %v (parse error: %v)", t2, t1, err)
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
	// Phase 9 — Reconnect Doll Link and execute through the reconstructed Core
	// ─────────────────────────────────────────────────────────────────────

	link2 := startM7ExecutionLink(t, rt2.Tunnel().Netstack(), bTun.Netstack(), reconNet.Core.OverlayAddress, string(bID.Identity.BodyID), dispatcher, log)
	defer func() {
		if err := link2.stop(5 * time.Second); err != nil {
			t.Errorf("Core B Doll Link shutdown: %v", err)
		}
	}()
	guard2 := newM7RemoteGuard(t, string(bID.Identity.BodyID), link2.server)
	migratedExecution := guard2.ExecuteContext(ctx, m7ExecutionRequest(string(bID.Identity.BodyID), "m7-after-migration", "run"))
	if migratedExecution.Status != corebody.StatusSuccess || migratedExecution.ExecutionID != "m7-after-migration" || migratedExecution.Output != `{"message":"migration-ok"}` {
		t.Fatalf("post-migration remote execution = %#v", migratedExecution)
	}
	if link1.server.ConnCount() != 0 {
		t.Fatalf("Core A retained %d stale Doll Link connections", link1.server.ConnCount())
	}
	t.Log("Body reconnected without re-pairing and authorized remote execution succeeded")

	t.Log("PASS: M7 — Body reconnection and remote execution after Core migration verified")
}
