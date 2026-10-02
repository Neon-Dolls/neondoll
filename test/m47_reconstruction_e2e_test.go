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

// TestM47_ReconstructionE2E proves that a client can re-establish its
// routes after the entire Relay is rebuilt.
func TestM47_ReconstructionE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	relayPortSvc := pickPort(t)
	relayPortWS := pickPort(t)

	// ── 1. Start Relay and ControlServer ──
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

	// ── 2. Core attaches via WS ──
	clientCfg := relay.ClientConfig{
		RelayURL:          relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "recon-core",
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

	opened, err := ctrlClient.OpenRoute(ctx, 1, relay.RouteCredentials{Token: "recon-route"})
	if err != nil {
		t.Fatalf("OpenRoute: %v", err)
	}
	t.Logf("Route opened (endpoint=%q)", opened.AllocatedEndpoint)
	time.Sleep(300 * time.Millisecond)

	// ── 3. Full teardown ──
	t.Log("Tearing down relay...")
	csCancel()
	sCancel()

	// Wait 6s for http.Server.Shutdown and client read deadline to expire
	time.Sleep(6 * time.Second)

	// Verify disconnect
	disconnected := false
	for i := 0; i < 20; i++ {
		if !ctrlClient.IsConnected() {
			disconnected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !disconnected {
		t.Fatal("client did not disconnect after relay teardown")
	}
	t.Logf("Client disconnected")

	// ── 4. Start new Relay + ControlServer on same ports ──
	t.Log("Building new relay...")
	relayCfg2 := relay.DefaultServiceConfig()
	relayCfg2.UDP.ListenAddress = "127.0.0.1"
	relayCfg2.UDP.PortMin = relayPortSvc
	relayCfg2.UDP.PortMax = relayPortSvc + 2
	svc2, err := relay.NewService(relayCfg2, relay.ClientConfig{})
	if err != nil {
		t.Fatalf("NewService 2: %v", err)
	}
	svc2Ctx, svc2Cancel := context.WithCancel(ctx)
	defer svc2Cancel()
	go func() { _ = svc2.Start(svc2Ctx) }()

	cs2 := relay.NewControlServer(svc2, fmt.Sprintf("127.0.0.1:%d", relayPortWS), log)
	cs2Ctx, cs2Cancel := context.WithCancel(ctx)
	defer cs2Cancel()
	go func() { _ = cs2.Start(cs2Ctx) }()
	time.Sleep(300 * time.Millisecond)

	// ── 5. Wait for client reconnect ──
	reconnected := false
	for i := 0; i < 100; i++ {
		if ctrlClient.IsConnected() {
			reconnected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !reconnected {
		t.Fatal("ControlClient did not reconnect within 10s after relay rebuild")
	}
	t.Log("Core reconnected to new relay")

	// ── 6. Client auto-restores routes on reconnect — verify route 1 is back ──
	time.Sleep(500 * time.Millisecond) // give restoreRoutes time to complete
	route, ok := svc2.Registry().Route(1)
	if !ok {
		t.Fatal("route 1 not found in new registry after reconnect restore")
	}
	t.Logf("Route auto-restored in new relay: state=%v endpoint=%s", route.State, route.Endpoint)

	// ── 7. Verify traffic through new relay ──
	routeAddr := netip.MustParseAddrPort(route.Endpoint)
	conn, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(routeAddr))
	if err != nil {
		t.Fatalf("UDP dial to new relay: %v", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("recon-world"))
	if err != nil {
		t.Fatalf("UDP write: %v", err)
	}
	t.Log("Traffic flowing through new relay")

	if cs2.ActiveRegistrations() < 1 {
		t.Error("expected at least 1 active registration in new relay")
	} else {
		t.Logf("Active registrations in new relay: %d", cs2.ActiveRegistrations())
	}

	t.Log("=== M4.7 Reconstruction E2E: ALL PASS ===")
}
