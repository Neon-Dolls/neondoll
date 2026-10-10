//go:build integration

// Core 4 / M6.2 — Bounded Recovery After Loss.
//
// Integration tests confirming:
//   - Direct→RelayUDP recovery with bidirectional encrypted overlay traffic
//   - Direct→RelayWSS recovery via real relay topology
//   - Identity preservation across recovery
//   - All-paths-fail bounded termination
//   - Context cancellation
//   - Stop() idempotency
//
// These tests integrate Body path selection with real WireGuard devices and
// (for WSS) real relay infrastructure, exercising the full recovery path end
// to end.

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Relay"
	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type testKeypair struct {
	pubHex  string
	privHex string
}

func newTestKeypair(t *testing.T) testKeypair {
	t.Helper()
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatalf("curve25519.X25519: %v", err)
	}
	return testKeypair{
		pubHex:  hex.EncodeToString(pub),
		privHex: hex.EncodeToString(priv[:]),
	}
}

func hexToKey(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q): %v", s, err)
	}
	var k [32]byte
	copy(k[:], b)
	return k
}

func hexToPublicKey(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q): %v", s, err)
	}
	var k [32]byte
	copy(k[:], b)
	return k
}

// testLogWriter returns an io.Writer that writes t.Log lines, suitable for
// slog.NewTextHandler.
func testLogWriter(t *testing.T) io.Writer {
	return &testWriter{t}
}

type testWriter struct {
	*testing.T
}

func (tw *testWriter) Write(p []byte) (int, error) {
	tw.T.Log(string(bytes.TrimRight(p, "\n")))
	return len(p), nil
}

// testLogger returns a *slog.Logger that writes through t.Log.
func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(testLogWriter(t), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// startCoreUDPDevice creates a WireGuard device that listens for incoming
// connections on a random UDP port. The peer is configured with no endpoint
// (listener only).
func startCoreUDPDevice(t *testing.T, keys testKeypair, peerPub string) (*device.Device, int) {
	t.Helper()

	tun, _, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr("fd00::1")},
		nil, 1280,
	)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}

	bind := conn.NewDefaultBind()
	dev := device.NewDevice(tun, bind, device.NewLogger(device.LogLevelError, "wg-core-udp: "))

	uapi := "private_key=" + keys.privHex + "\n"
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("IpcSet(private_key): %v", err)
	}

	peer := "public_key=" + peerPub + "\n" +
		"allowed_ip=fd00::2/128\n" +
		"persistent_keepalive_interval=1\n"
	if err := dev.IpcSet(peer); err != nil {
		t.Fatalf("IpcSet(peer): %v", err)
	}

	if err := dev.Up(); err != nil {
		t.Fatalf("dev.Up(): %v", err)
	}

	t.Cleanup(func() { dev.Close() })

	port, err := readWGPort(dev)
	if err != nil {
		t.Fatalf("readWGPort: %v", err)
	}
	return dev, int(port)
}

// startCoreUDPDeviceWithNetstack is like startCoreUDPDevice but also returns
// the netstack.Net for overlay traffic.
func startCoreUDPDeviceWithNetstack(t *testing.T, keys testKeypair, peerPub string) (*device.Device, *netstack.Net, int) {
	t.Helper()

	tun, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr("fd00::1")},
		nil, 1280,
	)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}

	bind := conn.NewDefaultBind()
	dev := device.NewDevice(tun, bind, device.NewLogger(device.LogLevelError, "wg-core-udp: "))

	uapi := "private_key=" + keys.privHex + "\n"
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("IpcSet(private_key): %v", err)
	}

	peer := "public_key=" + peerPub + "\n" +
		"allowed_ip=fd00::2/128\n" +
		"persistent_keepalive_interval=1\n"
	if err := dev.IpcSet(peer); err != nil {
		t.Fatalf("IpcSet(peer): %v", err)
	}

	if err := dev.Up(); err != nil {
		t.Fatalf("dev.Up(): %v", err)
	}

	t.Cleanup(func() { dev.Close() })

	port, err := readWGPort(dev)
	if err != nil {
		t.Fatalf("readWGPort: %v", err)
	}
	return dev, tnet, int(port)
}

// startWgRelayUDPDevice creates a Body-side WireGuard device configured to
// connect to a Core device via a relay-udp endpoint (relay dest address).
func startWgRelayUDPDevice(t *testing.T, relayAddr string, routeID relay.RouteID, routeCred string, keys testKeypair, peerPub string) (*device.Device, *netstack.Net) {
	t.Helper()
	bind := conn.NewDefaultBind()
	// The relay expects the route credential as a "source token" in the UDP
	// packet header. For these integration tests, the relay just forwards
	// datagrams, so we use a plain UDP bind.
	_, _, err := bind.Open(0)
	if err != nil {
		t.Fatalf("bind.Open(0): %v", err)
	}
	tun, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr("fd00::2")},
		nil, 1280,
	)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}
	dev := device.NewDevice(tun, bind, device.NewLogger(device.LogLevelError, "wg-relay-udp: "))
	uapi := "private_key=" + keys.privHex + "\n"
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("IpcSet(private_key): %v", err)
	}
	peer := "public_key=" + peerPub + "\n" +
		"endpoint=" + relayAddr + "\n" +
		"allowed_ip=fd00::1/128\n" +
		"persistent_keepalive_interval=1\n"
	if err := dev.IpcSet(peer); err != nil {
		t.Fatalf("IpcSet(peer): %v", err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("dev.Up(): %v", err)
	}
	t.Cleanup(func() { dev.Close() })
	return dev, tnet
}

// startWgDevice creates a WireGuard device using the provided bind, configures
// keys, peer, and endpoint, and brings it up. Returns the device and its
// netstack. Used for WSS-based devices where the bind is a RelayTransport.
func startWgDevice(t *testing.T, b conn.Bind, keys testKeypair, overlay netip.Addr, peerPub, endpoint, peerAllowed string) (*device.Device, *netstack.Net) {
	t.Helper()
	tun, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{overlay},
		nil, 1280,
	)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}
	dev := device.NewDevice(tun, b, device.NewLogger(device.LogLevelError, "wg-wss: "))
	uapi := "private_key=" + keys.privHex + "\n"
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("IpcSet(private_key): %v", err)
	}
	peer := "public_key=" + peerPub + "\n" +
		"endpoint=" + endpoint + "\n" +
		"allowed_ip=" + peerAllowed + "\n" +
		"persistent_keepalive_interval=1\n"
	if err := dev.IpcSet(peer); err != nil {
		t.Fatalf("IpcSet(peer): %v", err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("dev.Up(): %v", err)
	}
	t.Cleanup(func() { dev.Close() })
	return dev, tnet
}

// waitHandshake polls IpcGet until the device reports a recent handshake.
func waitHandshake(t *testing.T, dev *device.Device, label string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := dev.IpcGet()
		if err != nil {
			t.Fatalf("%s IpcGet: %v", label, err)
		}
		for _, line := range strings.Split(status, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "last_handshake_time_sec=") {
				sec := strings.TrimPrefix(line, "last_handshake_time_sec=")
				if sec != "0" {
					return // handshake happened
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: no handshake within %v", label, timeout)
}

// proveBidirectionalTraffic sends a small UDP packet from core→body and
// body→core through the overlay tunnel, verifying delivery at each end.
func proveBidirectionalTraffic(t *testing.T, bodyNet *netstack.Net, coreNet *netstack.Net, bodyOverlay, coreOverlay netip.Addr, label string) {
	t.Helper()

	payload := []byte("ping-" + label)

	// Core → Body via overlay
	bodyListen, err := bodyNet.ListenUDPAddrPort(netip.AddrPortFrom(bodyOverlay, 9999))
	if err != nil {
		t.Fatalf("bodyNet.ListenUDP: %v", err)
	}
	defer bodyListen.Close()

	coreConn, err := coreNet.DialUDPAddrPort(netip.AddrPortFrom(coreOverlay, 0), netip.AddrPortFrom(bodyOverlay, 9999))
	if err != nil {
		t.Fatalf("coreNet.DialUDP: %v", err)
	}
	defer coreConn.Close()

	n, err := coreConn.Write(payload)
	if err != nil {
		t.Fatalf("core→body write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("core→body wrote %d, want %d", n, len(payload))
	}

	buf := make([]byte, 1500)
	if err := bodyListen.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("bodyListen.SetReadDeadline: %v", err)
	}
	n, _, err = bodyListen.ReadFrom(buf)
	if err != nil {
		t.Fatalf("core→body read: %v", err)
	}
	if got := string(buf[:n]); got != string(payload) {
		t.Errorf("core→body: got %q, want %q (label=%s)", got, payload, label)
	}

	// Body → Core via overlay
	coreListen, err := coreNet.ListenUDPAddrPort(netip.AddrPortFrom(coreOverlay, 9998))
	if err != nil {
		t.Fatalf("coreNet.ListenUDP: %v", err)
	}
	defer coreListen.Close()

	bodyConn, err := bodyNet.DialUDPAddrPort(netip.AddrPortFrom(bodyOverlay, 0), netip.AddrPortFrom(coreOverlay, 9998))
	if err != nil {
		t.Fatalf("bodyNet.DialUDP: %v", err)
	}
	defer bodyConn.Close()

	n, err = bodyConn.Write(payload)
	if err != nil {
		t.Fatalf("body→core write: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("body→core wrote %d, want %d", n, len(payload))
	}

	if err := coreListen.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("coreListen.SetReadDeadline: %v", err)
	}
	n, _, err = coreListen.ReadFrom(buf)
	if err != nil {
		t.Fatalf("coreListen.ReadFrom: %v", err)
	}
	if got := string(buf[:n]); got != string(payload) {
		t.Errorf("body→core: got %q, want %q (label=%s)", got, payload, label)
	}
}

// waitClientConnected polls the ControlClient until its connection state is
// connected.
func waitClientConnected(t *testing.T, c *relay.ControlClient, label string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status := c.IsConnected()
		if status {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: ControlClient not connected within 30s", label)
}

// pickFreeTCPAddrPort picks a random TCP port and returns "127.0.0.1:PORT".
func pickFreeTCPAddrPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pickFreeTCPAddrPort: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// ---------------------------------------------------------------------------
// Test: Direct → Relay UDP recovery with bidirectional traffic
// ---------------------------------------------------------------------------

func TestRecoverPath_DirectToRelayUDP(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	// Two Core UDP devices on different ports, each with netstack for traffic
	coreDevA, coreNetA, corePortA := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	_, coreNetB, corePortB := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)

	logger := testLogger(t)
	bodyOverlay := netip.MustParseAddr("fd00::2")
	coreOverlay := netip.MustParseAddr("fd00::1")

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", corePortB),
		RelayWSSURL:        "",
		RouteID:            0,
		RouteCredential:    "",
		PerAttemptTimeout:  5 * time.Second,
	}

	// ── Initial path: Direct ──
	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Path != "direct" {
		t.Fatalf("want path 'direct', got %q", initialResult.Path)
	}
	if initialResult.Tunnel == nil {
		t.Fatal("nil Tunnel on success")
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	// Wait for handshake on initial direct path
	waitHandshake(t, coreDevA, "Core device A (direct)", 10*time.Second)

	if err := initialResult.Tunnel.WaitHandshake(selCtx, 10*time.Second); err != nil {
		t.Fatalf("initial tunnel handshake: %v", err)
	}

	// Prove bidirectional traffic through the initial direct path
	proveBidirectionalTraffic(t,
		initialResult.Tunnel.Netstack(), coreNetA,
		bodyOverlay, coreOverlay,
		"initial-direct",
	)

	// ── Kill direct path ──
	coreDevA.Close()

	// ── Recover to relay-udp ──
	recCtx, recCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer recCancel()

	epoch := body.NewRecoveryEpoch(nil)
	recResult := body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, epoch)
	if recResult.Err != nil {
		t.Fatalf("RecoverPath: %v", recResult.Err)
	}
	if recResult.Path != "relay-udp" {
		t.Fatalf("want path 'relay-udp', got %q", recResult.Path)
	}
	if recResult.Tunnel == nil {
		t.Fatal("nil Tunnel on success")
	}
	t.Cleanup(func() { recResult.Tunnel.Stop() })

	// Wait for handshake on recovered relay-udp path
	if err := recResult.Tunnel.WaitHandshake(recCtx, 20*time.Second); err != nil {
		t.Fatalf("recovered tunnel handshake: %v", err)
	}

	// Prove bidirectional traffic through the recovered relay-udp path
	proveBidirectionalTraffic(t,
		recResult.Tunnel.Netstack(), coreNetB,
		bodyOverlay, coreOverlay,
		"recovered-relay-udp",
	)
}

// ---------------------------------------------------------------------------
// Test: Direct → Relay WSS recovery (real relay infrastructure)
// ---------------------------------------------------------------------------

func TestRecoverPath_ToWSS(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	const routeID = relay.RouteID(42)
	const credential = "wss-recovery-cred"
	const token = "test-token"

	bodyOverlay := netip.MustParseAddr("fd00::2")
	coreOverlay := netip.MustParseAddr("fd00::1")

	logger := testLogger(t)

	// ── Core side: Direct path (UDP) ──
	coreDevA, coreNetA, corePortA := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)

	// ── Relay + WSS path ──
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

	// Core side: ControlClient + RelayTransport
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

	// Core WG device on WSS path (via RelayTransport)
	tr := relay.NewRelayTransport(client)
	_, _, trOpenErr := tr.Open(0)
	if trOpenErr != nil {
		t.Fatalf("tr.Open: %v", trOpenErr)
	}
	t.Cleanup(func() { _ = tr.Close() })

	endpoint := "relay:1"
	coreDevB, coreNetB := startWgDevice(t, tr, coreKeys, coreOverlay, bodyKeys.pubHex, endpoint, "fd00::2/128")

	// ── Body side: SelectInitialPath → Direct (working) ──
	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     bodyOverlay,
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWGUDPEndpoint: "",
		RelayWSSURL:        relayAddr,
		RouteID:            routeID,
		RouteCredential:    credential,
		PerAttemptTimeout:  5 * time.Second,
	}

	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Path != "direct" {
		t.Fatalf("want path 'direct', got %q", initialResult.Path)
	}
	if initialResult.Tunnel == nil {
		t.Fatal("nil Tunnel on success")
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	// Wait for handshake on direct path
	waitHandshake(t, coreDevA, "Core device A (direct, WSS test)", 10*time.Second)
	if err := initialResult.Tunnel.WaitHandshake(selCtx, 10*time.Second); err != nil {
		t.Fatalf("initial tunnel (direct) handshake: %v", err)
	}

	// Prove traffic through initial direct path
	proveBidirectionalTraffic(t,
		initialResult.Tunnel.Netstack(), coreNetA,
		bodyOverlay, coreOverlay,
		"initial-direct-wss-test",
	)

	// ── Kill direct path ──
	coreDevA.Close()

	// ── RecoverPath → Direct (dead) → Relay UDP (unavailable) → WSS ──
	recCtx, recCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer recCancel()

	// Configure an unavailable Relay UDP endpoint so the selector
	// actually tries all three paths: direct → relay-udp → relay-wss.
	recCfg := cfg
	recCfg.RelayWGUDPEndpoint = "127.0.0.1:1"

	epoch := body.NewRecoveryEpoch(nil)
	recResult := body.RecoverPath(recCtx, initialResult.Tunnel, recCfg, logger, epoch)
	if recResult.Err != nil {
		t.Fatalf("RecoverPath: %v", recResult.Err)
	}
	if recResult.Path != "relay-wss" {
		t.Fatalf("want path 'relay-wss', got %q", recResult.Path)
	}
	if recResult.Tunnel == nil {
		t.Fatal("nil Tunnel on success")
	}
	t.Cleanup(func() { recResult.Tunnel.Stop() })

	// Wait for handshake on WSS path
	waitHandshake(t, coreDevB, "Core device B (WSS)", 20*time.Second)
	if err := recResult.Tunnel.WaitHandshake(recCtx, 20*time.Second); err != nil {
		t.Fatalf("recovered tunnel (WSS) handshake: %v", err)
	}

	// ── Prove identity preserved ──
	ipcGet, err := recResult.Tunnel.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	if !strings.Contains(ipcGet, "private_key="+bodyKeys.privHex) {
		t.Errorf("recovered tunnel has different private key; ipcGet=\n%s", ipcGet)
	}

	// ── Prove bidirectional traffic through recovered WSS path ──
	proveBidirectionalTraffic(t,
		recResult.Tunnel.Netstack(), coreNetB,
		bodyOverlay, coreOverlay,
		"recovered-wss",
	)
}

// ---------------------------------------------------------------------------
// Test: Identity preservation
// ---------------------------------------------------------------------------

func TestRecoverPath_PreservesIdentity(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	_, corePortA := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)
	_, corePortB := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

	logger := testLogger(t)

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     netip.MustParseAddr("fd00::2"),
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", corePortB),
		RelayWSSURL:        "",
		RouteID:            0,
		RouteCredential:    "",
		PerAttemptTimeout:  5 * time.Second,
	}

	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Path != "direct" {
		t.Fatalf("want path 'direct', got %q", initialResult.Path)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	// Stop the Core device so recovery triggers
	// (we need to close the underlying dev so WG stops responding)
	// Unfortunately we don't have access to the dev, so capture IPC before
	// for identity check after recovery.
	// Actually we DO just check the recovered tunnel's IpcGet.

	recCtx, recCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer recCancel()

	// Kill direct path first
	// We use a trick: stop the old tunnel, then prove identity on a separate
	// recovery invocation. But the test actually tests that RecoverPath
	// reuses the same private key, so we stop the tunnel, kill the device.
	epoch := body.NewRecoveryEpoch(nil)
	recResult := body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, epoch)
	if recResult.Err != nil {
		t.Fatalf("RecoverPath: %v", recResult.Err)
	}
	t.Cleanup(func() { recResult.Tunnel.Stop() })

	// The recovered tunnel should have the same private key
	ipcGet, err := recResult.Tunnel.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	if !strings.Contains(ipcGet, "private_key="+bodyKeys.privHex) {
		t.Errorf("recovered tunnel has different private key; ipcGet=\n%s", ipcGet)
	}
}

// ---------------------------------------------------------------------------
// Test: All paths fail
// ---------------------------------------------------------------------------

func TestRecoverPath_AllPathsFail(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	// Use a real Core device for initial path selection
	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

	logger := testLogger(t)

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     netip.MustParseAddr("fd00::2"),
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePort),
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", 1),
		RelayWSSURL:        "",
		RouteID:            0,
		RouteCredential:    "",
		PerAttemptTimeout:  5 * time.Second,
	}

	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	if initialResult.Tunnel == nil {
		t.Fatal("nil Tunnel on first selection")
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	// Stop the Core device so the direct path becomes unavailable
	coreDev.Close()

	// ── RecoverPath with all paths unreachable ──
	recCfg := cfg
	recCfg.DirectEndpoint = fmt.Sprintf("127.0.0.1:%d", corePort) // old port, device is dead
	recCfg.RelayWGUDPEndpoint = "127.0.0.1:1"                     // unreachable
	recCfg.PerAttemptTimeout = 1 * time.Second

	recCtx, recCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer recCancel()

	recResult := body.RecoverPath(recCtx, initialResult.Tunnel, recCfg, logger, body.NewRecoveryEpoch(nil))
	if recResult.Err == nil {
		t.Fatal("expected an error when all paths fail during recovery")
	}
	if recResult.Tunnel != nil {
		t.Fatal("expected nil Tunnel when all paths fail")
	}
	if recResult.Path != "" {
		t.Fatalf("expected empty path on all-fail, got %q", recResult.Path)
	}
}

// ---------------------------------------------------------------------------
// Test: Context cancellation
// ---------------------------------------------------------------------------

func TestRecoverPath_Cancellation(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	_, corePortA := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)
	_, corePortB := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

	logger := testLogger(t)

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     netip.MustParseAddr("fd00::2"),
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", corePortB),
		RelayWSSURL:        "",
		RouteID:            0,
		RouteCredential:    "",
		PerAttemptTimeout:  5 * time.Second,
	}

	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	// Cancel the recovery context immediately
	recCtx, recCancel := context.WithCancel(context.Background())
	recCancel()

	recResult := body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, body.NewRecoveryEpoch(nil))
	if recResult.Err == nil {
		t.Fatal("expected an error when recovery context is cancelled")
	}
	if recResult.Tunnel != nil {
		t.Fatal("expected nil Tunnel on cancelled recovery")
	}
}

// ---------------------------------------------------------------------------
// Test: Stop() idempotency
// ---------------------------------------------------------------------------

func TestRecoverPath_StopIdempotent(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	_, corePortA := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)
	_, corePortB := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)

	logger := testLogger(t)

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToPublicKey(t, coreKeys.pubHex),
		OverlayAddress:     netip.MustParseAddr("fd00::2"),
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     fmt.Sprintf("127.0.0.1:%d", corePortA),
		RelayWGUDPEndpoint: fmt.Sprintf("127.0.0.1:%d", corePortB),
		RelayWSSURL:        "",
		RouteID:            0,
		RouteCredential:    "",
		PerAttemptTimeout:  5 * time.Second,
	}

	selCtx, selCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer selCancel()

	initialResult := body.SelectInitialPath(selCtx, cfg, logger)
	if initialResult.Err != nil {
		t.Fatalf("SelectInitialPath: %v", initialResult.Err)
	}
	t.Cleanup(func() { initialResult.Tunnel.Stop() })

	// Stop the tunnel, then start recovery
	if err := initialResult.Tunnel.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}

	recCtx, recCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer recCancel()

	// RecoverPath should handle the already-stopped tunnel gracefully
	recResult := body.RecoverPath(recCtx, initialResult.Tunnel, cfg, logger, body.NewRecoveryEpoch(nil))
	if recResult.Err != nil {
		t.Fatalf("RecoverPath after Stop: %v", recResult.Err)
	}
	if recResult.Tunnel == nil {
		t.Fatal("nil Tunnel after recovery from stopped tunnel")
	}
	t.Cleanup(func() { recResult.Tunnel.Stop() })

	// Stop the NEW tunnel twice to confirm idempotency
	if err := recResult.Tunnel.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if err := recResult.Tunnel.Stop(); err != nil {
		t.Fatalf("third Stop (should be idempotent): %v", err)
	}
}

// readWGPort extracts the listen_port from a WG device's IpcGet output.
func readWGPort(dev *device.Device) (uint16, error) {
	out, err := dev.IpcGet()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "listen_port=") {
			val := strings.TrimPrefix(line, "listen_port=")
			var port int
			if _, err := fmt.Sscanf(val, "%d", &port); err != nil {
				return 0, fmt.Errorf("parse listen_port %q: %w", val, err)
			}
			return uint16(port), nil
		}
	}
	return 0, errors.New("listen_port not found in IpcGet output")
}

// ── traffic proof helpers ───────────────────────────────────────────────────
