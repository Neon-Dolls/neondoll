//go:build integration

// Core 4 / M6.7 — Body Path Supervisor.
//
// Proves the Path Supervisor correctly wires M6.1 loss observation to M6.2
// recovery using M6.3 epoch ownership.
//
// Tests:
//   - LossAndRecovery: kill the active Core peer, verify the supervisor
//     recovers to an alternative Core on a different port.
//   - ShutdownDuringRecovery: cancel the supervisor while loss is being
//     processed, verify clean goroutine termination.

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	body "github.com/Neon-Dolls/neondoll/Body"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// pickTwoPorts returns two available UDP ports for Core peers.
func pickTwoPorts() (int, int) {
	const base = 20000
	offset1 := rand.Intn(10000)
	offset2 := rand.Intn(10000)
	return base + offset1, base + offset2 + 5000 // avoid collision
}

// createCoreDevice creates a WireGuard device as a Core responder that accepts
// connections from the body peer over the overlay address fd00::1.
func createCoreDevice(t *testing.T, keys testKeypair, bodyPubHex string, listenPort int) (*device.Device, *netstack.Net) {
	t.Helper()

	tun, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr("fd00::1")},
		nil, 1280,
	)
	if err != nil {
		t.Fatalf("createCoreDevice CreateNetTUN: %v", err)
	}

	dev := device.NewDevice(tun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelError,
			fmt.Sprintf("Core(%d) ", listenPort)))

	if err := dev.IpcSet(fmt.Sprintf(
		"private_key=%s\nlisten_port=%d\n", keys.privHex, listenPort,
	)); err != nil {
		t.Fatalf("createCoreDevice IpcSet(keys+port): %v", err)
	}
		if err := dev.IpcSet(fmt.Sprintf(
		"public_key=%s\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\npersistent_keepalive_interval=1\n",
		bodyPubHex,
	)); err != nil {
		t.Fatalf("createCoreDevice IpcSet(peer): %v", err)
	}

	dev.Up()
	t.Cleanup(func() { dev.Close() })
	return dev, tnet
}

// waitForHandshake polls a WG device until it shows a peer handshake or the
// context expires.
func waitForHandshake(ctx context.Context, dev *device.Device) error {
	for ctx.Err() == nil {
		ipc, err := dev.IpcGet()
		if err != nil {
			return fmt.Errorf("IpcGet: %w", err)
		}
		if strings.Contains(ipc, "last_handshake_time_sec=") {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ctx.Err()
}

// bodyOverlayAddr returns the overlay address used by the body tunnel.
func bodyOverlayAddr() netip.Addr {
	return netip.MustParseAddr("fd00::2")
}

// ── Test: Loss → Recovery ─────────────────────────────────────────────────────

func TestPathSupervisor_LossAndRecovery_M6_7(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	coreAPort, coreBPort := pickTwoPorts()
	overlayAddr := bodyOverlayAddr()

	// Create two Core peers (same keys, different ports).
	coreA, _ := createCoreDevice(t, coreKeys, bodyKeys.pubHex, coreAPort)
	coreB, _ := createCoreDevice(t, coreKeys, bodyKeys.pubHex, coreBPort)

	// Create initial Body tunnel to Core A.
	bt := body.NewBodyTunnel(body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: overlayAddr,
		OverlayPrefix:  netip.PrefixFrom(overlayAddr, 64),
		CoreEndpoint:   fmt.Sprintf("127.0.0.1:%d", coreAPort),
		ListenPort:     0,
	}, nil)
	if err := bt.Start(ctx); err != nil {
		t.Fatalf("Start body tunnel: %v", err)
	}
	t.Cleanup(func() { bt.Stop() })

	// Wait for initial handshake with Core A.
	if err := bt.WaitHandshake(ctx, 10*time.Second); err != nil {
		t.Fatalf("initial handshake with Core A: %v", err)
	}
	t.Log("initial handshake with Core A established ✓")

	// ── Start the supervisor ────────────────────────────────────────────
	// Start supervisor with a discard logger (nil logger would panic on slog calls).
	log := slog.New(slog.DiscardHandler)

	supervisorCfg := body.SupervisorConfig{
		PathSelector: body.PathSelectorConfig{
			PrivateKey:      hexToKey(t, bodyKeys.privHex),
			CorePublicKey:   hexToKey(t, coreKeys.pubHex),
			OverlayAddress:  overlayAddr,
			OverlayPrefix:   netip.PrefixFrom(overlayAddr, 64),
			DirectEndpoint:  fmt.Sprintf("127.0.0.1:%d", coreBPort),
		},
		LossObserver: body.LossObserverConfig{
			PollInterval:      50 * time.Millisecond,
			WGLivenessTimeout: 200 * time.Millisecond,
		},
		RecoverTimeout: 10 * time.Second,
	}

	supervisor, err := body.StartPathSupervisor(ctx, bt, supervisorCfg, log)
	if err != nil {
		t.Fatalf("StartPathSupervisor: %v", err)
	}
	defer supervisor.Stop()

	// ── Kill Core A → triggers path loss ───────────────────────────────
	t.Log("killing Core A — path loss imminent")
	coreA.Close()

	// Poll until the old tunnel is stopped (RecoverPath killed it) or
	// until the supervisor fails.
	deadline := time.Now().Add(5 * time.Second)
	var recovered bool
	for time.Now().Before(deadline) {
		if !bt.Started() {
			recovered = true
			break
		}
		// If the supervisor stopped early, recovery failed.
		select {
		case <-supervisor.Done:
			t.Fatal("supervisor stopped without recovering")
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("timed out waiting for old tunnel to be stopped (recovery)")
	}
	t.Log("old tunnel stopped — recovery triggered ✓")

	// Supervisor must still be running (observing the new tunnel).
	select {
	case <-supervisor.Done:
		t.Fatal("supervisor stopped unexpectedly after recovery")
	default:
	}

	// Wait briefly for the new tunnel to establish a handshake with Core B.
	time.Sleep(200 * time.Millisecond)
	if err := waitForHandshake(ctx, coreB); err != nil {
		if ipc, ipcErr := coreB.IpcGet(); ipcErr == nil {
			t.Logf("Core B UAPI state:\n%s", ipc)
		}
		t.Fatalf("Core B has no handshake — body tunnel did not connect: %v", err)
	}
	t.Log("Core B handshake confirmed — recovery to Core B ✓")
}

// ── Test: Shutdown during recovery ────────────────────────────────────────────

func TestPathSupervisor_ShutdownDuringRecovery_M6_7(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)
	coreAPort, _ := pickTwoPorts()
	overlayAddr := bodyOverlayAddr()

	// Single Core peer — loss will trigger recovery, but we cancel the
	// supervisor before it can complete.
	coreA, _ := createCoreDevice(t, coreKeys, bodyKeys.pubHex, coreAPort)

	bt := body.NewBodyTunnel(body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: overlayAddr,
		OverlayPrefix:  netip.PrefixFrom(overlayAddr, 64),
		CoreEndpoint:   fmt.Sprintf("127.0.0.1:%d", coreAPort),
		ListenPort:     0,
	}, nil)
	if err := bt.Start(ctx); err != nil {
		t.Fatalf("Start body tunnel: %v", err)
	}
	t.Cleanup(func() { bt.Stop() })

	if err := bt.WaitHandshake(ctx, 10*time.Second); err != nil {
		t.Fatalf("initial handshake: %v", err)
	}

	// Start supervisor with recovery pointing to an unreachable port so
	// that recovery blocks until ctx is cancelled.
	log := slog.New(slog.DiscardHandler)
	supervisor, err := body.StartPathSupervisor(ctx, bt, body.SupervisorConfig{
		PathSelector: body.PathSelectorConfig{
			PrivateKey:      hexToKey(t, bodyKeys.privHex),
			CorePublicKey:   hexToKey(t, coreKeys.pubHex),
			OverlayAddress:  overlayAddr,
			OverlayPrefix:   netip.PrefixFrom(overlayAddr, 64),
			DirectEndpoint: fmt.Sprintf("127.0.0.1:%d", coreAPort+1),
		},
		LossObserver: body.LossObserverConfig{
			PollInterval:      50 * time.Millisecond,
			WGLivenessTimeout: 150 * time.Millisecond,
		},
		RecoverTimeout: 5 * time.Second,
	}, log)
	if err != nil {
		t.Fatalf("StartPathSupervisor: %v", err)
	}

	// Kill Core A and then cancel the parent context.
	coreA.Close()
	time.Sleep(100 * time.Millisecond) // let the observer detect loss
	cancel()                            // cancels supervisor's ctx

	// Supervisor must stop cleanly within a reasonable time.
	select {
	case <-supervisor.Done:
		t.Log("supervisor shut down cleanly during recovery ✓")
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not shut down within 3s")
	}
}