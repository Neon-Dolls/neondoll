//go:build e2e

// M4.7 shared E2E helpers.
//
// These helpers extract the REAL M4.6 E2E topology
// (Body → WG → Relay → RelayTransport → Core WG → Doll Network → Doll Link)
// from TestM46E2E so the M4.7 reconnect / reconstruction / isolation /
// failure tests operate on the same real stack instead of a standalone
// parallel harness.
//
//	Identity invariants captured by captureIdentityState:
//	  NetworkID, Core PeerID, Core WG keypair, Core overlay IPv6/prefix,
//	  Body ID, Body PeerID, Body WG public key, Body overlay IPv6,
//	  membership record (endpoint + status + count), invitation state.
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
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
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// ── Topology ──────────────────────────────────────────────────────────────────

// m46Topology holds the full real M4.6 E2E stack so M4.7 tests can operate on
// the same Body → WG → Relay → RelayTransport → Core WG → Doll Network →
// Doll Link topology. There is deliberately NO second E2E harness.
type m46Topology struct {
	// Identity / pairing state (M1 + M2).
	netID    network.NetworkID
	m1Net    *network.Network
	bodyID   *body.IdentityState
	bodyKP   *body.WgKeypair
	invID    string
	invStore *invitation.MemoryStore
	invSvc   *invitation.Service
	memStore *memNetStore
	pairSvc  *pairing.PairingService

	// Relay control plane (M4.6).
	relayPortSvc    int
	relayPortWS     int
	relayCfg        relay.ServiceConfig
	relayURL        string
	relaySvc        *relay.Service
	cs              *relay.ControlServer
	csCancel        context.CancelFunc
	ctrlClient      *relay.ControlClient
	clientCfg       relay.ClientConfig
	relayTransport  *relay.RelayTransport
	coreMgr         *wireguard.Manager
	coreTun         wireguard.Tunnel
	bTun            *body.BodyTunnel
	coreNet         *netstack.Net
	wsServerStarted bool

	// Overlay identity derived from pairing.
	bodyOverlayAddr netip.Addr
	coreOverlayAddr netip.Addr
	overlayPrefix   netip.Prefix
	corePub         [32]byte
	coreEndpoint    string // Body's WG endpoint (relay UDP listener)
	routeEndpoint   string // route 1's allocated UDP endpoint (host:port)

	bodyMembership *network.Membership

	cleanupOnce sync.Once
	cleanupFn   func()
}

// discardLogger returns a debug-level slog logger writing to io.Discard.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// withReconnectTuning configures the ControlClient for reconnect tests:
// fast disconnect detection (no traffic for ReadTimeout ⇒ dead), quick
// reconnect attempts. Quiet control tunnels without relay traffic will also
// trip this tuning, which is fine: route restoration is idempotent.
func withReconnectTuning() func(*relay.ClientConfig) {
	return func(cfg *relay.ClientConfig) {
		cfg.ReadTimeout = 2 * time.Second
		cfg.PingInterval = 30 * time.Second
		cfg.ReconnectInitial = 100 * time.Millisecond
		cfg.ReconnectMax = 2 * time.Second
	}
}

// startRelayService (re)creates the relay.Service over the topology's UDP
// port range and stores it on the topology. The ControlServer that bridges
// Core to this service must be (re)started separately via
// restartControlServer.
func (tp *m46Topology) startRelayService(t *testing.T, ctx context.Context, log *slog.Logger) {
	t.Helper()
	relaySvc, err := relay.NewService(tp.relayCfg, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("relay.NewService: %v", err)
	}
	go func() {
		_ = relaySvc.Start(ctx)
	}()
	tp.relaySvc = relaySvc
}

// startControlServer creates a ControlServer on addr over svc and cancels it
// via the returned cancel func. The relay service stays alive: cancelling
// only tears down the Core ↔ Relay WS control plane.
func startControlServer(t *testing.T, ctx context.Context, log *slog.Logger, svc *relay.Service, addr string) (*relay.ControlServer, context.CancelFunc) {
	t.Helper()
	csCtx, cancel := context.WithCancel(ctx)
	cs := relay.NewControlServer(svc, addr, log)
	if err := cs.Start(csCtx); err != nil {
		cancel()
		t.Fatalf("ControlServer.Start(%s): %v", addr, err)
	}
	return cs, cancel
}

// restartControlServer starts a fresh ControlServer on the topology's WS
// port over the CURRENT relay service. Retries briefly while the previous
// instance's listener is still being released.
func (tp *m46Topology) restartControlServer(t *testing.T, ctx context.Context, log *slog.Logger) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", tp.relayPortWS)
	deadline := time.Now().Add(5 * time.Second)
	for {
		csCtx, cancel := context.WithCancel(ctx)
		cs := relay.NewControlServer(tp.relaySvc, addr, log)
		if err := cs.Start(csCtx); err == nil {
			tp.cs, tp.csCancel = cs, cancel
			t.Logf("ControlServer restarted on %s (relay service UDP %d-%d)", addr, tp.relayCfg.UDP.PortMin, tp.relayCfg.UDP.PortMax)
			return
		}
		cancel()
		if time.Now().After(deadline) {
			t.Fatalf("ControlServer restart on %s: port still busy after 5s", addr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// setupM46Topology builds the full real M4.6 E2E topology:
//
//	M1      Network + identity creation
//	M2      Invitation + pairing
//	M3      WireGuard state setup
//	M4.6    Core behind relay (RelayTransport), route credentials,
//	        Doll Link WS echo server on the overlay
//
// It mirrors TestM46E2E exactly, including the assertion that Core has no
// direct WG port (RelayTransport.Open(0) must yield port 0).
func setupM46Topology(t *testing.T, ctx context.Context, log *slog.Logger, suffix string, opts ...func(*relay.ClientConfig)) *m46Topology {
	t.Helper()
	if log == nil {
		log = discardLogger()
	}
	tp := &m46Topology{}

	// ──────────────────── M1: Identity creation ────────────────────
	tp.netID = network.NetworkID("e2e-m47-" + suffix)
	m1Net, err := network.NewNetwork(tp.netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	alloc := network.NewIPv6Allocator(tp.netID)
	coreAddr, err := alloc.CoreAddress()
	if err != nil {
		t.Fatalf("CoreAddress: %v", err)
	}
	tp.overlayPrefix = alloc.Prefix()
	m1Net.Core.OverlayAddress = coreAddr
	m1Net.Core.OverlayPrefix = tp.overlayPrefix
	tp.m1Net = m1Net

	t.Logf("Core network: net_id=%s peer_id=%s overlay=%s prefix=%s",
		m1Net.NetworkID, m1Net.Core.PeerID, coreAddr, tp.overlayPrefix)

	bMeta := body.BodyMetadata{
		Implementation: "neondoll-e2e",
		Platform:       "linux",
		Arch:           "amd64",
	}
	bID, err := body.NewIdentityState("M47TestBody-"+suffix, bMeta)
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	bKP, err := body.GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	tp.bodyID, tp.bodyKP = bID, bKP
	t.Logf("Body WG pubkey: %s", bKP.PublicKeyBase64())

	// ──────────────────── M2: Invitation + Pairing ────────────────────
	tp.invStore = invitation.NewMemoryStore()
	tp.invSvc = invitation.NewService(tp.invStore, stubClock{})

	tp.memStore = newMemNetStore()
	if err := tp.memStore.SaveNetwork(ctx, m1Net); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	endpoints := dollnetwork.Endpoints{{URL: "relay://core-m47-" + suffix + ":51820"}}
	tp.pairSvc = pairing.NewService(m1Net, tp.memStore, tp.invSvc, pairing.AllowAuthorizer{}, stubClock{}, endpoints)

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

	tp.bodyOverlayAddr = netip.MustParseAddr(presp.BodyAddresses[0])
	tp.coreOverlayAddr = netip.MustParseAddr(presp.CoreAddresses[0])

	corePubBytes, err := dollnetwork.DecodeWgPublicKey(presp.CoreWGPublicKey)
	if err != nil {
		t.Fatalf("DecodeWgPublicKey: %v", err)
	}
	copy(tp.corePub[:], corePubBytes)

	// ──────────────────── M4.6: Relay-aware Core setup ────────────────────
	tp.relayPortSvc = pickPort(t)
	tp.relayPortWS = pickPort(t)

	tp.relayCfg = relay.DefaultServiceConfig()
	tp.relayCfg.Credentials = []string{"d89e45dd5a9a2557aa7e36afca04a58e0c47eddd613992f13896c60bef14ac29"}
	tp.relayCfg.UDP.ListenAddress = "127.0.0.1"
	tp.relayCfg.UDP.PortMin = tp.relayPortSvc
	// Room for a second route (or a second relay instance) at adjacent
	// ports; route 1 still binds the first free port = relayPortSvc.
	tp.relayCfg.UDP.PortMax = tp.relayPortSvc + 2
	tp.startRelayService(t, ctx, log)

	tp.cs, tp.csCancel = startControlServer(t, ctx, log, tp.relaySvc, fmt.Sprintf("127.0.0.1:%d", tp.relayPortWS))
	time.Sleep(100 * time.Millisecond)

	// Core connects to Relay via ControlClient.
	tp.relayURL = fmt.Sprintf("ws://127.0.0.1:%d/relay", tp.relayPortWS)
	clientCfg := relay.ClientConfig{
		RelayURL:          tp.relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "test-reg-token",
	}
	for _, opt := range opts {
		opt(&clientCfg)
	}
	tp.clientCfg = clientCfg
	ctrlClient := relay.NewControlClient(clientCfg)
	if err := ctrlClient.Start(ctx); err != nil {
		t.Fatalf("ControlClient.Start: %v", err)
	}
	tp.ctrlClient = ctrlClient

	// Open route 1 with credentials.
	opened, err := ctrlClient.OpenRoute(ctx, relay.RouteID(1), relay.RouteCredentials{Token: "test-route-credential"})
	if err != nil {
		t.Fatalf("OpenRoute: %v", err)
	}
	tp.routeEndpoint = opened.AllocatedEndpoint
	t.Logf("Route 1 opened with credentials, endpoint=%s", tp.routeEndpoint)

	// Core endpoint for Body — points at the relay's UDP listener.
	tp.coreEndpoint = fmt.Sprintf("127.0.0.1:%d", tp.relayPortSvc)

	// RelayTransport backed by the ControlClient; port 0 means no direct
	// Core WG listener — all traffic goes through the relay.
	relayTransport := relay.NewRelayTransport(ctrlClient)
	_, relayPort, err := relayTransport.Open(0)
	if err != nil {
		t.Fatalf("RelayTransport.Open: %v", err)
	}
	if relayPort != 0 {
		t.Fatalf("Core must NOT have a direct WG port, got %d (expected 0)", relayPort)
	}
	t.Logf("RelayTransport port = %d (no direct WG path: PASS)", relayPort)
	tp.relayTransport = relayTransport

	coreTun := wireguard.NewRealTunnel(log)
	coreTun.SetBind(relayTransport)
	coreMgr := wireguard.NewManager(coreTun, tp.memStore, log)
	if err := coreMgr.Start(ctx, m1Net); err != nil {
		t.Fatalf("Core manager Start: %v", err)
	}
	tp.coreMgr, tp.coreTun = coreMgr, coreTun

	// Save Body membership to Core's store so Core can find Body's WG info.
	relayPeerEndpoint := relay.RelayEndpointString(relay.RouteID(1))
	bodyMembership := &network.Membership{
		BodyID:             string(bID.Identity.BodyID),
		PeerID:             network.PeerID(presp.BodyPeerID),
		WireGuardPublicKey: decodeWgPubKey(bKP.PublicKeyBase64()),
		OverlayAddress:     tp.bodyOverlayAddr,
		Endpoint:           relayPeerEndpoint,
		Status:             network.MembershipActive,
	}
	if err := tp.memStore.SaveMembership(ctx, bodyMembership); err != nil {
		t.Fatalf("SaveMembership: %v", err)
	}
	tp.bodyMembership = bodyMembership
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
		CorePublicKey:  tp.corePub,
		OverlayAddress: tp.bodyOverlayAddr,
		OverlayPrefix:  tp.overlayPrefix,
		CoreEndpoint:   tp.coreEndpoint,
		ListenPort:     bodyPort,
		MTU:            wireguard.DefaultMTU,
	}, log)
	if err := bTun.Start(ctx); err != nil {
		t.Fatalf("bodyTun.Start: %v", err)
	}
	tp.bTun = bTun
	tp.coreNet = coreTun.Netstack()

	// Doll Link WS echo server on the Core overlay (port 9998) — the
	// proveDollLink helper dials this through the Body overlay path.
	wsLn, err := tp.coreNet.ListenTCP(&net.TCPAddr{IP: net.ParseIP(tp.coreOverlayAddr.String()), Port: 9998})
	if err != nil {
		t.Fatalf("coreNet.ListenTCP for WS: %v", err)
	}
	wsSrv := ws.New(ws.Config{}, &wsLogger{inner: log}, echoHandler{})
	wsSrv.SetListener(wsLn)
	go func() {
		if err := wsSrv.Start(ctx); err != nil {
			log.Debug("overlay WS server exited", "error", err)
		}
	}()
	tp.wsServerStarted = true

	// Cleanup: shutdown everything this topology created. Safe to call
	// multiple times; reads the CURRENT service/cs pointers so a mid-test
	// relay restart is torn down correctly.
	tp.cleanupFn = func() {
		_ = tp.ctrlClient.Shutdown()
		_ = tp.coreMgr.Stop()
		_ = tp.coreTun.Stop()
		_ = tp.bTun.Stop()
		if tp.csCancel != nil {
			tp.csCancel()
		}
		if tp.relaySvc != nil {
			_ = tp.relaySvc.Shutdown(context.Background())
		}
	}
	t.Cleanup(tp.cleanup)

	return tp
}

// cleanup shuts the topology down exactly once.
func (tp *m46Topology) cleanup() {
	if tp.cleanupFn != nil {
		tp.cleanupOnce.Do(tp.cleanupFn)
	}
}

// ── Identity invariants ───────────────────────────────────────────────────────

// identitySnapshot captures every identity invariant that must survive
// relay reconnects and reconstruction.
type identitySnapshot struct {
	netID        string
	corePeerID   string
	coreWGKey    string
	corePrivKey  string
	coreOverlay  netip.Addr
	corePrefix   netip.Prefix
	bodyID       string
	bodyPeerID   string
	bodyWGKey    string
	bodyOverlay  netip.Addr
	bodyEndpoint string
	membership   network.MembershipStatus
	memberships  int
	invFound     bool
	invConsumed  bool
}

func keyString(k any) string { return fmt.Sprintf("%x", k) }

// captureIdentityState reads the live identity state from the real stores
// (network store, membership store, invitation store) plus the topology's
// Core WG key.
func captureIdentityState(t *testing.T, ctx context.Context, tp *m46Topology) *identitySnapshot {
	t.Helper()
	snap := &identitySnapshot{
		netID:       string(tp.netID),
		coreWGKey:   keyString(tp.m1Net.Core.PublicKey),
		corePrivKey: keyString(tp.m1Net.Core.PrivateKey),
	}

	n, err := tp.memStore.LoadNetwork(ctx)
	if err != nil || n == nil {
		t.Fatalf("LoadNetwork: %v (net=%v)", err, n)
	}
	snap.netID = string(n.NetworkID)
	snap.corePeerID = string(n.Core.PeerID)
	snap.coreOverlay = n.Core.OverlayAddress
	snap.corePrefix = n.Core.OverlayPrefix
	snap.coreWGKey = keyString(n.Core.PublicKey)
	snap.corePrivKey = keyString(n.Core.PrivateKey)

	mems, err := tp.memStore.ListMemberships(ctx)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	snap.memberships = len(mems)
	if len(mems) != 1 {
		t.Fatalf("expected exactly 1 membership before/without re-pairing, got %d", len(mems))
	}
	m := mems[0]
	snap.bodyID = m.BodyID
	snap.bodyPeerID = string(m.PeerID)
	snap.bodyOverlay = m.OverlayAddress
	snap.bodyEndpoint = m.Endpoint
	snap.membership = m.Status
	if m.WireGuardPublicKey != nil {
		snap.bodyWGKey = keyString(*m.WireGuardPublicKey)
	}

	inv, err := tp.invStore.Get(ctx, tp.invID)
	if err != nil || inv == nil {
		snap.invFound = false
	} else {
		snap.invFound = true
		snap.invConsumed = inv.Consumed
	}
	return snap
}

// assertIdentityUnchanged fails the test if any captured identity invariant
// differs between before and after a relay event.
func assertIdentityUnchanged(t *testing.T, label string, before, after *identitySnapshot) {
	t.Helper()
	var diffs []string
	cmp := func(name string, b, a any) {
		if fmt.Sprint(b) != fmt.Sprint(a) {
			diffs = append(diffs, fmt.Sprintf("%s: %v → %v", name, b, a))
		}
	}
	cmp("NetworkID", before.netID, after.netID)
	cmp("Core PeerID", before.corePeerID, after.corePeerID)
	cmp("Core WG PublicKey", before.coreWGKey, after.coreWGKey)
	cmp("Core WG PrivateKey", before.corePrivKey, after.corePrivKey)
	cmp("Core overlay IPv6", before.coreOverlay, after.coreOverlay)
	cmp("Core overlay prefix", before.corePrefix, after.corePrefix)
	cmp("Body ID", before.bodyID, after.bodyID)
	cmp("Body PeerID", before.bodyPeerID, after.bodyPeerID)
	cmp("Body WG PublicKey", before.bodyWGKey, after.bodyWGKey)
	cmp("Body overlay IPv6", before.bodyOverlay, after.bodyOverlay)
	cmp("Body membership endpoint", before.bodyEndpoint, after.bodyEndpoint)
	cmp("Body membership status", before.membership, after.membership)
	cmp("membership count", before.memberships, after.memberships)
	cmp("invitation present", before.invFound, after.invFound)
	cmp("invitation consumed", before.invConsumed, after.invConsumed)
	if len(diffs) > 0 {
		t.Fatalf("%s: identity invariants changed:\n  %s", label, strings.Join(diffs, "\n  "))
	}
	t.Logf("%s: identity invariants unchanged (NetworkID=%s core_peer=%s body_peer=%s core_overlay=%s body_overlay=%s memberships=%d invitation_consumed=%v)",
		label, after.netID, after.corePeerID, after.bodyPeerID, after.coreOverlay, after.bodyOverlay, after.memberships, after.invConsumed)
}

// ── Connectivity proofs ───────────────────────────────────────────────────────

// proveWgHandshake waits for the Body's WireGuard handshake through the
// relay. After a reconnect/reconstruction the session established earlier
// may still be valid, in which case this returns immediately; the real
// post-event liveness proof is proveOverlayEcho.
func proveWgHandshake(t *testing.T, ctx context.Context, tp *m46Topology) {
	t.Helper()
	hsCtx, hsCancel := context.WithTimeout(ctx, 20*time.Second)
	defer hsCancel()
	if err := tp.bTun.WaitHandshake(hsCtx, 20*time.Second); err != nil {
		t.Fatalf("Body handshake timed out (relayed): %v", err)
	}
	t.Log("WireGuard handshake completed through relay")
}

// proveOverlayEcho proves the overlay path works end to end: a TCP echo
// server on the Core overlay (real netstack listener) is reached from the
// Body overlay (Body netstack dial) through the relayed WG tunnel.
func proveOverlayEcho(t *testing.T, ctx context.Context, log *slog.Logger, tp *m46Topology, label string) {
	t.Helper()
	const attempts = 3
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := tryOverlayEcho(t, ctx, tp); err != nil {
			lastErr = err
			t.Logf("overlay echo (%s) attempt %d/%d failed: %v", label, attempt, attempts, err)
			time.Sleep(400 * time.Millisecond)
			continue
		}
		t.Logf("Overlay TCP echo through relay (%s): PASS", label)
		return
	}
	t.Fatalf("overlay echo (%s) failed after %d attempts: %v", label, attempts, lastErr)
}

func tryOverlayEcho(t *testing.T, ctx context.Context, tp *m46Topology) error {
	t.Helper()
	port := pickPort(t)
	coreNet := tp.coreNet
	echoLn, err := coreNet.ListenTCP(&net.TCPAddr{IP: net.ParseIP(tp.coreOverlayAddr.String()), Port: port})
	if err != nil {
		return fmt.Errorf("coreNet.ListenTCP: %w", err)
	}
	defer echoLn.Close()

	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		conn, err := echoLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		_, _ = conn.Write([]byte("pong:" + string(buf[:n])))
	}()
	defer func() {
		_ = echoLn.Close()
		<-echoDone
	}()

	bNet := tp.bTun.Netstack()
	target := netip.AddrPortFrom(tp.coreOverlayAddr, uint16(port)).String()

	type dialRes struct {
		conn net.Conn
		err  error
	}
	dialCh := make(chan dialRes, 1)
	go func() {
		c, err := bNet.Dial("tcp", target)
		dialCh <- dialRes{c, err}
	}()
	var bConn net.Conn
	select {
	case d := <-dialCh:
		if d.err != nil {
			return fmt.Errorf("body dial %s: %w", target, d.err)
		}
		bConn = d.conn
	case <-time.After(6 * time.Second):
		return fmt.Errorf("body dial %s timed out", target)
	case <-ctx.Done():
		return ctx.Err()
	}
	defer bConn.Close()

	if _, err := bConn.Write([]byte("hello")); err != nil {
		return fmt.Errorf("body write: %w", err)
	}

	type readRes struct {
		buf []byte
		err error
	}
	readCh := make(chan readRes, 1)
	go func() {
		buf := make([]byte, 1024)
		n, err := bConn.Read(buf)
		if n > 0 {
			buf = buf[:n]
		} else {
			buf = nil
		}
		readCh <- readRes{buf, err}
	}()
	select {
	case r := <-readCh:
		if r.err != nil {
			return fmt.Errorf("body read: %w", r.err)
		}
		if got := string(r.buf); got != "pong:hello" {
			return fmt.Errorf("overlay echo got %q, want %q", got, "pong:hello")
		}
		return nil
	case <-time.After(6 * time.Second):
		return errors.New("body read timed out")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// proveDollLink proves Doll Link over the overlay: a real WebSocket upgrade
// from the Body overlay to the Core overlay's WS echo server, a correlated
// event round-trip, and returns the correlation response.
func proveDollLink(t *testing.T, ctx context.Context, log *slog.Logger, tp *m46Topology, label string, eventID string) string {
	t.Helper()
	const attempts = 3
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		corr, err := tryDollLink(ctx, tp, eventID)
		if err != nil {
			lastErr = err
			t.Logf("Doll Link (%s) attempt %d/%d failed: %v", label, attempt, attempts, err)
			time.Sleep(400 * time.Millisecond)
			continue
		}
		if corr != eventID {
			t.Fatalf("Doll Link (%s): correlation ID = %q, want %q", label, corr, eventID)
		}
		t.Logf("Doll Link event round-trip (%s): correlation=%q PASS", label, corr)
		return corr
	}
	t.Fatalf("Doll Link (%s) failed after %d attempts: %v", label, attempts, lastErr)
	return ""
}

func tryDollLink(ctx context.Context, tp *m46Topology, eventID string) (string, error) {
	bNet := tp.bTun.Netstack()
	target := net.JoinHostPort(tp.coreOverlayAddr.String(), "9998")

	type dialRes struct {
		conn net.Conn
		err  error
	}
	dialCh := make(chan dialRes, 1)
	go func() {
		c, err := bNet.Dial("tcp", target)
		dialCh <- dialRes{c, err}
	}()
	var wsTcpConn net.Conn
	select {
	case d := <-dialCh:
		if d.err != nil {
			return "", fmt.Errorf("body WS dial: %w", d.err)
		}
		wsTcpConn = d.conn
	case <-time.After(6 * time.Second):
		return "", fmt.Errorf("body WS dial %s timed out", target)
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer wsTcpConn.Close()

	upgradeReq := fmt.Sprintf("GET /ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", target)
	if _, err := wsTcpConn.Write([]byte(upgradeReq)); err != nil {
		return "", fmt.Errorf("WS upgrade write: %w", err)
	}

	br := bufio.NewReader(wsTcpConn)
	respLine, err := br.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("WS upgrade read status: %w", err)
	}
	respLine = strings.TrimSpace(respLine)
	if len(respLine) < 12 || respLine[:9] != "HTTP/1.1 " || respLine[9:12] != "101" {
		return "", fmt.Errorf("WS upgrade did not return 101, got: %s", respLine)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("WS upgrade read header: %w", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	pingEvent := events.Event{
		Type:      events.TypeMessage,
		Source:    "body",
		ID:        eventID,
		Timestamp: time.Now().UTC(),
		Payload: events.MessagePayload{
			Text: "ping-from-body",
		},
	}
	eventBytes, _ := json.Marshal(pingEvent)
	wsFrame := buildWSTextFrame(eventBytes)
	if _, err := wsTcpConn.Write(wsFrame); err != nil {
		return "", fmt.Errorf("WS write frame: %w", err)
	}

	type readRes struct {
		buf []byte
		err error
	}
	readCh := make(chan readRes, 1)
	go func() {
		readBuf := make([]byte, 4096)
		n, err := wsTcpConn.Read(readBuf)
		if n > 0 {
			readBuf = readBuf[:n]
		} else {
			readBuf = nil
		}
		readCh <- readRes{readBuf, err}
	}()
	var respPayload []byte
	select {
	case r := <-readCh:
		if r.err != nil {
			return "", fmt.Errorf("WS read response: %w", r.err)
		}
		respPayload, err = parseWSFrame(r.buf)
		if err != nil {
			return "", fmt.Errorf("parse WS response frame: %w (raw=%.40q)", err, string(r.buf))
		}
	case <-time.After(6 * time.Second):
		return "", errors.New("WS read response timed out")
	case <-ctx.Done():
		return "", ctx.Err()
	}

	var respEvent events.Event
	if err := json.Unmarshal(respPayload, &respEvent); err != nil {
		return "", fmt.Errorf("unmarshal WS response: %w", err)
	}
	return respEvent.CorrelationID, nil
}

// ── Wait helpers ──────────────────────────────────────────────────────────────

// waitCondition polls cond every 100ms until true or timeout.
func waitCondition(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s after %s", what, timeout)
}

// waitConnected waits for the ControlClient connection state to become want.
func waitConnected(t *testing.T, tp *m46Topology, want bool, timeout time.Duration) {
	t.Helper()
	what := "disconnect detection"
	if want {
		what = "reconnect"
	}
	waitCondition(t, what, timeout, func() bool {
		return tp.ctrlClient.IsConnected() == want
	})
	t.Logf("ControlClient.IsConnected() == %v (%s)", want, what)
}

// waitRouteOpen waits until svc reports route id open with the expected
// endpoint — this proves the client's route was restored after a reconnect.
func waitRouteOpen(t *testing.T, ctx context.Context, tp *m46Topology, svc *relay.Service, id relay.RouteID, wantEndpoint string, timeout time.Duration) {
	t.Helper()
	what := fmt.Sprintf("route %d open with endpoint %s", id, wantEndpoint)
	waitCondition(t, what, timeout, func() bool {
		r, ok := svc.Registry().Route(id)
		if !ok {
			return false
		}
		return r.State == relay.RouteStateOpen && r.Endpoint == wantEndpoint
	})
	t.Logf("Route %d restored: state=open endpoint=%s", id, wantEndpoint)
}

// waitRestoredRoutes polls until RoutesRestored >= minRoutes or the context
// expires.  This is needed because RoutesRestored is incremented
// asynchronously on the control client when it processes RouteOpened
// — the server-side route registry (checked by waitRouteOpen) may
// indicate the route is open before the counter is updated.
func waitRestoredRoutes(t *testing.T, ctx context.Context, tp *m46Topology, minRoutes int, timeout time.Duration) {
	t.Helper()
	what := fmt.Sprintf("RoutesRestored >= %d", minRoutes)
	waitCondition(t, what, timeout, func() bool {
		return tp.ctrlClient.RoutesRestored.Load() >= int64(minRoutes)
	})
	t.Logf("RoutesRestored=%d (wanted >=%d)", tp.ctrlClient.RoutesRestored.Load(), minRoutes)
}
