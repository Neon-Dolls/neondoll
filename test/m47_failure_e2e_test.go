//go:build e2e

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	relay "github.com/Neon-Dolls/neondoll/Core/Relay"
)

// TestM47_FailureE2E covers client recovery from a WSS interruption.
//
// Coverage:
//   - WSS interruption: cancel the ControlServer + Service, client detects disconnection
//   - Client reconnects when a new server starts on the same port
//   - Closed UDP route: open a route, close it, verify route gone from registry
//   - OpenRoute with empty token is rejected (M4.6 boundary)
func TestM47_FailureE2E(t *testing.T) {
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
	clientCfg := relay.ClientConfig{
		RelayURL:          relayURL,
		HandshakeTimeout:  10 * time.Second,
		RegistrationToken: "fail-tkn",
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

	// Open a route
	opened, err := ctrlClient.OpenRoute(ctx, 1, relay.RouteCredentials{Token: "fail-route"})
	if err != nil {
		t.Fatalf("OpenRoute: %v", err)
	}
	t.Logf("Route opened (endpoint=%q)", opened.AllocatedEndpoint)

	// ── Failure 1: WSS interruption — cancel both contexts ──
	t.Log("Interrupting WS connection (cancel ControlServer + Service)...")
	csCancel()
	sCancel()

	// Wait 6s: covers the 5s http.Server.Shutdown timeout and gives the
	// client's 2s ReadTimeout time to expire and flag the connection dead.
	time.Sleep(6 * time.Second)

	// Verify the client has disconnected
	disconnected := false
	for i := 0; i < 20; i++ {
		if !ctrlClient.IsConnected() {
			disconnected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !disconnected {
		t.Fatal("client did not disconnect after WSS interruption")
	}
	t.Logf("Disconnect detected")

	// ── Start new Service + ControlServer on same WS port ──
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

	// Wait for reconnect
	reconnected := false
	for i := 0; i < 100; i++ {
		if ctrlClient.IsConnected() {
			reconnected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !reconnected {
		t.Fatal("client did not reconnect after WS interrupt + server restart")
	}

	metrics := ctrlClient.Metrics()
	t.Logf("Failure 1 (WSS interrupt): %d reconnects — PASS", metrics.Reconnects)

	// Open a new route after reconnect
	opened2, err := ctrlClient.OpenRoute(ctx, 2, relay.RouteCredentials{Token: "fail-route-2"})
	if err != nil {
		t.Fatalf("OpenRoute after reconnect: %v", err)
	}
	t.Logf("Opened new route after reconnect (endpoint=%q)", opened2.AllocatedEndpoint)

	// ── Failure 2: closed UDP route — close and verify gone ──
	if err := ctrlClient.CloseRoute(ctx, 2); err != nil {
		t.Fatalf("CloseRoute 2: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, ok := svc2.Registry().Route(2); ok {
		t.Error("route 2 should be removed from registry after CloseRoute")
	}
	t.Log("Failure 2 (closed UDP route): route removed from registry — PASS")

	// ── Failure 3: OpenRoute with empty token ──
	_, emptyErr := ctrlClient.OpenRoute(ctx, 3, relay.RouteCredentials{Token: ""})
	if emptyErr == nil {
		t.Error("OpenRoute with empty token should fail")
	} else {
		t.Logf("Failure 3 (empty token rejected): %v — PASS", emptyErr)
	}

	t.Log("=== M4.7 Failure E2E: ALL PASS ===")
}
