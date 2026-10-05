// SPDX-License-Identifier: AGPL-3.0-only
//go:build e2e

package integration

import (
	"context"
	"encoding/hex"
	"log"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	body "github.com/Neon-Dolls/neondoll/Body"
)

// ── Core device helpers ───────────────────────────────────────────────────────

// startCoreUDPDevice creates a WireGuard device listening on a random UDP port.
// The device uses overlay fd00::1 and only expects one peer (the Body under test).
// Returns the device and the assigned listen port.
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
	dev := device.NewDevice(tun, bind, device.NewLogger(device.LogLevelError, "wg-core-test: "))

	uapi := "private_key=" + keys.privHex + "\n"
	uapi += "listen_port=0\n"
	if err := dev.IpcSet(uapi); err != nil {
		t.Fatalf("IpcSet(private_key+listen_port): %v", err)
	}

	peer := "public_key=" + peerPub + "\n"
	peer += "allowed_ip=fd00::2/128\n"
	peer += "persistent_keepalive_interval=1\n"
	if err := dev.IpcSet(peer); err != nil {
		t.Fatalf("IpcSet(peer): %v", err)
	}

	if err := dev.Up(); err != nil {
		t.Fatalf("Up(): %v", err)
	}

	// Read back the assigned port via IPC Get
	out, err := dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	port := parseListenPort(t, out)

	t.Cleanup(func() { dev.Close() })

	return dev, port
}

// parseListenPort extracts the listen_port value from a WireGuard IPC Get response.
func parseListenPort(t *testing.T, ipcOut string) int {
	t.Helper()
	for _, line := range strings.Split(ipcOut, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "listen_port=") {
			ps := strings.TrimPrefix(line, "listen_port=")
			port, err := strconv.Atoi(ps)
			if err != nil {
				t.Fatalf("parse listen_port %q: %v", ps, err)
			}
			return port
		}
	}
	t.Fatalf("listen_port not found in IpcGet output:\n%s", ipcOut)
	return 0
}

// hexToKey converts a hex string (64 hex chars) to a [32]byte WireGuard key.
func hexToKey(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q): %v", s, err)
	}
	if len(b) != 32 {
		t.Fatalf("hex key %q has %d bytes, want 32", s, len(b))
	}
	var k [32]byte
	copy(k[:], b)
	return k
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestPathSelect_DirectSuccess(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)
	defer coreDev.Close()

	logger := slog.New(slog.NewTextHandler(log.Writer(), nil))

	cfg := body.PathSelectorConfig{
		PrivateKey:        hexToKey(t, bodyKeys.privHex),
		CorePublicKey:     hexToKey(t, coreKeys.pubHex),
		OverlayAddress:    netip.MustParseAddr("fd00::2"),
		OverlayPrefix:     netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:    "127.0.0.1:" + strconv.Itoa(corePort),
		PerAttemptTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err != nil {
		t.Fatalf("SelectInitialPath returned error: %v", result.Err)
	}
	if result.Tunnel == nil {
		t.Fatal("SelectInitialPath returned nil Tunnel on success")
	}
	if result.Path != "direct" {
		t.Fatalf("expected path 'direct', got %q", result.Path)
	}
	if result.BoundBind != nil {
		t.Fatal("SelectInitialPath returned non-nil BoundBind for direct path")
	}

	result.Tunnel.Stop()
}

func TestPathSelect_DirectFailRelayUDPSuccess(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)
	defer coreDev.Close()

	logger := slog.New(slog.NewTextHandler(log.Writer(), nil))

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToKey(t, coreKeys.pubHex),
		OverlayAddress:     netip.MustParseAddr("fd00::2"),
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     "127.0.0.1:1", // dead port
		RelayWGUDPEndpoint: "127.0.0.1:" + strconv.Itoa(corePort),
		PerAttemptTimeout:  2 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err != nil {
		t.Fatalf("SelectInitialPath returned error: %v", result.Err)
	}
	if result.Tunnel == nil {
		t.Fatal("SelectInitialPath returned nil Tunnel on success")
	}
	if result.Path != "relay-udp" {
		t.Fatalf("expected path 'relay-udp', got %q", result.Path)
	}

	result.Tunnel.Stop()
}

func TestPathSelect_AllPathsFail(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	logger := slog.New(slog.NewTextHandler(log.Writer(), nil))

	cfg := body.PathSelectorConfig{
		PrivateKey:         hexToKey(t, bodyKeys.privHex),
		CorePublicKey:      hexToKey(t, coreKeys.pubHex),
		OverlayAddress:     netip.MustParseAddr("fd00::2"),
		OverlayPrefix:      netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:     "127.0.0.1:1",
		RelayWGUDPEndpoint: "127.0.0.1:2",
		PerAttemptTimeout:  2 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(result.Err.Error(), "all paths failed") {
		t.Fatalf("expected 'all paths failed' error, got: %v", result.Err)
	}
	if result.Tunnel != nil {
		t.Fatal("expected nil Tunnel when all paths fail")
	}
}

func TestPathSelect_IdentityDoesNotChange(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	coreDev, corePort := startCoreUDPDevice(t, coreKeys, bodyKeys.pubHex)
	defer coreDev.Close()

	// Capture the key bytes before path selection
	cfgPriv := hexToKey(t, bodyKeys.privHex)
	cfgCorePub := hexToKey(t, coreKeys.pubHex)

	logger := slog.New(slog.NewTextHandler(log.Writer(), nil))

	cfg := body.PathSelectorConfig{
		PrivateKey:        cfgPriv,
		CorePublicKey:     cfgCorePub,
		OverlayAddress:    netip.MustParseAddr("fd00::2"),
		OverlayPrefix:     netip.MustParsePrefix("fd00::/64"),
		DirectEndpoint:    "127.0.0.1:" + strconv.Itoa(corePort),
		PerAttemptTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := body.SelectInitialPath(ctx, cfg, logger)
	if result.Err != nil {
		t.Fatalf("SelectInitialPath failed: %v", result.Err)
	}
	defer result.Tunnel.Stop()

	// Config's identity fields must be unchanged after selection
	if cfg.PrivateKey != cfgPriv {
		t.Fatal("PrivateKey changed in config after path selection")
	}
	if cfg.CorePublicKey != cfgCorePub {
		t.Fatal("CorePublicKey changed in config after path selection")
	}
}
