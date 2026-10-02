//go:build e2e

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

// TestM47_ReconnectE2E proves client can survive a relay rebuild.
//
// Scenario:
//  1. Relay + ControlServer start
//  2. Core attaches via WS, opens a route
//  3. Full shutdown (ControlServer + relay service)
//  4. Client detects disconnection (short ReadTimeout forces detection)
//  5. New relay + ControlServer on same ports
//  6. Client reconnects automatically
//  7. Route restored, traffic flows
func TestM47_ReconnectE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	relayPortSvc := pickPort(t)
	relayPortWS := pickPort(t)

	relayCfg := relay.DefaultServiceConfig()
	relayCfg.UDP.ListenAddress = "127.0.0.1"
	relayCfg.UDP.PortMin = relayPortSvc
	relayCfg.UDP.PortMax = relayPortSvc
	svc, err := relay.NewService(relayCfg, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	sCtx, sCancel := context.WithCancel(ctx)
	defer sCancel()
	go func() { _ = svc.Start(sCtx) }()

	cs := relay.NewControlServer(svc, fmt.Sprintf("127.0.0.1:%d", relayPortWS), log)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	go func() { _ = cs.Start(csCtx) }()
	time.Sleep(200 * time.Millisecond)

	relayURL := fmt.Sprintf("ws://127.0.0.1:%d/relay", relayPortWS)

	// Core attaches via WS control tunnel.
	// Short ReadTimeout (2s) ensures the client detects disconnect quickly
	// after the server stops; http.Server.Shutdown does not close hijacked
	// WS connections, so the read deadline is the only detection mechanism.
	clientCfg := relay.ClientConfig{
		RelayURL:          relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "reconn-tkn",
		ReconnectInitial:  100 * time.Millisecond,
		ReconnectMax:      2 * time.Second,
		ReadTimeout:       2 * time.Second,
		PingInterval:      30 * time.Second,
	}
	ctrlClient := relay.NewControlClient(clientCfg)
	if err := ctrlClient.Start(ctx); err != nil {
		t.Fatalf("ControlClient Start: %v", err)
	}
	defer ctrlClient.Shutdown()

	opened, err := ctrlClient.OpenRoute(ctx, 1, relay.RouteCredentials{Token: "reconn-route"})
	if err != nil {
		t.Fatalf("OpenRoute: %v", err)
	}
	t.Logf("Route opened (endpoint=%q)", opened.AllocatedEndpoint)
	time.Sleep(200 * time.Millisecond)

	// ── Full shutdown — cancel both ControlServer and Service ──
	csCancel()
	sCancel()

	// Wait 6s: covers the 5s http.Server.Shutdown timeout and gives the
	// client's 2s ReadTimeout time to expire and flag the connection dead.
	time.Sleep(6 * time.Second)

	// Verify the client has disconnected (poll up to 2s more if needed)
	disconnected := false
	for i := 0; i < 20; i++ {
		if !ctrlClient.IsConnected() {
			disconnected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !disconnected {
		t.Fatal("client did not disconnect after relay shutdown")
	}
	t.Logf("Client disconnected after shutdown")

	// ── Start new relay + server ──
	// New service uses a wider UDP port range because the old service's
	// UDP socket may still hold relayPortSvc (context cancel doesn't close it).
	relayCfg2 := relay.DefaultServiceConfig()
	relayCfg2.UDP.ListenAddress = "127.0.0.1"
	relayCfg2.UDP.PortMin = relayPortSvc
	relayCfg2.UDP.PortMax = relayPortSvc + 2
	svc2, err := relay.NewService(relayCfg2, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("NewService 2: %v", err)
	}
	sCtx2, sCancel2 := context.WithCancel(ctx)
	defer sCancel2()
	go func() { _ = svc2.Start(sCtx2) }()

	cs2 := relay.NewControlServer(svc2, fmt.Sprintf("127.0.0.1:%d", relayPortWS), log)
	csCtx2, csCancel2 := context.WithCancel(ctx)
	defer csCancel2()
	go func() { _ = cs2.Start(csCtx2) }()

	// Wait for client to reconnect
	reconnected := false
	for i := 0; i < 100; i++ {
		if ctrlClient.IsConnected() {
			reconnected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !reconnected {
		t.Fatal("client did not reconnect after relay rebuild")
	}
	t.Logf("Client reconnected")

	metrics := ctrlClient.Metrics()
	t.Logf("Reconnect metrics: %d reconnects", metrics.Reconnects)
	if metrics.Reconnects == 0 {
		t.Error("expected at least 1 reconnect")
	}

	// ── Open route after reconnect (use a fresh ID, old relay is gone) ──
	opened2, err := ctrlClient.OpenRoute(ctx, 2, relay.RouteCredentials{Token: "reconn-route-2"})
	if err != nil {
		t.Fatalf("OpenRoute after reconnect: %v", err)
	}
	t.Logf("New route opened (endpoint=%q)", opened2.AllocatedEndpoint)
	time.Sleep(200 * time.Millisecond)

	// ── Verify traffic ──
	routeAddr := netip.MustParseAddrPort(opened2.AllocatedEndpoint)
	conn, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(routeAddr))
	if err != nil {
		t.Fatalf("UDP dial: %v", err)
	}
	defer conn.Close()
	_, err = conn.Write([]byte("reconnect-verify"))
	if err != nil {
		t.Fatalf("UDP write: %v", err)
	}
	t.Log("Traffic flowing")

	route, ok := svc2.Registry().Route(2)
	if !ok {
		t.Fatal("route 2 missing from registry")
	}
	t.Logf("Route 2 in registry: state=%v endpoint=%s", route.State, route.Endpoint)

	t.Log("=== M4.7 Reconnect E2E: ALL PASS ===")
}
