//go:build e2e

// Core 4 / M5.6b — Hostile-network conformance.
//
// Prove the M5 initial path-selection behaviour:
//   Direct WG UDP unavailable
//   → Relay WG UDP unavailable
//   → Body.SelectInitialPath
//   → Relay WSS/TLS succeeds
//   → real WG handshake
//   → bidirectional overlay traffic.
//
// The same Body/WireGuard identity is retained across all failed attempts and the
// successful WSS fallback.
//
// No M6 background probing, roaming, path promotion, or ongoing reconciliation.

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

// ── helpers ────────────────────────────────────────────────────────────

// clampPrivateKey applies Curve25519 clamping to a 32-byte private key.
// WireGuard's device.SetPrivateKey does this internally; we apply it here
// so we can compare the key we pass in with what IpcGet returns.
func clampPrivateKey(key [32]byte) [32]byte {
	key[0] &= 0xF8
	key[31] &= 0x7F
	key[31] |= 0x40
	return key
}

// ── M5.6b conformance test ─────────────────────────────────────────────

func TestM56b_HostileNetworkConformance(t *testing.T) {
	// ── Predefined test identifiers ──
	const routeID = relay.RouteID(42)
	const token = "m56b-test-token"
	const routeCred = "m56b-wss-cred"

	// ── Key material ──
	coreKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)

	// Convert hex keys to [32]byte for the path selector.
	bodyPrivKey, err := hexToBytes(bodyKeys.privHex)
	if err != nil {
		t.Fatalf("body priv hex: %v", err)
	}
	corePubKey, err := hexToBytes(coreKeys.pubHex)
	if err != nil {
		t.Fatalf("core pub hex: %v", err)
	}

	// ── 1. Start Relay Service + ControlServer ──
	svcCfg := relay.DefaultServiceConfig()
	hash := sha256.Sum256([]byte(token))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svcCfg.KeepaliveInterval = 2 * time.Second
	svcCfg.RouteTimeout = 180 * time.Second
	svcCfg.RegistrationTimeout = 180 * time.Second

	svc, svcErr := relay.NewService(svcCfg, relay.ClientConfig{})
	if svcErr != nil {
		t.Fatalf("NewService: %v", svcErr)
	}

	svcCtx, svcCancel := context.WithCancel(context.Background())
	defer svcCancel()
	if err := svc.Start(svcCtx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}

	csAddr := pickFreeTCPAddrPort(t)
	cs := relay.NewControlServer(svc, csAddr, nil)
	csCtx, csCancel := context.WithCancel(svcCtx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	// ── 2. Core side: ControlClient + RelayTransport ──
	cfg := relay.DefaultClientConfig()
	cfg.RelayURL = "ws://" + csAddr + "/relay"
	cfg.RegistrationToken = token
	cfg.HandshakeTimeout = 5 * time.Second
	cfg.ReadTimeout = 15 * time.Second
	cfg.PingInterval = 5 * time.Second

	client := relay.NewControlClient(cfg)
	if err := client.Start(svcCtx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	waitClientConnected(t, client, "core-client")

	// ── 3. Open route through the ControlClient ──
	opened, openErr := client.OpenRoute(svcCtx, routeID, relay.RouteCredentials{Token: routeCred})
	if openErr != nil {
		t.Fatalf("OpenRoute(%d): %v", routeID, openErr)
	}

	// Set route credentials on the registry.
	regID, ok := svc.Registry().RouteRegistration(routeID)
	if !ok {
		t.Fatalf("RouteRegistration(%d): not found", routeID)
	}
	if err := svc.Registry().SetRouteCredentials(regID, routeID, relay.RouteCredentials{Token: routeCred}); err != nil {
		t.Fatalf("SetRouteCredentials: %v", err)
	}

	// Create RelayTransport for the Core WG device.
	tr := relay.NewRelayTransport(client)
	defer tr.Close()
	_, _, trOpenErr := tr.Open(0)
	if trOpenErr != nil {
		t.Fatalf("tr.Open: %v", trOpenErr)
	}

	_ = opened // RouteOpened not needed

	// ── 4. Generate TLS cert and start TLS proxy ──
	cert := generateSelfSignedCert(t)

	proxy, proxyErr := newTLSProxy(svcCtx, csAddr, cert)
	if proxyErr != nil {
		t.Fatalf("newTLSProxy: %v", proxyErr)
	}
	proxyAddr := proxy.Addr()
	defer proxy.Close()

	// ── 5. Start Core WG device ──
	coreOverlay := netip.MustParseAddr("fd00::1")
	bodyOverlay := netip.MustParseAddr("fd00::2")

	// The Core's WG device uses RelayTransport as its bind, so the endpoint
	// string is symbolic.
	endpoint := "relay:1"
	coreDev, coreNet := startWgDevice(t, tr, coreKeys, coreOverlay, bodyKeys.pubHex, endpoint, "fd00::2/128")

	// ── 6. Configure and run Body.SelectInitialPath ──
	//
	// The first two paths (direct-wg, relay-wg) point at dead UDP ports
	// so the selector falls through to relay-wss.
	ps := body.NewPathSelector(slog.Default())

	selCtx, selCancel := context.WithTimeout(svcCtx, 30*time.Second)
	defer selCancel()

	result := ps.SelectInitialPath(selCtx, bodyPrivKey, body.PathSelectorConfig{
		CorePublicKey:      corePubKey,
		DirectEndpoint:     "127.0.0.1:1",
		RelayWGUDPEndpoint: "127.0.0.1:2",
		RelayWSSURL:        "wss://" + proxyAddr,
		RouteID:            routeID,
		RouteCredential:    routeCred,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		HandshakeTimeout: 10 * time.Second,
	})
	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v", result.Err)
	}
	if result.Path != "relay-wss" {
		t.Fatalf("expected relay-wss path, got %q", result.Path)
	}
	t.Logf("SelectInitialPath chose %q (expected relay-wss)", result.Path)

	// ── 7. Verify identity retained ──
	// IpcGet returns the device's private key (clamped).  Compare against
	// the clamped input.
	ipc, ipcErr := result.Tunnel.IpcGet()
	if ipcErr != nil {
		t.Fatalf("IpcGet: %v", ipcErr)
	}
	bodyPrivClamped := clampPrivateKey(bodyPrivKey)
	if !containsPrivateKey(ipc, bodyPrivClamped) {
		t.Fatalf("identity changed: tunnel private_key does not match input body key")
	}
	t.Log("identity retained ✓")

	// ── 8. Verify WG handshake completed ──
	// Wait for the Core side to show a handshake.
	waitHandshake(t, coreDev, "Core WG device", 15*time.Second)

	// The Body's tunnel device is accessible via the PathTunnel interface.
	// The tunnel's Start() already called dev.Up(), so WireGuard is actively
	// sending handshake initiations.  We verify by reading the Core's
	// established-peer state.
	coreIpCut, _ := coreDev.IpcGet()
	t.Logf("Core IpcGet has handshake: %v", containsHandshake(coreIpCut))

	// ── 9. Prove bidirectional overlay traffic ──
	ping := []byte("m56b-ping-from-core")
	pong := []byte("m56b-pong-from-body")

	// Body listens on its netstack at fd00::2:7.
	bodyListen := netip.AddrPortFrom(bodyOverlay, 7)
	bodyNet := result.Tunnel.Netstack()
	bodyConn, listenErr := bodyNet.ListenUDPAddrPort(bodyListen)
	if listenErr != nil {
		t.Fatalf("bodyNet.ListenUDP(%s): %v", bodyListen, listenErr)
	}
	defer bodyConn.Close()

	// Core listens on its netstack at fd00::1:7.
	coreListen := netip.AddrPortFrom(coreOverlay, 7)
	coreConn, listenErr2 := coreNet.ListenUDPAddrPort(coreListen)
	if listenErr2 != nil {
		t.Fatalf("coreNet.ListenUDP(%s): %v", coreListen, listenErr2)
	}
	defer coreConn.Close()

	// Core → Body: dial from Core's netstack to Body's WG address.
	coreSender, dialErr := coreNet.DialUDPAddrPort(netip.AddrPort{}, bodyListen)
	if dialErr != nil {
		t.Fatalf("coreNet.DialUDP(%s): %v", bodyListen, dialErr)
	}
	n, writeErr := coreSender.Write(ping)
	if writeErr != nil {
		t.Fatalf("core→body write: %v", writeErr)
	}
	if n <= 0 {
		t.Fatalf("core→body wrote %d bytes (expected >0)", n)
	}

	buf := make([]byte, 1500)
	n, _, readErr := bodyConn.ReadFrom(buf)
	if readErr != nil {
		t.Fatalf("body read: %v", readErr)
	}
	got := buf[:n]
	if !bytes.Equal(got, ping) {
		t.Fatalf("body received %x, want %x", got, ping)
	}
	t.Logf("bidirectional: core→body ping received ✓")

	// Body → Core (reverse): dial from Body's netstack to Core's WG address.
	bodySender, dialErr2 := bodyNet.DialUDPAddrPort(netip.AddrPort{}, coreListen)
	if dialErr2 != nil {
		t.Fatalf("bodyNet.DialUDP(%s): %v", coreListen, dialErr2)
	}
	n2, writeErr2 := bodySender.Write(pong)
	if writeErr2 != nil {
		t.Fatalf("body→core write: %v", writeErr2)
	}
	if n2 <= 0 {
		t.Fatalf("body→core wrote %d bytes (expected >0)", n2)
	}

	buf2 := make([]byte, 1500)
	n2, _, readErr2 := coreConn.ReadFrom(buf2)
	if readErr2 != nil {
		t.Fatalf("core read: %v", readErr2)
	}
	got2 := buf2[:n2]
	if !bytes.Equal(got2, pong) {
		t.Fatalf("core received %x, want %x", got2, pong)
	}
	t.Log("bidirectional: core↔body ping/pong ✓")
}

// ── helpers ────────────────────────────────────────────────────────────

func hexToBytes(s string) ([32]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], b)
	return out, nil
}

// containsPrivateKey checks whether `ipc` output contains the given
// clamped private key as the device's private_key value.
func containsPrivateKey(ipc string, key [32]byte) bool {
	raw := hex.EncodeToString(key[:])
	for _, line := range bytes.Split([]byte(ipc), []byte("\n")) {
		if bytes.HasPrefix(line, []byte("private_key=")) {
			return bytes.Contains(line, []byte(raw))
		}
	}
	return false
}

// containsHandshake checks whether `ipc` output contains an established
// handshake (non-zero, non-"none" handshake time) for any peer.
func containsHandshake(ipc string) bool {
	for _, line := range bytes.Split([]byte(ipc), []byte("\n")) {
		trim := bytes.TrimSpace(line)
		// IpcGet shows per-peer fields; a handshake appears like:
		//  public_key=<hex>
		//  preshared_key=…
		//  protocol_version=1
		//  endpoint=…
		//  last_handshake_time_sec=1234
		//  last_handshake_time_nsec=567
		//  tx_bytes=…
		//  rx_bytes=…
		if bytes.HasPrefix(trim, []byte("last_handshake_time_sec=")) {
			val := string(bytes.TrimPrefix(trim, []byte("last_handshake_time_sec=")))
			if val != "0" {
				return true
			}
		}
	}
	return false
}
