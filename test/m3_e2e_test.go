//go:build e2e

// Package integration contains real-network E2E tests for the full
// M1 → M2 → M3 path of the NeonDoll stack.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Pairing"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	wireguard "github.com/Neon-Dolls/neondoll/Core/WireGuard"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"
	"io"
)

// ── In-memory network store ──────────────────────────────────────────────────

// memNetStore implements network.NetworkStore in-memory for testing.
type memNetStore struct {
	mu   sync.Mutex
	net  *network.Network
	mems map[network.PeerID]*network.Membership
}

func newMemNetStore() *memNetStore {
	return &memNetStore{mems: make(map[network.PeerID]*network.Membership)}
}

func (s *memNetStore) SaveNetwork(_ context.Context, n *network.Network) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.net = n
	return nil
}

func (s *memNetStore) LoadNetwork(_ context.Context) (*network.Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.net == nil {
		return nil, nil
	}
	return s.net, nil
}

func (s *memNetStore) SaveMembership(_ context.Context, m *network.Membership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *m
	s.mems[m.PeerID] = &clone
	return nil
}

func (s *memNetStore) LoadMembership(_ context.Context, peerID network.PeerID) (*network.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.mems[peerID]
	if !ok {
		return nil, nil
	}
	clone := *m
	return &clone, nil
}

func (s *memNetStore) ListMemberships(_ context.Context) ([]*network.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*network.Membership, 0, len(s.mems))
	for _, m := range s.mems {
		cloned := *m
		out = append(out, &cloned)
	}
	return out, nil
}

func (s *memNetStore) DeleteMembership(_ context.Context, peerID network.PeerID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mems, peerID)
	return nil
}

// ── Test helpers ──────────────────────────────────────────────────────────────

// stubClock implements invitation.Clock with wall time for real E2E tests.
type stubClock struct{}

func (stubClock) Now() time.Time { return time.Now() }

// ── ws.Logger adapter ─────────────────────────────────────────────────────────

// wsLogger wraps a *slog.Logger to satisfy ws.Logger interface.
type wsLogger struct {
	inner *slog.Logger
}

func (w *wsLogger) Info(msg string, fields ...map[string]any) {
	w.inner.Info(msg, slog.Any("fields", fields))
}

func (w *wsLogger) Warn(msg string, fields ...map[string]any) {
	w.inner.Warn(msg, slog.Any("fields", fields))
}

func (w *wsLogger) Error(msg string, fields ...map[string]any) {
	w.inner.Error(msg, slog.Any("fields", fields))
}

// ── echoHandler implements websocket.Handler ──────────────────────────────────

type echoHandler struct{}

func (echoHandler) HandleEvent(_ context.Context, event *events.Event) (*events.Event, error) {
	payloadText := ""
	if p, ok := event.Payload.(map[string]any); ok {
		if t, ok := p["text"].(string); ok {
			payloadText = t
		}
	}
	return &events.Event{
		Type:          events.TypeMessage,
		Source:        "core",
		CorrelationID: event.ID,
		Timestamp:     time.Now().UTC(),
		Payload: events.MessagePayload{
			Text: "response_" + payloadText,
		},
	}, nil
}

// ── M3 E2E: real M1 → M2 → M3 path ──────────────────────────────────────────

// TestM3RealPath proves the full NeonDoll stack:
//
//	M1: identity creation (Core network + Body identity)
//	M2: real invitation + pairing protocol
//	M3: WireGuard overlay, TCP echo, real Doll Link events
//
// After Core destruction/reconstruction, identities are unchanged
// and the Body reconnects without re-pairing.
func TestM3RealPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// ── M1: Identity creation ────────────────────────────────────────────

	netID := network.NetworkID("e2e-real-path-test")
	m1Net, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	alloc := network.NewIPv6Allocator(netID)
	coreAddr, err := alloc.CoreAddress()
	if err != nil {
		t.Fatalf("CoreAddress: %v", err)
	}
	overlayPrefix := alloc.Prefix()

	m1Net.Core.OverlayAddress = coreAddr
	m1Net.Core.OverlayPrefix = overlayPrefix

	t.Logf("Core network: net_id=%s peer_id=%s overlay=%s prefix=%s",
		m1Net.NetworkID, m1Net.Core.PeerID, m1Net.Core.OverlayAddress, m1Net.Core.OverlayPrefix)

	// Create Body identity
	bMeta := body.BodyMetadata{
		Implementation: "neondoll-e2e",
		Platform:       "linux",
		Arch:           "amd64",
	}
	bID, err := body.NewIdentityState("E2ETestBody", bMeta)
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	t.Logf("Body identity: body_id=%s name=%s", bID.Identity.BodyID, bID.Identity.Name)

	// Generate Body WG keypair
	bKP, err := body.GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	t.Logf("Body WG pubkey: %s", bKP.PublicKeyBase64())

	// ── M2: Invitation + Pairing ─────────────────────────────────────────

	invStore := invitation.NewMemoryStore()
	invSvc := invitation.NewService(invStore, stubClock{})

	memStore := newMemNetStore()
	if err := memStore.SaveNetwork(ctx, m1Net); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	endpoints := dollnetwork.Endpoints{{URL: "relay://core-e2e:51820"}}
	pairSvc := pairing.NewService(m1Net, memStore, invSvc, pairing.AllowAuthorizer{}, stubClock{}, endpoints)

	// Create invitation
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

	// Build and send PairRequest (the real Body path)
	preq := body.BuildPairRequest(inv.InvitationID, inv.InvitationSecret, bID, bKP)
	presp, errResp := pairSvc.HandlePairing(ctx, &preq)
	if errResp != nil {
		t.Fatalf("HandlePairing error response: %+v", errResp)
	}
	if presp == nil {
		t.Fatal("HandlePairing returned nil response")
	}

	t.Logf("Pairing: body_peer_id=%s body_addr=%v core_peer_id=%s core_addr=%v",
		presp.BodyPeerID, presp.BodyAddresses, presp.CorePeerID, presp.CoreAddresses)

	// Verify pairing response content
	if got, want := presp.NetworkID, string(m1Net.NetworkID); got != want {
		t.Errorf("network_id = %q, want %q", got, want)
	}
	if presp.BodyPeerID == "" {
		t.Error("body_peer_id is empty")
	}
	if len(presp.BodyAddresses) != 1 {
		t.Fatalf("expected 1 body address, got %d", len(presp.BodyAddresses))
	}
	if presp.CorePeerID != string(m1Net.Core.PeerID) {
		t.Errorf("core_peer_id = %q, want %q", presp.CorePeerID, m1Net.Core.PeerID)
	}
	if presp.CoreWGPublicKey == "" {
		t.Error("core_wg_public_key is empty")
	}
	if len(presp.CoreAddresses) != 1 {
		t.Errorf("expected 1 core address, got %d", len(presp.CoreAddresses))
	}

	// Parse addresses and keys from pairing response
	bodyOverlayAddr := netip.MustParseAddr(presp.BodyAddresses[0])
	coreOverlayAddr := netip.MustParseAddr(presp.CoreAddresses[0])

	corePubBytes, err := dollnetwork.DecodeWgPublicKey(presp.CoreWGPublicKey)
	if err != nil {
		t.Fatalf("DecodeWgPublicKey: %v", err)
	}
	var corePub [32]byte
	copy(corePub[:], corePubBytes)

	// ── M3: WireGuard overlay ─────────────────────────────────────────────

	// Pick a free port for Core and Body
	corePort := pickPort(t)
	bodyPort := pickPort(t)

	// Start Core side: create tunnel + Manager with listen port set
	coreTun := wireguard.NewRealTunnel(log)
	coreMgr := wireguard.NewManager(coreTun, memStore, log)
	coreMgr.SetListenPort(corePort)
	if err := coreMgr.Start(ctx, m1Net); err != nil {
		t.Fatalf("Core manager Start: %v", err)
	}
	coreEndpoint := fmt.Sprintf("127.0.0.1:%d", corePort)
	t.Logf("Core WG tunnel + manager started on port %d", corePort)

	// Update the membership with the Body's UDP endpoint so Core knows
	// where to reach the Body for WireGuard handshakes.
	bodyMembership := &network.Membership{
		BodyID:             string(bID.Identity.BodyID),
		PeerID:             network.PeerID(presp.BodyPeerID),
		WireGuardPublicKey: decodeWgPubKey(bKP.PublicKeyBase64()),
		OverlayAddress:     bodyOverlayAddr,
		Endpoint:           fmt.Sprintf("127.0.0.1:%d", bodyPort),
		Status:             network.MembershipActive,
	}
	if err := memStore.SaveMembership(ctx, bodyMembership); err != nil {
		t.Fatalf("SaveMembership with endpoint: %v", err)
	}
	// Refresh peers so Core learns the Body's endpoint
	if err := coreMgr.RefreshPeers(ctx); err != nil {
		t.Fatalf("RefreshPeers: %v", err)
	}
	t.Log("Body endpoint configured in Core membership")

	// Start Body WG tunnel using data from pairing (not manually generated)
	bPrivBytes := bKP.PrivateKeyBytes()
	var bPrivKey [32]byte
	copy(bPrivKey[:], bPrivBytes)
	body.Wipe(bPrivBytes)

	bTun := body.NewBodyTunnel(body.BodyTunnelConfig{
		PrivateKey:     bPrivKey,
		CorePublicKey:  corePub,
		OverlayAddress: bodyOverlayAddr,
		OverlayPrefix:  overlayPrefix,
		CoreEndpoint:   coreEndpoint,
		ListenPort:     bodyPort,
		MTU:            wireguard.DefaultMTU,
	}, log)
	if err := bTun.Start(ctx); err != nil {
		t.Fatalf("Body tunnel Start: %v", err)
	}
	t.Log("Body WG tunnel started")

	// Wait for WireGuard handshake
	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := bTun.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("Body handshake timeout: %v", err)
	}
	t.Log("WireGuard handshake complete")

	// ── Verify Diagnostics ──────────────────────────────────────────────
	diag, err := coreTun.Diagnostics()
	if err != nil {
		t.Fatalf("Core Diagnostics: %v", err)
	}
	if len(diag.Peers) != 1 {
		t.Errorf("expected 1 peer in diagnostics, got %d", len(diag.Peers))
	} else {
		if diag.Peers[0].Endpoint == "" {
			t.Log("Diagnostics: endpoint empty (expected before traffic)")
		}
		t.Logf("Diagnostics: peer endpoint=%s tx=%d rx=%d handshake_pending=%v",
			diag.Peers[0].Endpoint, diag.Peers[0].TxBytes, diag.Peers[0].RxBytes, diag.Peers[0].HandshakePending)
		if !diag.Peers[0].HandshakePending && diag.Peers[0].HandshakeTime == "" {
			t.Errorf("handshake completed but HandshakeTime is empty")
		} else {
			t.Logf("HandshakeTime: %s", diag.Peers[0].HandshakeTime)
		}
	}
	t.Log("Diagnostics: PASS")

	// ── Overlay TCP echo test ───────────────────────────────────────────
	echoLn, err := coreTun.Netstack().ListenTCP(&net.TCPAddr{IP: net.IP(coreOverlayAddr.AsSlice()), Port: 9999})
	if err != nil {
		t.Fatalf("Core netstack ListenTCP: %v", err)
	}

	var (
		echoDone  sync.WaitGroup
		echoBytes string
		echoMu    sync.Mutex
	)
	echoDone.Add(1)
	go func() {
		defer echoDone.Done()
		conn, err := echoLn.Accept()
		if err != nil {
			t.Logf("echo server accept: %v", err)
			return
		}
		defer conn.Close()
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		echoMu.Lock()
		echoBytes = string(buf[:n])
		echoMu.Unlock()
		conn.Write(buf[:n])
	}()

	bNet := bTun.Netstack()
	bConn, err := bNet.Dial("tcp", net.JoinHostPort(coreOverlayAddr.String(), "9999"))
	if err != nil {
		t.Fatalf("Body overlay dial: %v", err)
	}

	hello := "Hello from Body over WireGuard!"
	if _, err := bConn.Write([]byte(hello)); err != nil {
		t.Fatalf("overlay write: %v", err)
	}
	respBuf := make([]byte, 256)
	nRead, err := bConn.Read(respBuf)
	if err != nil {
		t.Fatalf("overlay read: %v", err)
	}
	bConn.Close()
	echoLn.Close()
	echoDone.Wait()

	echoMu.Lock()
	gotEcho := echoBytes
	echoMu.Unlock()
	if gotEcho != hello {
		t.Errorf("echo server received %q, want %q", gotEcho, hello)
	}
	if string(respBuf[:nRead]) != hello {
		t.Errorf("echo client received %q, want %q", string(respBuf[:nRead]), hello)
	}
	t.Log("Overlay TCP echo: PASS")

	// ── Real Doll Link test ─────────────────────────────────────────────
	// Start a real DollLink WebSocket server on Core's netstack overlay
	wsLn, err := coreTun.Netstack().ListenTCP(&net.TCPAddr{IP: net.IP(coreOverlayAddr.AsSlice()), Port: 9998})
	if err != nil {
		t.Fatalf("Core netstack ListenTCP for WS: %v", err)
	}

	wsSrv := ws.New(ws.Config{}, &wsLogger{inner: log}, echoHandler{})
	wsSrv.SetListener(wsLn)
	go func() {
		if err := wsSrv.Start(ctx); err != nil {
			t.Logf("WS server exited: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond) // let WS server start

	// Connect from Body's netstack and perform manual WS upgrade
	wsTcpConn, err := bNet.Dial("tcp", net.JoinHostPort(coreOverlayAddr.String(), "9998"))
	if err != nil {
		t.Fatalf("Body WS dial: %v", err)
	}

	// Manual HTTP upgrade to WebSocket
	upgradeReq := fmt.Sprintf("GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n",
		net.JoinHostPort(coreOverlayAddr.String(), "9998"))
	if _, err := wsTcpConn.Write([]byte(upgradeReq)); err != nil {
		t.Fatalf("WS upgrade write: %v", err)
	}

	// Read HTTP response
	httpResp := make([]byte, 4096)
	nRead, err = wsTcpConn.Read(httpResp)
	if err != nil {
		t.Fatalf("WS upgrade read: %v", err)
	}
	respStr := string(httpResp[:nRead])
	if len(respStr) < 12 || respStr[:9] != "HTTP/1.1 " || respStr[9:12] != "101" {
		t.Fatalf("WS upgrade did not return 101, got: %.60s", respStr)
	}
	t.Log("WebSocket upgrade: 101 Switching Protocols")

	// Send a real events.Event over the WebSocket
	event := events.Event{
		ID:        "e2e-test-001",
		Type:      events.TypeMessage,
		Source:    "test-body",
		Timestamp: time.Now().UTC(),
		Payload: events.MessagePayload{
			Text: "ping",
		},
	}
	eventData, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	// Write WS text frame: FIN=1, opcode=0x1, unmasked
	frame := buildWSTextFrame(eventData)
	if _, err := wsTcpConn.Write(frame); err != nil {
		t.Fatalf("WS frame write: %v", err)
	}

	// Read the WS response frame
	respFrame := make([]byte, 4096)
	nRead, err = wsTcpConn.Read(respFrame)
	if err != nil {
		t.Fatalf("WS frame read: %v", err)
	}

	frameData, err := parseWSFrame(respFrame[:nRead])
	if err != nil {
		t.Fatalf("parse WS frame: %v", err)
	}

	var respEvent events.Event
	if err := json.Unmarshal(frameData, &respEvent); err != nil {
		t.Fatalf("unmarshal response event: %v (raw: %s)", err, string(frameData))
	}

	if respEvent.CorrelationID != "e2e-test-001" {
		t.Errorf("response correlation_id = %q, want %q", respEvent.CorrelationID, "e2e-test-001")
	}
	if respEvent.Type != events.TypeMessage {
		t.Errorf("response type = %q, want %q", respEvent.Type, events.TypeMessage)
	}
	// Verify echo handler prepended "response_"
	if p, ok := respEvent.Payload.(map[string]any); ok {
		if text, ok := p["text"].(string); ok {
			if text != "response_ping" {
				t.Errorf("response text = %q, want %q", text, "response_ping")
			}
		} else {
			t.Errorf("response payload text is not a string: %T", p["text"])
		}
	} else {
		t.Errorf("response payload type = %T, want MessagePayload/map", respEvent.Payload)
	}
	t.Log("Real Doll Link event round-trip: PASS")

	wsTcpConn.Close()
	wsSrv.Shutdown(ctx)

	// ── Capture identities before destruction ────────────────────────────
	savedNet, err := memStore.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("LoadNetwork: %v", err)
	}
	if savedNet == nil {
		t.Fatal("no saved network")
	}
	savedMems, err := memStore.ListMemberships(ctx)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(savedMems) != 1 {
		t.Fatalf("expected 1 saved membership, got %d", len(savedMems))
	}

	// ── Phase 4: Core destruction + reconstruction ──────────────────────
	t.Log("=== Destroying Core ===")
	coreMgr.Stop()
	coreTun.Stop()

	// Real SQLite persistence — close and reopen to prove durability
	dbPath := filepath.Join(t.TempDir(), "recon_network_state.db")
	s1, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create reconstruction store: %v", err)
	}
	store1 := s1.(network.NetworkStore)
	if err := store1.SaveNetwork(ctx, savedNet); err != nil {
		t.Fatalf("recon SaveNetwork: %v", err)
	}
	for _, m := range savedMems {
		if err := store1.SaveMembership(ctx, m); err != nil {
			t.Fatalf("recon SaveMembership: %v", err)
		}
	}
	s1.Close()

	// Reopen the same SQLite DB — data must survive
	reconStore, err := persistence.NewNetworkStore(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen reconstruction store: %v", err)
	}

	reconNet, err := reconStore.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("recon LoadNetwork: %v", err)
	}
	if reconNet == nil {
		t.Fatal("recon LoadNetwork returned nil")
	}

	// Verify identities unchanged
	if reconNet.NetworkID != m1Net.NetworkID {
		t.Errorf("recon network_id = %q, want %q", reconNet.NetworkID, m1Net.NetworkID)
	}
	if reconNet.Core.PeerID != m1Net.Core.PeerID {
		t.Errorf("recon core peer_id = %q, want %q", reconNet.Core.PeerID, m1Net.Core.PeerID)
	}
	if reconNet.Core.PrivateKey != m1Net.Core.PrivateKey {
		t.Error("recon core private key changed")
	}
	if reconNet.Core.PublicKey != m1Net.Core.PublicKey {
		t.Error("recon core public key changed")
	}
	if reconNet.Core.OverlayAddress != m1Net.Core.OverlayAddress {
		t.Errorf("recon core overlay = %s, want %s", reconNet.Core.OverlayAddress, m1Net.Core.OverlayAddress)
	}

	// Reconstruct allocator for overlay prefix
	reconNet.Core.OverlayPrefix = network.NewIPv6Allocator(reconNet.NetworkID).Prefix()

	// Verify body membership intact
	reconMems, _ := reconStore.ListMemberships(ctx)
	if len(reconMems) != 1 {
		t.Fatalf("expected 1 recon membership, got %d", len(reconMems))
	}
	if reconMems[0].PeerID != network.PeerID(presp.BodyPeerID) {
		t.Errorf("recon peer_id = %q, want %q", reconMems[0].PeerID, presp.BodyPeerID)
	}
	if reconMems[0].OverlayAddress != bodyOverlayAddr {
		t.Errorf("recon body overlay = %s, want %s", reconMems[0].OverlayAddress, bodyOverlayAddr)
	}
	t.Log("Identity persistence: PASS — all unchanged")

	// ── Phase 5: Reconnect without re-pairing ────────────────────────────
	coreTun2 := wireguard.NewRealTunnel(log)
	coreMgr2 := wireguard.NewManager(coreTun2, reconStore, log)
	coreMgr2.SetListenPort(corePort)
	if err := coreMgr2.Start(ctx, reconNet); err != nil {
		t.Fatalf("Core manager 2 Start: %v", err)
	}
	t.Log("Reconstructed Core started")

	// Body reconnects naturally (same keys, same endpoint) — no re-pairing
	hsCtx2, hsCancel2 := context.WithTimeout(ctx, 30*time.Second)
	defer hsCancel2()
	if err := bTun.WaitHandshake(hsCtx2, 30*time.Second); err != nil {
		t.Fatalf("Reconnect handshake timeout: %v", err)
	}
	t.Log("Reconnect handshake: PASS — Body reconnected without re-pairing")

	// Verify overlay still works
	echoLn2, err := coreTun2.Netstack().ListenTCP(&net.TCPAddr{IP: net.IP(coreOverlayAddr.AsSlice()), Port: 9997})
	if err != nil {
		t.Fatalf("Core netstack 2 ListenTCP: %v", err)
	}

	var echo2Done sync.WaitGroup
	echo2Done.Add(1)
	go func() {
		defer echo2Done.Done()
		conn, err := echoLn2.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		conn.Write(buf[:n])
	}()

	bConn2, err := bNet.Dial("tcp", net.JoinHostPort(coreOverlayAddr.String(), "9997"))
	if err != nil {
		t.Fatalf("Body reconnect dial: %v", err)
	}
	bConn2.Write([]byte("reconnect-ok"))
	resp2 := make([]byte, 256)
	n2, _ := bConn2.Read(resp2)
	bConn2.Close()
	echoLn2.Close()
	echo2Done.Wait()

	if string(resp2[:n2]) != "reconnect-ok" {
		t.Errorf("reconnect echo = %q, want %q", string(resp2[:n2]), "reconnect-ok")
	}
	t.Log("Reconnect overlay TCP echo: PASS")

	// Cleanup
	bTun.Stop()
	coreMgr2.Stop()
	coreTun2.Stop()

	t.Log("=== M3 E2E: ALL PASS ===")
}

// ── Port allocation (loopback) ────────────────────────────────────────────────

// decodeWgPubKey decodes a base64 WG public key string into a *network.WireGuardPublicKey.
func decodeWgPubKey(b64 string) *network.WireGuardPublicKey {
	raw, err := dollnetwork.DecodeWgPublicKey(b64)
	if err != nil {
		return nil
	}
	var pub network.WireGuardPublicKey
	copy(pub[:], raw)
	return &pub
}

// pickPort returns a free TCP port on loopback.
func pickPort(t *testing.T) int {
	t.Helper()
	a, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	defer a.Close()
	return a.Addr().(*net.TCPAddr).Port
}

// ── WebSocket frame helpers ───────────────────────────────────────────────────

// buildWSTextFrame constructs a complete MASKED WS text frame.
// Client-to-server frames MUST be masked per RFC 6455.
func buildWSTextFrame(payload []byte) []byte {
	// Generate random 4-byte mask key
	maskKey := make([]byte, 4)
	for i := range maskKey {
		maskKey[i] = byte(i * 37) // deterministic mask for reproducibility
	}

	// XOR the payload with the mask key
	maskedPayload := make([]byte, len(payload))
	for i, b := range payload {
		maskedPayload[i] = b ^ maskKey[i%4]
	}

	var frame []byte
	// FIN=1, opcode=0x1 (text)
	frame = append(frame, 0x81)
	// Length with mask bit set
	if len(payload) < 126 {
		frame = append(frame, 0x80|byte(len(payload)))
	} else if len(payload) < 65536 {
		frame = append(frame, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	} else {
		frame = append(frame, 0x80|127)
		b := make([]byte, 8)
		for i := 0; i < 8; i++ {
			b[7-i] = byte(len(payload) >> (8 * i))
		}
		frame = append(frame, b...)
	}
	// Mask key
	frame = append(frame, maskKey...)
	// Masked payload
	frame = append(frame, maskedPayload...)
	return frame
}

// parseWSFrame extracts the payload from an unmasked WS data frame.
func parseWSFrame(frame []byte) ([]byte, error) {
	if len(frame) < 2 {
		return nil, fmt.Errorf("frame too short: %d bytes", len(frame))
	}

	payloadLen := int(frame[1] & 0x7f)
	offset := 2
	if payloadLen == 126 {
		if len(frame) < 4 {
			return nil, fmt.Errorf("frame too short for 16-bit length")
		}
		payloadLen = int(frame[2])<<8 | int(frame[3])
		offset = 4
	} else if payloadLen == 127 {
		if len(frame) < 10 {
			return nil, fmt.Errorf("frame too short for 64-bit length")
		}
		payloadLen = 0
		for i := 0; i < 8; i++ {
			payloadLen = payloadLen<<8 | int(frame[2+i])
		}
		offset = 10
	}

	// Support masked frames (server may mask outgoing frames)
	// Actually: RFC 6455 says server MUST NOT mask frames it sends to client.
	// But gorilla/websocket as of some versions masks outgoing server frames.
	// Let's handle both cases.
	if len(frame) > offset && (frame[1]&0x80) != 0 {
		// Masked frame
		if len(frame) < offset+4 {
			return nil, fmt.Errorf("frame too short for mask key")
		}
		maskKey := frame[offset : offset+4]
		offset += 4
		if len(frame) < offset+payloadLen {
			return nil, fmt.Errorf("frame too short for masked payload: have %d, need %d", len(frame), offset+payloadLen)
		}
		payload := make([]byte, payloadLen)
		for i := 0; i < payloadLen; i++ {
			payload[i] = frame[offset+i] ^ maskKey[i%4]
		}
		return payload, nil
	}

	// Unmasked frame
	if len(frame) < offset+payloadLen {
		return nil, fmt.Errorf("frame too short for payload: have %d, need %d", len(frame), offset+payloadLen)
	}
	return frame[offset : offset+payloadLen], nil
}
