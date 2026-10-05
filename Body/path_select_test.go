// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

type testKeypair struct {
	privHex string
	pubHex  string
}

func newTestKeypair(t *testing.T) testKeypair {
	t.Helper()
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pub := priv.PublicKey()
	return testKeypair{
		privHex: hex.EncodeToString(priv.Bytes()),
		pubHex:  hex.EncodeToString(pub.Bytes()),
	}
}

// startCoreWGDevice starts a WireGuard device on the Core side and returns
// its UDP listen port.
func startCoreWGDevice(t *testing.T, coreKeys testKeypair, bodyPubHex string) (uint16, *device.Device) {
	t.Helper()

	tun, _, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr("fd00::1")},
		nil, 1280,
	)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}

	bind := conn.NewDefaultBind()
	logger := device.NewLogger(device.LogLevelError, "core: ")
	dev := device.NewDevice(tun, bind, logger)

	// Set private key (no listen_port — the bind will open on a random port
	// during Up).
	uapi := "private_key=" + coreKeys.privHex + "\n"
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("IpcSet private_key: %v", err)
	}

	// Add Body as a peer — no endpoint, Core learns it from incoming packets.
	peer := "public_key=" + bodyPubHex + "\n" +
		"allowed_ip=fd00::2/128\n" +
		"persistent_keepalive_interval=1\n"
	if err := dev.IpcSet(peer); err != nil {
		t.Fatalf("IpcSet peer: %v", err)
	}

	if err := dev.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	t.Cleanup(func() { dev.Close() })

	// Read the device's listen port via IpcGet.
	port, err := readWGPort(dev)
	if err != nil {
		t.Fatalf("readWGPort: %v", err)
	}
	return port, dev
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

// pathSelectorConfigForTest builds a PathSelectorConfig for test use.
func pathSelectorConfigForTest(t *testing.T, bodyKeys, coreKeys testKeypair, directEp, relayUDPEp, relayWSSURL string, routeID relay.RouteID, credential string) PathSelectorConfig {
	cfg := PathSelectorConfig{
		PrivateKey:    hexToKey(t, bodyKeys.privHex),
		CorePublicKey: hexToKey(t, coreKeys.pubHex),

		DirectEndpoint:     directEp,
		RelayWGUDPEndpoint: relayUDPEp,
		RelayWSSURL:        relayWSSURL,
		RouteID:            routeID,
		RouteCredential:    credential,

		PerAttemptTimeout: 5 * time.Second,

		OverlayAddress: netip.MustParseAddr("fd00::2"),
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
	}
	return cfg
}

// hexToKey decodes a hex-encoded WireGuard key into a [32]byte.
func hexToKey(t *testing.T, s string) [32]byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 32 {
		t.Fatalf("hex key length %d, want 32", len(b))
	}
	var k [32]byte
	copy(k[:], b)
	return k
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestBodyPathSelect_DirectSuccess(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	corePort, _ := startCoreWGDevice(t, coreKeys, bodyKeys.pubHex)
	directEp := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", corePort))

	cfg := pathSelectorConfigForTest(t,
		bodyKeys, coreKeys,
		directEp, "", "", 0, "",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	log := slog.New(slog.DiscardHandler)
	result := SelectInitialPath(ctx, cfg, log)

	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v", result.Err)
	}
	if result.Path != "direct" {
		t.Fatalf("want path 'direct', got %q", result.Path)
	}
	if result.Tunnel == nil {
		t.Fatal("Tunnel is nil on success")
	}
	if result.BoundBind != nil {
		t.Fatal("BoundBind should be nil for direct path")
	}

	// Cleanup
	result.Tunnel.Stop()
}

func TestBodyPathSelect_DirectFailRelayUDPSuccess(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	corePort, _ := startCoreWGDevice(t, coreKeys, bodyKeys.pubHex)

	// Direct endpoint: dead port
	directEp := "127.0.0.1:1"
	// Relay UDP endpoint: Core's real port
	relayUDPEp := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", corePort))

	cfg := pathSelectorConfigForTest(t,
		bodyKeys, coreKeys,
		directEp, relayUDPEp, "", 0, "",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	log := slog.New(slog.DiscardHandler)
	result := SelectInitialPath(ctx, cfg, log)

	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v", result.Err)
	}
	if result.Path != "relay-udp" {
		t.Fatalf("want path 'relay-udp', got %q", result.Path)
	}
	if result.Tunnel == nil {
		t.Fatal("Tunnel is nil on success")
	}

	result.Tunnel.Stop()
}

func TestBodyPathSelect_AllPathsFail(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	// All endpoints dead.
	directEp := "127.0.0.1:1"
	relayUDPEp := "127.0.0.1:2"

	cfg := pathSelectorConfigForTest(t,
		bodyKeys, coreKeys,
		directEp, relayUDPEp, "", 0, "",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	log := slog.New(slog.DiscardHandler)
	result := SelectInitialPath(ctx, cfg, log)

	if result.Err == nil {
		t.Fatal("expected error when all paths fail")
	}
	if !errors.Is(result.Err, ErrAllPathsFailed) {
		t.Fatalf("want ErrAllPathsFailed, got %v", result.Err)
	}
	if result.Tunnel != nil {
		t.Fatal("Tunnel should be nil when all paths fail")
	}
	if result.Path != "" {
		t.Fatalf("Path should be empty, got %q", result.Path)
	}
}

func TestBodyPathSelect_AllPathsFailLeavesNoActiveTunnel(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	directEp := "127.0.0.1:1"
	relayUDPEp := "127.0.0.1:2"

	cfg := pathSelectorConfigForTest(t,
		bodyKeys, coreKeys,
		directEp, relayUDPEp, "", 0, "",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	log := slog.New(slog.DiscardHandler)
	result := SelectInitialPath(ctx, cfg, log)

	if result.Tunnel != nil {
		t.Fatal("failed attempts left an active Tunnel behind")
	}

	// Now try a valid path to confirm state is clean
	corePort, _ := startCoreWGDevice(t, coreKeys, bodyKeys.pubHex)
	directEp = net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", corePort))
	cfg2 := pathSelectorConfigForTest(t,
		bodyKeys, coreKeys,
		directEp, "", "", 0, "",
	)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()
	result2 := SelectInitialPath(ctx2, cfg2, log)

	if result2.Err != nil {
		t.Fatalf("second attempt with valid endpoint: %v", result2.Err)
	}
	if result2.Path != "direct" {
		t.Fatalf("want 'direct', got %q", result2.Path)
	}
	result2.Tunnel.Stop()
}

func TestBodyPathSelect_IdentityDoesNotChange(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	directEp := "127.0.0.1:1"
	relayUDPEp := "127.0.0.1:2"

	cfg := pathSelectorConfigForTest(t,
		bodyKeys, coreKeys,
		directEp, relayUDPEp, "", 0, "",
	)

	// Save originals
	origPriv := cfg.PrivateKey
	origPub := cfg.CorePublicKey

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	log := slog.New(slog.DiscardHandler)
	_ = SelectInitialPath(ctx, cfg, log)

	if cfg.PrivateKey != origPriv {
		t.Fatal("PrivateKey changed after path selection")
	}
	if cfg.CorePublicKey != origPub {
		t.Fatal("CorePublicKey changed after path selection")
	}
}

func TestBodyPathSelect_WSSFallback(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	// Start real Core WG device on UDP
	corePort, _ := startCoreWGDevice(t, coreKeys, bodyKeys.pubHex)
	coreUDPAddr := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", corePort))

	// Start a fake relay that forwards between WSS and Core's UDP
	routeID := relay.RouteID(42)
	fr := newFakeRelay(t, fakeRelayOpts{routeID: routeID})
	srv := httptest.NewServer(fr.handler())
	t.Cleanup(func() { srv.Close() })
	wssURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Timing monitor
	go func() {
		for sec := 1; ; sec++ {
			time.Sleep(1 * time.Second)
			t.Logf("TIMING: T+%ds elapsed", sec)
		}
	}()

	// Start UDP forwarder: captureCh → Core UDP → injectCh
	var (
		wg          sync.WaitGroup
		forwardCtx  context.Context
		forwardCancel context.CancelFunc
	)
	forwardCtx, forwardCancel = context.WithCancel(context.Background())
	defer forwardCancel()

	relayUDPConn, err := net.Dial("udp", coreUDPAddr)
	if err != nil {
		t.Fatalf("net.Dial(udp, %s): %v", coreUDPAddr, err)
	}
	defer relayUDPConn.Close()

	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 1500)
		for {
			select {
			case <-forwardCtx.Done():
				return
			case frameBytes, ok := <-fr.captureCh:
				if !ok {
					return
				}
				frame, err := relay.UnmarshalFrame(frameBytes)
				if err != nil {
					continue
				}
				if _, err := relayUDPConn.Write(frame.Payload); err != nil {
					return
				}
				// Read response from Core
				n, err := relayUDPConn.Read(buf)
				if err != nil {
					return
				}
				respFrame, err := relay.MarshalFrame(&relay.Frame{
					Version: relay.ProtocolVersion,
					RouteID: routeID,
					Payload: buf[:n],
				})
				if err != nil {
					return
				}
				select {
				case fr.injectCh <- respFrame:
				case <-forwardCtx.Done():
					return
				case <-time.After(3 * time.Second):
					return
				}
			}
		}
	}()

	// Path selector config: both UDP paths dead, only WSS works
	directEp := "127.0.0.1:1"
	relayUDPEp := "127.0.0.1:2"

	cfg := PathSelectorConfig{
		PrivateKey:    hexToKey(t, bodyKeys.privHex),
		CorePublicKey: hexToKey(t, coreKeys.pubHex),

		DirectEndpoint:     directEp,
		RelayWGUDPEndpoint: relayUDPEp,
		RelayWSSURL:        wssURL,
		RouteID:            routeID,
		RouteCredential:    "",

		PerAttemptTimeout: 5 * time.Second,

		OverlayAddress: netip.MustParseAddr("fd00::2"),
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	log := slog.New(slog.DiscardHandler)
	result := SelectInitialPath(ctx, cfg, log)

	// Verify WSS path success
	if result.Err != nil {
		t.Fatalf("SelectInitialPath: %v (expected WSS success)", result.Err)
	}
	if result.Path != "relay-wss" {
		t.Fatalf("want path 'relay-wss', got %q", result.Path)
	}
	if result.Tunnel == nil {
		t.Fatal("Tunnel is nil on success")
	}
	if result.BoundBind == nil {
		t.Fatal("BoundBind must be non-nil for relay-wss path")
	}

	// Cleanup
	result.Tunnel.Stop()
	// Signal forwarder to stop, then wait for it
	forwardCancel()
	wg.Wait()
}