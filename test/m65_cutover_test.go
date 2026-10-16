//go:build integration

// Core 4 / M6.5 — Controlled Path Migration: WG Cutover and Rollback.
//
// Integration tests confirming:
//   - Break-before-make cutover with real encrypted bidirectional traffic
//   - Handshake timeout triggers rollback to original path
//   - Stale generation tickets cannot disrupt the active path
//   - epoch.Active() consistency across all cutover outcomes
//
// These tests integrate Body path migration with real WireGuard devices,
// exercising the full break-before-make lifecycle end to end.

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Body"
	"github.com/Neon-Dolls/neondoll/Core/Relay"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
)

// trackingBind wraps a conn.Bind and records Close calls so tests can
// verify ownership transfer after migration.
type trackingBind struct {
	inner  conn.Bind
	mu     sync.Mutex
	closed bool
}

func (tb *trackingBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	tb.mu.Lock()
	tb.closed = false
	tb.mu.Unlock()
	return tb.inner.Open(port)
}
func (tb *trackingBind) Close() error {
	tb.mu.Lock()
	tb.closed = true
	tb.mu.Unlock()
	return tb.inner.Close()
}
func (tb *trackingBind) SetMark(mark uint32) error { return tb.inner.SetMark(mark) }
func (tb *trackingBind) BatchSize() int            { return tb.inner.BatchSize() }
func (tb *trackingBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	return tb.inner.Send(bufs, ep)
}
func (tb *trackingBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	return tb.inner.ParseEndpoint(s)
}
func (tb *trackingBind) String() string { return "trackingBind" }

// ── Test candidates ──────────────────────────────────────────────────────

// cutoverCandidate is a simple test candidate that returns a fixed endpoint.
type cutoverCandidate struct {
	endpoint string    // core endpoint for the new path
	bind     conn.Bind // optional transport bind (e.g. WSS) to attach
	fail     bool      // if set, Validate returns an error
}

func (c *cutoverCandidate) Validate(ctx context.Context) (*body.MigrationTarget, error) {
	if c.fail {
		return nil, errors.New("candidate validation failed")
	}
	target := &body.MigrationTarget{CoreEndpoint: c.endpoint}
	if c.bind != nil {
		// Preserve transport-specific bind semantics rather than forcing
		// every candidate onto generic UDP.
		target.Bind = c.bind
	}
	return target, nil
}

func (c *cutoverCandidate) Stop() {}

// ── Tests ────────────────────────────────────────────────────────────────

// TestCutoverPath_Success verifies a complete break-before-make cutover:
//  1. old tunnel carries encrypted traffic
//  2. cutover breaks the old path and establishes the new one
//  3. new tunnel also carries encrypted traffic
//  4. epoch.Active() reflects the new tunnel
//  5. old tunnel is fully stopped
func TestCutoverPath_Success(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	// Generate keypairs
	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	// Create core WG device with netstack
	coreTun, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	defer coreTun.Close()
	coreEndpoint := fmt.Sprintf("127.0.0.1:%d", corePort)

	bodyOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:2")
	coreOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:1")

	// Create the old (pre-cutover) body tunnel
	oldCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   coreEndpoint,
		ListenPort:     0,
		MTU:            device.DefaultMTU,
	}
	oldTunnel := body.NewBodyTunnel(oldCfg, log)
	if err := oldTunnel.Start(ctx); err != nil {
		t.Fatalf("old tunnel Start: %v", err)
	}
	t.Cleanup(func() {
		if oldTunnel.Started() {
			oldTunnel.Stop()
		}
	})

	// Wait for handshake on both sides: core must register the body's
	// handshake before encrypted traffic can flow peer-to-peer.
	waitHandshake(t, coreTun, "core device", 15*time.Second)
	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := oldTunnel.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("old tunnel handshake: %v", err)
	}
	t.Log("old tunnel handshake complete")

	// Prove bidirectional encrypted traffic before cutover
	proveBidirectionalTraffic(t, oldTunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "pre-cutover")
	t.Log("pre-cutover traffic OK")

	// Create epoch and reserve migration gen
	epoch := body.NewRecoveryEpoch(oldTunnel)
	gen := epoch.NextGenForTunnel(oldTunnel)

	// Candidate pointing to the same core endpoint
	candidate := &cutoverCandidate{endpoint: coreEndpoint}

	// ── CUTOVER ──────────────────────────────────────────────────────
	result := body.CutoverPath(ctx, oldTunnel, epoch, candidate, gen,
		body.MigrationConfig{ProbeTimeout: 10 * time.Second}, log)

	if result.Err != nil {
		t.Fatalf("CutoverPath returned error: %v", result.Err)
	}
	if result.Tunnel == nil {
		t.Fatal("CutoverPath returned nil tunnel on success")
	}
	t.Cleanup(func() {
		if result.Tunnel.Started() {
			result.Tunnel.Stop()
		}
	})

	// Verify epoch consistency
	if epoch.Active() != result.Tunnel {
		t.Error("epoch.Active() does not match cutover result tunnel")
	}

	// Verify old tunnel is fully stopped
	if oldTunnel.Started() {
		t.Error("old tunnel still reports started after cutover")
	}

	// Prove bidirectional encrypted traffic on the new tunnel
	proveBidirectionalTraffic(t, result.Tunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "post-cutover")
	t.Log("post-cutover traffic OK")

	t.Log("TestCutoverPath_Success: PASS")
}

// TestCutoverPath_RollbackOnHandshakeTimeout verifies that when the new
// tunnel cannot complete its WireGuard handshake, cutover rolls back to the
// original endpoint path. Traffic flows through the rollback tunnel, and
// epoch.Active() reflects the rollback tunnel.
func TestCutoverPath_RollbackOnHandshakeTimeout(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	coreTun, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	defer coreTun.Close()

	coreEndpoint := fmt.Sprintf("127.0.0.1:%d", corePort)

	bodyOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:2")
	coreOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:1")

	oldCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   coreEndpoint,
		ListenPort:     0,
		MTU:            device.DefaultMTU,
	}
	oldTunnel := body.NewBodyTunnel(oldCfg, log)
	if err := oldTunnel.Start(ctx); err != nil {
		t.Fatalf("old tunnel Start: %v", err)
	}
	t.Cleanup(func() {
		if oldTunnel.Started() {
			oldTunnel.Stop()
		}
	})

	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := oldTunnel.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("old tunnel handshake: %v", err)
	}
	t.Log("old tunnel handshake complete")

	// Prove bidirectional traffic before cutover
	proveBidirectionalTraffic(t, oldTunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "pre-rollback")
	t.Log("pre-rollback traffic OK")

	// Create epoch, get gen, then create a candidate pointing to a dead endpoint
	epoch := body.NewRecoveryEpoch(oldTunnel)
	gen := epoch.NextGenForTunnel(oldTunnel)

	// No one listening on port 1 — handshake will timeout via the core
	// (the core tries to handshake back but gets connection refused).
	candidate := &cutoverCandidate{endpoint: "127.0.0.1:1"}

	// ── CUTOVER (expecting rollback) ────────────────────────────────
	result := body.CutoverPath(ctx, oldTunnel, epoch, candidate, gen,
		body.MigrationConfig{ProbeTimeout: 5 * time.Second}, log)

	// Should return error (cutover failed) but with a rollback tunnel
	if result.Err == nil {
		t.Fatal("CutoverPath should have returned an error on rollback")
	}
	if result.Tunnel == nil {
		t.Fatal("CutoverPath should have returned a rollback tunnel")
	}
	t.Cleanup(func() {
		if result.Tunnel.Started() {
			result.Tunnel.Stop()
		}
	})

	// Verify epoch consistency — active tunnel must be the rollback
	if epoch.Active() != result.Tunnel {
		t.Error("epoch.Active() does not match rollback tunnel")
	}

	// Verify old tunnel is stopped
	if oldTunnel.Started() {
		t.Error("old tunnel still reports started after rollback")
	}

	// Prove bidirectional encrypted traffic on the rollback tunnel
	proveBidirectionalTraffic(t, result.Tunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "post-rollback")
	t.Log("post-rollback traffic OK — recovery path works")

	t.Log("TestCutoverPath_RollbackOnHandshakeTimeout: PASS")
}

// TestCutoverPath_StaleGeneration verifies that when the generation ticket is
// already stale (another migration advanced the epoch gen), cutover rejects
// the ticket without touching the current active tunnel.
func TestCutoverPath_StaleGeneration(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	coreTun, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	defer coreTun.Close()

	coreEndpoint := fmt.Sprintf("127.0.0.1:%d", corePort)

	bodyOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:2")
	coreOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:1")

	oldCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   coreEndpoint,
		ListenPort:     0,
		MTU:            device.DefaultMTU,
	}
	oldTunnel := body.NewBodyTunnel(oldCfg, log)
	if err := oldTunnel.Start(ctx); err != nil {
		t.Fatalf("old tunnel Start: %v", err)
	}
	t.Cleanup(func() {
		if oldTunnel.Started() {
			oldTunnel.Stop()
		}
	})

	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := oldTunnel.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("old tunnel handshake: %v", err)
	}
	t.Log("old tunnel handshake complete")

	// Prove bidirectional traffic BEFORE stale gen attempt
	proveBidirectionalTraffic(t, oldTunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "before-stale")
	t.Log("pre-stale traffic OK")

	// Create epoch, get gen, then advance the gen so our ticket is stale
	epoch := body.NewRecoveryEpoch(oldTunnel)
	gen := epoch.NextGenForTunnel(oldTunnel) // gen = current
	_ = epoch.NextGen()                      // advance — now gen is stale

	candidate := &cutoverCandidate{endpoint: coreEndpoint}

	// ── CUTOVER with stale generation ───────────────────────────────
	result := body.CutoverPath(ctx, oldTunnel, epoch, candidate, gen,
		body.MigrationConfig{ProbeTimeout: 2 * time.Second}, log)

	// Must return ErrMigrationSuperseded
	if !errors.Is(result.Err, body.ErrMigrationSuperseded) {
		t.Fatalf("expected ErrMigrationSuperseded, got %v", result.Err)
	}
	if result.Tunnel != nil {
		t.Fatal("CutoverPath should not return a tunnel on stale gen")
	}

	// The old tunnel must NOT have been touched
	if !oldTunnel.Started() {
		t.Error("old tunnel was stopped despite stale generation — disruption!")
	}
	if epoch.Active() != oldTunnel {
		t.Error("epoch.Active() changed despite stale generation")
	}

	// Prove bidirectional traffic still works on the untouched old tunnel
	proveBidirectionalTraffic(t, oldTunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "after-stale")
	t.Log("post-stale traffic OK — old tunnel unaffected")

	t.Log("TestCutoverPath_StaleGeneration: PASS")
}

// TestCutoverPath_ToRelayUDP migrates a working direct path to a second core
// endpoint reached over plain UDP. It represents a relay-udp-style alternative
// path and proves the cutover swaps the endpoint while carrying real encrypted
// traffic on the migrated tunnel.
func TestCutoverPath_ToRelayUDP(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	coreTunA, coreNetA, corePortA := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	defer coreTunA.Close()
	coreTunB, coreNetB, corePortB := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	defer coreTunB.Close()

	coreEndpointA := fmt.Sprintf("127.0.0.1:%d", corePortA)
	coreEndpointB := fmt.Sprintf("127.0.0.1:%d", corePortB)

	bodyOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:2")
	coreOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:1")

	oldCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   coreEndpointA,
		ListenPort:     0,
		MTU:            device.DefaultMTU,
	}
	oldTunnel := body.NewBodyTunnel(oldCfg, log)
	if err := oldTunnel.Start(ctx); err != nil {
		t.Fatalf("old tunnel Start: %v", err)
	}
	t.Cleanup(func() {
		if oldTunnel.Started() {
			oldTunnel.Stop()
		}
	})

	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := oldTunnel.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("old tunnel handshake: %v", err)
	}
	t.Log("old tunnel (direct) handshake complete")

	// Bidirectional traffic BEFORE migration
	proveBidirectionalTraffic(t, oldTunnel.Netstack(), coreNetA,
		bodyOverlay, coreOverlay, "before-migration")
	t.Log("pre-migration traffic OK on direct path")

	epoch := body.NewRecoveryEpoch(oldTunnel)
	gen := epoch.NextGenForTunnel(oldTunnel)

	// Candidate: relay-udp-style alternative endpoint (second core device)
	candidate := &cutoverCandidate{endpoint: coreEndpointB}

	// ── CUTOVER ─────────────────────────────────────────────────────
	result := body.CutoverPath(ctx, oldTunnel, epoch, candidate, gen,
		body.MigrationConfig{ProbeTimeout: 5 * time.Second}, log)
	if result.Err != nil {
		t.Fatalf("CutoverPath: %v", result.Err)
	}
	if result.Tunnel == nil {
		t.Fatal("CutoverPath returned nil tunnel")
	}
	t.Cleanup(func() {
		if result.Tunnel.Started() {
			result.Tunnel.Stop()
		}
	})

	if epoch.Active() != result.Tunnel {
		t.Error("epoch.Active() does not match migrated tunnel")
	}
	if oldTunnel.Started() {
		t.Error("old tunnel still reports started after cutover")
	}

	// Bidirectional traffic AFTER migration, to the new endpoint
	proveBidirectionalTraffic(t, result.Tunnel.Netstack(), coreNetB,
		bodyOverlay, coreOverlay, "post-migration")
	t.Log("post-migration traffic OK on relay-udp path")

	t.Log("TestCutoverPath_ToRelayUDP: PASS")
}

// TestCutoverPath_ToWSS migrates a working direct path to a WSS path through a
// real relay. It proves CutoverPath preserves transport-specific Bind
// semantics — the migrated tunnel attaches the candidate's WSS bind instead of
// falling back to generic UDP — and carries real encrypted traffic over WSS.
func TestCutoverPath_ToWSS(t *testing.T) {
	ctx := context.Background()
	log := testLogger(t)

	bodyKeys := newTestKeypair(t)
	coreKeys := newTestKeypair(t)

	coreTunA, coreNetA, corePortA := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	defer coreTunA.Close()

	coreEndpointA := fmt.Sprintf("127.0.0.1:%d", corePortA)

	bodyOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:2")
	coreOverlay := netip.MustParseAddr("fd00:0:0:0:0:0:0:1")

	oldCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   coreEndpointA,
		ListenPort:     0,
		MTU:            device.DefaultMTU,
	}
	oldTunnel := body.NewBodyTunnel(oldCfg, log)
	if err := oldTunnel.Start(ctx); err != nil {
		t.Fatalf("old tunnel Start: %v", err)
	}
	t.Cleanup(func() {
		if oldTunnel.Started() {
			oldTunnel.Stop()
		}
	})

	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := oldTunnel.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("old tunnel handshake: %v", err)
	}
	t.Log("old tunnel (direct) handshake complete")

	// Bidirectional traffic BEFORE migration
	proveBidirectionalTraffic(t, oldTunnel.Netstack(), coreNetA,
		bodyOverlay, coreOverlay, "before-migration")
	t.Log("pre-migration traffic OK on direct path")

	// ── Relay + WSS infrastructure (mirrors m62 recovery topology) ──
	relayAddr := pickFreeTCPAddrPort(t)
	const token = "m65-wss-token"
	const routeID = relay.RouteID(65)
	const credential = "m65-wss-cred"

	svcCfg := relay.DefaultServiceConfig()
	hash := sha256.Sum256([]byte(token))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svcCfg.KeepaliveInterval = 2 * time.Second
	svcCfg.RouteTimeout = 180 * time.Second
	svcCfg.RegistrationTimeout = 180 * time.Second

	svc, err := relay.NewService(svcCfg, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("relay.NewService: %v", err)
	}
	svcCtx, svcCancel := context.WithCancel(ctx)
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
	waitClientConnected(t, client, "wss-cutover-client")

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
	if _, _, trErr := tr.Open(0); trErr != nil {
		t.Fatalf("tr.Open: %v", trErr)
	}
	t.Cleanup(func() { _ = tr.Close() })

	endpoint := "relay:1"
	coreDevB, coreNetB := startWgDevice(t, tr, coreKeys, coreOverlay, bodyKeys.pubHex, endpoint, "fd00::2/128")

	epoch := body.NewRecoveryEpoch(oldTunnel)
	gen := epoch.NextGenForTunnel(oldTunnel)

	// Candidate: WSS bind targeted at the relay route
	candidate := &cutoverCandidate{
		endpoint: endpoint,
		bind:     body.NewWSSBind(relayAddr, routeID, credential),
	}

	// ── CUTOVER ─────────────────────────────────────────────────────
	result := body.CutoverPath(ctx, oldTunnel, epoch, candidate, gen,
		body.MigrationConfig{ProbeTimeout: 10 * time.Second}, log)
	if result.Err != nil {
		t.Fatalf("CutoverPath: %v", result.Err)
	}
	if result.Tunnel == nil {
		t.Fatal("CutoverPath returned nil tunnel")
	}
	t.Cleanup(func() {
		if result.Tunnel.Started() {
			result.Tunnel.Stop()
		}
	})

	if epoch.Active() != result.Tunnel {
		t.Error("epoch.Active() does not match migrated WSS tunnel")
	}
	if oldTunnel.Started() {
		t.Error("old tunnel still reports started after cutover")
	}

	// The migrated tunnel must be carrying real encrypted traffic over WSS
	waitHandshake(t, coreDevB, "Core device B (WSS, migrated)", 15*time.Second)
	proveBidirectionalTraffic(t, result.Tunnel.Netstack(), coreNetB,
		bodyOverlay, coreOverlay, "post-migration-wss")
	t.Log("post-migration traffic OK on WSS path")

	t.Log("TestCutoverPath_ToWSS: PASS")
}

// TestMigratePath_CutoverPath exercises the full MigratePath → CutoverPath
// flow, where MigratePath obtains the generation ticket and CutoverPath
// performs the actual break-before-make cutover with that ticket.
func TestMigratePath_CutoverPath(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	log := testLogger(t)

	// ── Setup: direct UDP core + body tunnel ────────────────────────
	coreKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)

	coreTun, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	t.Cleanup(func() { coreTun.Close() })
	coreEndpoint := fmt.Sprintf("127.0.0.1:%d", corePort)

	coreOverlay := netip.MustParseAddr("fd00::1")
	bodyOverlay := netip.MustParseAddr("fd00::2")

	oldCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   coreEndpoint,
		ListenPort:     0,
		MTU:            device.DefaultMTU,
	}
	oldTunnel := body.NewBodyTunnel(oldCfg, log)
	if err := oldTunnel.Start(ctx); err != nil {
		t.Fatalf("old tunnel Start: %v", err)
	}
	t.Cleanup(func() {
		if oldTunnel.Started() {
			oldTunnel.Stop()
		}
	})

	waitHandshake(t, coreTun, "core device", 15*time.Second)
	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := oldTunnel.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("old tunnel handshake: %v", err)
	}
	t.Log("old tunnel handshake complete")

	proveBidirectionalTraffic(t, oldTunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "pre-cutover")
	t.Log("pre-cutover traffic OK")

	// ── MigratePath + CutoverPath ──────────────────────────────────
	epoch := body.NewRecoveryEpoch(oldTunnel)
	candidate := &cutoverCandidate{endpoint: coreEndpoint}

	gen, err := body.MigratePath(ctx, epoch, candidate, body.MigrationConfig{ProbeTimeout: 5 * time.Second}, log)
	if err != nil {
		t.Fatalf("MigratePath: %v", err)
	}
	t.Logf("MigratePath reserved gen=%d", gen)

	result := body.CutoverPath(ctx, oldTunnel, epoch, candidate, gen,
		body.MigrationConfig{ProbeTimeout: 5 * time.Second}, log)
	if result.Err != nil {
		t.Fatalf("CutoverPath: %v", result.Err)
	}
	if result.Tunnel == nil {
		t.Fatal("CutoverPath returned nil tunnel")
	}
	t.Cleanup(func() {
		if result.Tunnel.Started() {
			result.Tunnel.Stop()
		}
	})

	if epoch.Active() != result.Tunnel {
		t.Error("epoch.Active() does not match migrated tunnel")
	}
	if oldTunnel.Started() {
		t.Error("old tunnel still reports started after cutover")
	}

	waitHandshake(t, coreTun, "Core device (post-cutover)", 15*time.Second)
	proveBidirectionalTraffic(t, result.Tunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "post-cutover")
	t.Log("post-cutover traffic OK on migrated tunnel")

	t.Log("TestMigratePath_CutoverPath: PASS")
}

// TestCutoverPath_BindNotClosedAfterTransfer verifies that after a successful
// cutover, candidate.Stop() does not close the Bind now owned by the new tunnel.
func TestCutoverPath_BindNotClosedAfterTransfer(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	log := testLogger(t)

	// ── Setup: direct UDP core + body tunnel ────────────────────────
	coreKeys := newTestKeypair(t)
	bodyKeys := newTestKeypair(t)

	coreTun, coreNet, corePort := startCoreUDPDeviceWithNetstack(t, coreKeys, bodyKeys.pubHex)
	t.Cleanup(func() { coreTun.Close() })
	coreEndpoint := fmt.Sprintf("127.0.0.1:%d", corePort)

	coreOverlay := netip.MustParseAddr("fd00::1")
	bodyOverlay := netip.MustParseAddr("fd00::2")

	oldCfg := body.BodyTunnelConfig{
		PrivateKey:     hexToKey(t, bodyKeys.privHex),
		CorePublicKey:  hexToKey(t, coreKeys.pubHex),
		OverlayAddress: bodyOverlay,
		OverlayPrefix:  netip.MustParsePrefix("fd00::/64"),
		CoreEndpoint:   coreEndpoint,
		ListenPort:     0,
		MTU:            device.DefaultMTU,
	}
	oldTunnel := body.NewBodyTunnel(oldCfg, log)
	if err := oldTunnel.Start(ctx); err != nil {
		t.Fatalf("old tunnel Start: %v", err)
	}
	t.Cleanup(func() {
		if oldTunnel.Started() {
			oldTunnel.Stop()
		}
	})

	waitHandshake(t, coreTun, "core device", 15*time.Second)
	hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer hsCancel()
	if err := oldTunnel.WaitHandshake(hsCtx, 15*time.Second); err != nil {
		t.Fatalf("old tunnel handshake: %v", err)
	}
	t.Log("old tunnel handshake complete")

	proveBidirectionalTraffic(t, oldTunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "pre-cutover")
	t.Log("pre-cutover traffic OK")

	// ── Candidate with a tracking bind ──────────────────────────────
	innerBind := conn.NewDefaultBind()
	tb := &trackingBind{inner: innerBind}
	candidate := &cutoverCandidate{
		endpoint: coreEndpoint,
		bind:     tb,
	}

	epoch := body.NewRecoveryEpoch(oldTunnel)
	gen := epoch.NextGenForTunnel(oldTunnel)

	result := body.CutoverPath(ctx, oldTunnel, epoch, candidate, gen,
		body.MigrationConfig{ProbeTimeout: 5 * time.Second}, log)
	if result.Err != nil {
		t.Fatalf("CutoverPath: %v", result.Err)
	}
	if result.Tunnel == nil {
		t.Fatal("CutoverPath returned nil tunnel")
	}
	t.Cleanup(func() {
		if result.Tunnel.Started() {
			result.Tunnel.Stop()
		}
	})

	// Verify the bind was NOT closed — candidate.Stop() must not close
	// a bind now owned by the new tunnel.
	tb.mu.Lock()
	closed := tb.closed
	tb.mu.Unlock()
	if closed {
		t.Error("bind was closed after transfer — candidate.Stop() must not close owned bind")
	}

	// Traffic still works
	waitHandshake(t, coreTun, "Core device (post-cutover)", 15*time.Second)
	proveBidirectionalTraffic(t, result.Tunnel.Netstack(), coreNet,
		bodyOverlay, coreOverlay, "post-cutover")
	t.Log("post-cutover traffic OK on migrated tunnel")

	t.Log("TestCutoverPath_BindNotClosedAfterTransfer: PASS")
}
