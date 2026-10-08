//go:build e2e

// M6.1 — Active Path-Loss Detection.
//
// Proves that the LossObserver correctly detects path loss on an established
// BodyTunnel, covering:
//   - WGLivenessLost: peer disappears, handshake goes stale.
//   - TransportFailure: the tunnel itself dies (IpcGet fails).
//   - CleanTeardown: cancelling the observer context does NOT emit a loss event.
//   - RejectsNoHandshake: StartLossObserver fails if no handshake is recorded.
//
// Observers use channel-based synchronisation (no sleeps in production code).
// Tests wait on the onLoss channel with bounded timeouts.

package integration

import (
	"context"
	"fmt"
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

// ── device helpers ───────────────────────────────────────────────────────────

// wgPortGetter is satisfied by *device.Device for reading IpcGet.
type wgPortGetter interface {
	IpcGet() (string, error)
}

type wgPortReader interface {
	IpcGet() (string, error)
	IpcSet(string) error
}

// readDevicePort returns the listen_port from a device's IpcGet output.
func readDevicePort(t *testing.T, d wgPortGetter) int {
	t.Helper()
	out, err := d.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	const prefix = "listen_port="
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			portStr := strings.TrimPrefix(line, prefix)
			var port int
			if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
				t.Fatalf("parse listen_port %q: %v", portStr, err)
			}
			return port
		}
	}
	t.Fatalf("listen_port not found in IpcGet output:\n%s", out)
	return 0
}

// readBodyTunnelPort returns the listen_port from a BodyTunnel's IpcGet.
func readBodyTunnelPort(t *testing.T, bt *body.BodyTunnel) int {
	t.Helper()
	return readDevicePort(t, bt)
}

// updatePeerEndpoint reconfigures the single peer on device d to the given
// endpoint, so the device knows where to send WG handshake initiations.
func updatePeerEndpoint(t *testing.T, d wgPortReader, bodyPubHex, newEndpoint string) {
	t.Helper()
	out, err := d.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet for endpoint update: %v", err)
	}
	// Extract the existing peer's allowed_ip from IpcGet output.
	var allowedIP string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "allowed_ip=") {
			allowedIP = strings.TrimPrefix(line, "allowed_ip=")
		}
	}
	if allowedIP == "" {
		allowedIP = "fd01::1/128"
	}

	// Replace the peer config with the correct endpoint.
	peerCfg := fmt.Sprintf(
		"public_key=%s\nendpoint=%s\nallowed_ip=%s\npersistent_keepalive_interval=1\n",
		bodyPubHex, newEndpoint, allowedIP,
	)
	t.Logf("updating peer endpoint to %s (bodyPub=%s)", newEndpoint, bodyPubHex[:8]+"…")
	if err := d.IpcSet(peerCfg); err != nil {
		t.Fatalf("IpcSet(peer endpoint): %v", err)
	}
	// Verify the updated endpoint via IpcGet.
	getOut, err := d.IpcGet()
	if err != nil {
		t.Logf("IpcGet after endpoint update failed: %v", err)
	} else if !strings.Contains(getOut, "endpoint="+newEndpoint) {
		t.Logf("WARNING: peer endpoint update to %s may not have applied; IpcGet shows:\n%s", newEndpoint, getOut)
	} else {
		t.Logf("confirmed peer endpoint updated to %s", newEndpoint)
	}
}

// dumpWgState logs the full IpcGet output for both a BodyTunnel and a raw WG device.
func dumpWgState(t *testing.T, label string, bt *body.BodyTunnel, peerDev *device.Device) {
	t.Helper()
	if s, err := bt.IpcGet(); err != nil {
		t.Logf("=== %s BodyTunnel IpcGet ERROR: %v ===", label, err)
	} else {
		t.Logf("=== %s BodyTunnel WG state ===\n%s", label, s)
	}
	if s, err := peerDev.IpcGet(); err != nil {
		t.Logf("=== %s PeerDev IpcGet ERROR: %v ===", label, err)
	} else {
		t.Logf("=== %s PeerDev WG state ===\n%s", label, s)
	}
}

// waitHandshakeTime polls IpcGet on a device until last_handshake_time_sec
// is non-zero, or the given timeout expires.
func waitHandshakeTime(t *testing.T, bt *body.BodyTunnel, peerDev *device.Device, label string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		btOut, btErr := bt.IpcGet()
		pOut, pErr := peerDev.IpcGet()
		if btErr == nil && pErr == nil {
			btOK := strings.Contains(btOut, "last_handshake_time_sec=") &&
				!strings.Contains(btOut, "last_handshake_time_sec=0")
			pOK := strings.Contains(pOut, "last_handshake_time_sec=") &&
				!strings.Contains(pOut, "last_handshake_time_sec=0")
			if btOK && pOK {
				t.Logf("%s: handshake complete (both sides)", label)
				return
			}
			if btOK {
				t.Logf("%s: body handshake OK, peer pending…", label)
			}
			if pOK {
				t.Logf("%s: peer handshake OK, body pending…", label)
			}
		} else {
			t.Logf("%s: IpcGet errors — bt=%v, peer=%v", label, btErr, pErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	dumpWgState(t, "TIMEOUT", bt, peerDev)
	t.Fatalf("%s: no handshake within %v", label, timeout)
}

// ── topology helper ──────────────────────────────────────────────────────────

// startBodyWgTopology creates a BodyTunnel and a peer WG device that complete
// a handshake. Returns both devices, the body pub-key hex, and the body tunnel.
//
// Both devices use explicit chosen ports so they know each other's endpoints
// from the moment they start, matching the pattern verified in miniwg test.
// Ports are randomized per-call to avoid conflicts when tests run sequentially.
func choosePorts() (peerPort, bodyPort int) {
	base := 52000 + rand.Intn(500)
	return base, base + 1
}

func startBodyWgTopology(t *testing.T) (
	bt *body.BodyTunnel,
	peerDev *device.Device,
	bodyPubHex string,
) {
	t.Helper()

	// Generate keypairs.
	peerKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)

	// Overlay addresses.
	bodyOverlayAddr := netip.MustParseAddr("fd01::2")
	peerOverlayAddr := netip.MustParseAddr("fd01::1")
	overlayPrefix := netip.MustParsePrefix("fd01::/64")
	bodyPort, peerPort := choosePorts()

	t.Logf("topology: peer=127.0.0.1:%d, body=127.0.0.1:%d", peerPort, bodyPort)

	// Step 1: Start peer WG device with explicit listen_port before Up.
	peerAllowed := "fd01::2/128"
	bodyEndpoint := fmt.Sprintf("127.0.0.1:%d", bodyPort)
	peerTun, _, err := netstack.CreateNetTUN(
		[]netip.Addr{peerOverlayAddr},
		nil,
		1280,
	)
	if err != nil {
		t.Fatalf("peer CreateNetTUN(%s): %v", peerOverlayAddr, err)
	}
	peerLogger := device.NewLogger(device.LogLevelError, "wg-peer: ")
	peerBind := conn.NewDefaultBind()
	peerDev = device.NewDevice(peerTun, peerBind, peerLogger)

	// Configure peer: private key, listen port, peer config.
	peerUapi := fmt.Sprintf(
		"private_key=%s\nlisten_port=%d\n",
		peerKeys.privHex, peerPort,
	)
	if err := peerDev.IpcSet(peerUapi); err != nil {
		t.Fatalf("peer IpcSet private_key+listen_port: %v", err)
	}
	peerPeerCfg := fmt.Sprintf(
		"public_key=%s\nendpoint=%s\nallowed_ip=%s\npersistent_keepalive_interval=1\n",
		bodyKeys.pubHex, bodyEndpoint, peerAllowed,
	)
	if err := peerDev.IpcSet(peerPeerCfg); err != nil {
		t.Fatalf("peer IpcSet peer config: %v", err)
	}
	if err := peerDev.Up(); err != nil {
		t.Fatalf("peer Up: %v", err)
	}
	t.Cleanup(func() { peerDev.Close() })

	// Step 2: Create and start BodyTunnel.
	btCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, peerKeys.pubHex),
		OverlayAddress: bodyOverlayAddr,
		OverlayPrefix:  overlayPrefix,
		CoreEndpoint:   fmt.Sprintf("127.0.0.1:%d", peerPort),
		ListenPort:     bodyPort,
	}
	bt = body.NewBodyTunnel(btCfg, nil)
	bodyCtx, bodyCancel := context.WithCancel(context.Background())
	t.Cleanup(bodyCancel)
	if err := bt.Start(bodyCtx); err != nil {
		t.Fatalf("bt.Start: %v", err)
	}

	// Step 3: Wait for handshake to complete.
	waitHandshakeTime(t, bt, peerDev, "topology", 10*time.Second)

	return bt, peerDev, bodyKeys.pubHex
}

// ── Tests ────────────────────────────────────────────────────────────────────

// TestLossObserver_RejectsNoHandshake verifies that StartLossObserver fails if
// the BodyTunnel has not completed a handshake yet.
func TestLossObserver_RejectsNoHandshake(t *testing.T) {
	bodyKeys := newTestKeypair(t)
	peerKeys := newTestKeypair(t)
	bodyOverlayAddr := netip.MustParseAddr("fd01::2")
	overlayPrefix := netip.MustParsePrefix("fd01::/64")

	btCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, peerKeys.pubHex),
		OverlayAddress: bodyOverlayAddr,
		OverlayPrefix:  overlayPrefix,
		CoreEndpoint:   "127.0.0.1:1",
		ListenPort:     0,
	}
	bt := body.NewBodyTunnel(btCfg, nil)
	bodyCtx, bodyCancel := context.WithCancel(context.Background())
	t.Cleanup(bodyCancel)
	if err := bt.Start(bodyCtx); err != nil {
		t.Fatalf("bt.Start: %v", err)
	}

	onLoss := make(chan body.PathLossEvent, 1)
	obsCtx, obsCancel := context.WithCancel(context.Background())
	t.Cleanup(func() { obsCancel() })

	obsHandle, obsErr := body.StartLossObserver(bt, obsCtx, body.LossObserverConfig{
		WGLivenessTimeout: 5 * time.Second,
		PollInterval:      1 * time.Second,
	}, onLoss, nil)
	if obsHandle != nil || obsErr == nil {
		t.Fatal("expected StartLossObserver to fail (no handshake), but it succeeded")
	}
	t.Logf("OK: StartLossObserver correctly rejected: %v", obsErr)
}

// TestLossObserver_CleanTeardown verifies that cancelling the observer context
// does NOT emit a loss event.
func TestLossObserver_CleanTeardown(t *testing.T) {
	bt, _, _ := startBodyWgTopology(t)

	onLoss := make(chan body.PathLossEvent, 1)
	obsCtx, obsCancel := context.WithCancel(context.Background())

	obsHandle, err := body.StartLossObserver(bt, obsCtx, body.LossObserverConfig{
		WGLivenessTimeout: 30 * time.Second,
		PollInterval:      500 * time.Millisecond,
	}, onLoss, nil)
	if err != nil {
		t.Fatalf("StartLossObserver: %v", err)
	}

	// Cancel the observer context — should exit without loss event.
	obsCancel()

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	select {
	case ev := <-onLoss:
		t.Fatalf("unexpected loss event on clean teardown: reason=%s",
			ev.Reason)
	case <-obsHandle.Done:
		t.Log("OK: observer terminated without emitting loss event")
	case <-timeout.C:
		t.Fatalf("observer did not terminate within %v", timeout)
	}
}

// TestLossObserver_DetectsTransportFailure kills the BodyTunnel's underlying
// transport and expects the observer to send a TransportFailure event.
//
// We simulate transport death via bt.CloseTransport() which closes the
// BodyTunnel's internal WG device without signaling clean lifecycle shutdown,
// making IpcGet fail. bt.Stop() must NOT cause TransportFailure — only
// CloseTransport() should.
func TestLossObserver_DetectsTransportFailure(t *testing.T) {
	bt, _, _ := startBodyWgTopology(t)

	onLoss := make(chan body.PathLossEvent, 1)
	obsCtx, obsCancel := context.WithCancel(context.Background())
	t.Cleanup(func() { obsCancel() })

	obsHandle, err := body.StartLossObserver(bt, obsCtx, body.LossObserverConfig{
		WGLivenessTimeout: 30 * time.Second, // long — won't fire
		PollInterval:      500 * time.Millisecond,
	}, onLoss, nil)
	if err != nil {
		t.Fatalf("StartLossObserver: %v", err)
	}
	if obsHandle == nil {
		t.Fatal("StartLossObserver returned nil handle")
	}

	// Kill the BodyTunnel's transport via CloseTransport (not Stop).
	bt.CloseTransport()

	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	select {
	case ev := <-onLoss:
		if ev.Reason != body.TransportFailure {
			t.Fatalf("expected TransportFailure, got %s", ev.Reason)
		}
		t.Logf("OK: detected TransportFailure")
	case <-timeout.C:
		t.Fatal("timed out waiting for TransportFailure event")
	}
}

// TestLossObserver_DetectsWGLivenessLost starts the observer, then kills the
// peer WG device so no more handshakes occur.  After the WGLivenessTimeout
// elapses, a WGLivenessLost event must arrive.
func TestLossObserver_DetectsWGLivenessLost(t *testing.T) {
	bt, peerDev, _ := startBodyWgTopology(t)

	onLoss := make(chan body.PathLossEvent, 1)
	obsCtx, obsCancel := context.WithCancel(context.Background())
	t.Cleanup(func() { obsCancel() })

	livenessTimeout := 5 * time.Second

	obsHandle, err := body.StartLossObserver(bt, obsCtx, body.LossObserverConfig{
		WGLivenessTimeout: livenessTimeout,
		PollInterval:      500 * time.Millisecond,
	}, onLoss, nil)
	if err != nil {
		t.Fatalf("StartLossObserver: %v", err)
	}
	if obsHandle == nil {
		t.Fatal("StartLossObserver returned nil handle")
	}

	// Kill the peer device so no further handshakes are possible.
	peerDev.Close()

	timeout := time.NewTimer(livenessTimeout + 5*time.Second)
	defer timeout.Stop()
	select {
	case ev := <-onLoss:
		if ev.Reason != body.WGLivenessLost {
			t.Fatalf("expected WGLivenessLost, got %s", ev.Reason)
		}
		t.Logf("OK: detected WGLivenessLost")
	case <-timeout.C:
		t.Fatal("timed out waiting for WGLivenessLost event")
	}
}