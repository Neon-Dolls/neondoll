//go:build e2e

// M4.6 E2E test for the relayed Doll Network overlay path:
//
//	M1      Network + identity creation
//	M2      Invitation + pairing
//	M3      WireGuard state setup
//	M4.6    Core behind relay (RelayTransport), route credentials,
//	        Doll Link proof, and assertion that Core has no direct WG path.
//
// Reuses helper types/functions from m3_e2e_test.go (same package).
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	network "github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Pairing"
	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
	wireguard "github.com/Neon-Dolls/neondoll/Core/WireGuard"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	dollnetwork "github.com/Neon-Dolls/neondoll/DollNetwork"
)

func TestM46E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// ──────────────────── M1: Identity creation ────────────────────
	netID := network.NetworkID("e2e-m46-test")
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
		m1Net.NetworkID, m1Net.Core.PeerID, coreAddr, overlayPrefix)

	// Create Body identity
	bMeta := body.BodyMetadata{
		Implementation: "neondoll-e2e",
		Platform:       "linux",
		Arch:           "amd64",
	}
	bID, err := body.NewIdentityState("M46TestBody", bMeta)
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}

	bKP, err := body.GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	t.Logf("Body WG pubkey: %s", bKP.PublicKeyBase64())

	// ──────────────────── M2: Invitation + Pairing ────────────────────
	invStore := invitation.NewMemoryStore()
	invSvc := invitation.NewService(invStore, stubClock{})

	memStore := newMemNetStore()
	if err := memStore.SaveNetwork(ctx, m1Net); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	endpoints := dollnetwork.Endpoints{{URL: "relay://core-m46:51820"}}
	pairSvc := pairing.NewService(m1Net, memStore, invSvc, pairing.AllowAuthorizer{}, stubClock{}, endpoints)

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

	preq := body.BuildPairRequest(inv.InvitationID, inv.InvitationSecret, bID, bKP)
	presp, errResp := pairSvc.HandlePairing(ctx, &preq)
	if errResp != nil {
		t.Fatalf("HandlePairing error: %+v", errResp)
	}
	if presp == nil {
		t.Fatal("HandlePairing returned nil response")
	}

	t.Logf("Pairing: body_peer_id=%s body_addr=%v core_wg_key=%s",
		presp.BodyPeerID, presp.BodyAddresses, presp.CoreWGPublicKey[:16]+"...")

	if got, want := presp.NetworkID, string(m1Net.NetworkID); got != want {
		t.Errorf("network_id = %q, want %q", got, want)
	}

	bodyOverlayAddr := netip.MustParseAddr(presp.BodyAddresses[0])
	coreOverlayAddr := netip.MustParseAddr(presp.CoreAddresses[0])

	corePubBytes, err := dollnetwork.DecodeWgPublicKey(presp.CoreWGPublicKey)
	if err != nil {
		t.Fatalf("DecodeWgPublicKey: %v", err)
	}
	var corePub [32]byte
	copy(corePub[:], corePubBytes)

	// ──────────────────── M4.6: Relay-aware Core setup ────────────────────
	relayPortSvc := pickPort(t)
	relayPortWS := pickPort(t)

	relayCfg := relay.DefaultServiceConfig()
	relayCfg.UDP.ListenAddress = "127.0.0.1"
	relayCfg.UDP.PortMin = relayPortSvc
	relayCfg.UDP.PortMax = relayPortSvc
	relaySvc, err := relay.NewService(relayCfg, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relaySvc.Start(ctx)
	}()

	cs := relay.NewControlServer(relaySvc, fmt.Sprintf("127.0.0.1:%d", relayPortWS), log)
	csDone := make(chan error, 1)
	go func() {
		csDone <- cs.Start(ctx)
	}()
	time.Sleep(100 * time.Millisecond)

	// Core connects to Relay via ControlClient
	relayURL := fmt.Sprintf("ws://127.0.0.1:%d/relay", relayPortWS)
	clientCfg := relay.ClientConfig{
		RelayURL:          relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "test-reg-token",
	}
	ctrlClient := relay.NewControlClient(clientCfg)
	if err := ctrlClient.Start(ctx); err != nil {
		t.Fatalf("ControlClient.Start: %v", err)
	}
	defer ctrlClient.Shutdown()

	// Open route with credentials
	_, err = ctrlClient.OpenRoute(ctx, relay.RouteID(1), relay.RouteCredentials{Token: "test-route-credential"})
	if err != nil {
		t.Fatalf("OpenRoute: %v", err)
	}
	t.Log("Route opened with credentials")

	// Create RelayTransport backed by the ControlClient.
	// NewRelayTransport pre-initialises the queue and client reference.
	relayTransport := relay.NewRelayTransport(ctrlClient)

	// Open the transport — registers the frame handler on the control
	// tunnel. Port 0 means no direct WG listener.
	_, relayPort, err := relayTransport.Open(0)
	if err != nil {
		t.Fatalf("RelayTransport.Open: %v", err)
	}
	if relayPort != 0 {
		t.Fatalf("Core must NOT have a direct WG port, got %d (expected 0)", relayPort)
	}
	t.Logf("RelayTransport port = %d (no direct WG path: PASS)", relayPort)

	// Start Core tunnel with RelayTransport bind
	coreTun := wireguard.NewRealTunnel(log)
	coreTun.SetBind(relayTransport)
	coreMgr := wireguard.NewManager(coreTun, memStore, log)
	if err := coreMgr.Start(ctx, m1Net); err != nil {
		t.Fatalf("Core manager Start: %v", err)
	}
	defer coreMgr.Stop()
	defer coreTun.Stop()

	// Core endpoint for Body — points at relay's UDP listener
	coreEndpoint := fmt.Sprintf("127.0.0.1:%d", relayPortSvc)
	relayPeerEndpoint := relay.RelayEndpointString(relay.RouteID(1))

	// Save Body membership to Core's store so Core can find Body's WG info
	bodyMembership := &network.Membership{
		BodyID:             string(bID.Identity.BodyID),
		PeerID:             network.PeerID(presp.BodyPeerID),
		WireGuardPublicKey: decodeWgPubKey(bKP.PublicKeyBase64()),
		OverlayAddress:     bodyOverlayAddr,
		Endpoint:           relayPeerEndpoint,
		Status:             network.MembershipActive,
	}
	if err := memStore.SaveMembership(ctx, bodyMembership); err != nil {
		t.Fatalf("SaveMembership: %v", err)
	}
	// Refresh Core's peer config now that Body membership is saved
	if err := coreMgr.RefreshPeers(ctx); err != nil {
		t.Fatalf("RefreshPeers: %v", err)
	}

	// ──────────────────── Body connects through relay ────────────────────
	bodyPort := pickPort(t)

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
	defer bTun.Stop()

	if err := bTun.Start(ctx); err != nil {
		t.Fatalf("bodyTun.Start: %v", err)
	}

	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := bTun.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("Body handshake timed out (relayed): %v", err)
	}
	t.Log("WireGuard handshake completed through relay")

	// Allow tunnel to settle before attempting data traffic
	time.Sleep(500 * time.Millisecond)

	// ──────────────────── Overlay connectivity proof (TCP echo) ────────────────────
	coreNet := coreTun.Netstack()
	echoAddr := &net.TCPAddr{IP: net.ParseIP(coreOverlayAddr.String()), Port: 9999}
	echoLn, err := coreNet.ListenTCP(echoAddr)
	if err != nil {
		t.Fatalf("coreNet.ListenTCP: %v", err)
	}
	defer echoLn.Close()

	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		conn, err := echoLn.Accept()
		if err != nil {
			t.Logf("echo accept: %v", err)
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		conn.Write([]byte("pong:" + string(buf[:n])))
	}()

	bNet := bTun.Netstack()
	bodyEchoTarget := netip.AddrPortFrom(coreOverlayAddr, 9999).String()
	// Small delay for listener readiness
	time.Sleep(50 * time.Millisecond)
	bConn, err := bNet.Dial("tcp", bodyEchoTarget)
	if err != nil {
		t.Fatalf("body Dial tcp: %v", err)
	}
	defer bConn.Close()

	if _, err := bConn.Write([]byte("hello")); err != nil {
		t.Fatalf("body write: %v", err)
	}
	buf := make([]byte, 1024)
	n, err := bConn.Read(buf)
	if err != nil {
		t.Fatalf("body read: %v", err)
	}
	if got := string(buf[:n]); got != "pong:hello" {
		t.Fatalf("overlay echo got %q, want %q", got, "pong:hello")
	}
	bConn.Close()
	echoLn.Close()
	<-echoDone
	t.Log("Overlay TCP echo through relay: PASS")

	// ──────────────────── Doll Link proof ────────────────────
	wsLn, err := coreNet.ListenTCP(&net.TCPAddr{IP: net.ParseIP(coreOverlayAddr.String()), Port: 9998})
	if err != nil {
		t.Fatalf("coreNet.ListenTCP for WS: %v", err)
	}
	wsCfg := ws.Config{}
	wsSrv := ws.New(wsCfg, &wsLogger{inner: log}, echoHandler{})
	wsSrv.SetListener(wsLn)
	go func() {
		if err := wsSrv.Start(ctx); err != nil {
			t.Logf("WS server exited: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond) // let WS server start

	// Body connects to Core's WS via gorilla websocket dialer through overlay
	wsTcpConn, err := bNet.Dial("tcp", net.JoinHostPort(coreOverlayAddr.String(), "9998"))
	if err != nil {
		t.Fatalf("body WS dial: %v", err)
	}
	defer wsTcpConn.Close()

	upgradeReq := fmt.Sprintf("GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n",
		net.JoinHostPort(coreOverlayAddr.String(), "9998"))
	if _, err := wsTcpConn.Write([]byte(upgradeReq)); err != nil {
		t.Fatalf("WS upgrade write: %v", err)
	}

	// Read the HTTP upgrade response line by line
	br := bufio.NewReader(wsTcpConn)
	respLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("WS upgrade read status: %v", err)
	}
	respLine = strings.TrimSpace(respLine)
	if len(respLine) < 12 || respLine[:9] != "HTTP/1.1 " || respLine[9:12] != "101" {
		t.Fatalf("WS upgrade did not return 101, got: %s", respLine)
	}
	// Consume remaining headers
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("WS upgrade read header: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
	}
	t.Log("WebSocket upgrade: 101 Switching Protocols")

	// Send a Doll Link event
	pingEvent := events.Event{
		Type:      events.TypeMessage,
		Source:    "body",
		ID:        "m46-ping-001",
		Timestamp: time.Now().UTC(),
		Payload: events.MessagePayload{
			Text: "ping-from-body",
		},
	}
	eventBytes, _ := json.Marshal(pingEvent)

	wsFrame := buildWSTextFrame(eventBytes)
	if _, err := wsTcpConn.Write(wsFrame); err != nil {
		t.Fatalf("WS write frame: %v", err)
	}

	readBuf := make([]byte, 4096)
	n, err = wsTcpConn.Read(readBuf)
	if err != nil {
		t.Fatalf("WS read response: %v", err)
	}
	respPayload, err := parseWSFrame(readBuf[:n])
	if err != nil {
		t.Fatalf("parse WS response frame: %v (raw=%.40q)", err, string(readBuf[:n]))
	}

	var respEvent events.Event
	if err := json.Unmarshal(respPayload, &respEvent); err != nil {
		t.Fatalf("unmarshal WS response: %v", err)
	}

	if respEvent.CorrelationID != "m46-ping-001" {
		t.Fatalf("WS correlation ID: got %q, want %q", respEvent.CorrelationID, "m46-ping-001")
	}
	t.Logf("Doll Link event round-trip: correlation=%q PASS", respEvent.CorrelationID)

	// ──────────────────── Cleanup ────────────────────
	ctrlClient.Shutdown()
	relaySvc.Shutdown(ctx)

	t.Log("=== M4.6 E2E: ALL PASS ===")
}
