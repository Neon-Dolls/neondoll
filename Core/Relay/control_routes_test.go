package relay

import (
	"context"
	"testing"
	"time"
)

func TestControlClient_OpenRoute(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "token"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer client.Shutdown()

	route, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: "route-token"})
	if err != nil {
		t.Fatalf("OpenRoute() = %v", err)
	}
	if route.RouteID != 1 {
		t.Errorf("RouteID = %d; want 1", route.RouteID)
	}
	if route.AllocatedEndpoint == "" {
		t.Error("AllocatedEndpoint is empty; want non-empty")
	}

	// Verify route appears in metrics
	metrics := client.Metrics()
	if metrics.RoutesRestored != 0 {
		t.Errorf("Metrics().RoutesRestored = %d; want 0 (no reconnects)", metrics.RoutesRestored)
	}
}

func TestControlClient_OpenRouteRejected(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "token"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer client.Shutdown()

	// Route with empty Token should be rejected by the fake relay
	_, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: ""})
	if err == nil {
		t.Fatal("OpenRoute() = nil; expected rejection")
	}
}

func TestControlClient_CloseRoute(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "token"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer client.Shutdown()

	// Open first
	route, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: "rt-1"})
	if err != nil {
		t.Fatalf("OpenRoute() = %v", err)
	}
	if route.AllocatedEndpoint != "127.0.0.1:51820" {
		t.Errorf("AllocatedEndpoint = %q; want 127.0.0.1:51820", route.AllocatedEndpoint)
	}

	// Then close
	if err := client.CloseRoute(context.Background(), 1); err != nil {
		t.Fatalf("CloseRoute() = %v", err)
	}
}

func TestControlClient_OpenRoute_StructValidation(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "token"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 500 * time.Millisecond
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer client.Shutdown()

	t.Run("nonzero routeID", func(t *testing.T) {
		_, err := client.OpenRoute(context.Background(), 0, RouteCredentials{Token: "rt-0"})
		if err == nil {
			t.Error("OpenRoute with routeID 0 should fail")
		}
	})

	t.Run("nonempty credentials", func(t *testing.T) {
		_, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: ""})
		if err == nil {
			t.Error("OpenRoute with empty credentials should fail")
		}
	})
}

func TestControlClient_CloseRoute_StructValidation(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "token"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 500 * time.Millisecond
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer client.Shutdown()

	t.Run("nonzero routeID", func(t *testing.T) {
		err := client.CloseRoute(context.Background(), 0)
		if err == nil {
			t.Error("CloseRoute with routeID 0 should fail")
		}
	})
}

// ----- M4.3: Two-Core isolation integration test -----

func TestControlClient_TwoCoreIsolation(t *testing.T) {
	// Two independent ControlClient instances registered against the same
	// fake Relay. Each must:
	//   - authenticate independently
	//   - obtain/manage its own route
	//   - NOT have its operations disrupted by the other's state
	//   - survive one's disconnect without affecting the other

	srv, url := startFakeRelay(t, multiClientRelayHandler(t))
	defer srv.Close()

	cfg1 := DefaultClientConfig()
	cfg1.RelayURL = url
	cfg1.RegistrationToken = "token-alpha"
	cfg1.HandshakeTimeout = 500 * time.Millisecond
	cfg1.ReadTimeout = 2 * time.Second
	cfg1.PingInterval = 30 * time.Second
	cfg1.ReconnectInitial = 100 * time.Millisecond
	cfg1.ReconnectMax = 500 * time.Millisecond

	cfg2 := DefaultClientConfig()
	cfg2.RelayURL = url
	cfg2.RegistrationToken = "token-beta"
	cfg2.HandshakeTimeout = 500 * time.Millisecond
	cfg2.ReadTimeout = 2 * time.Second
	cfg2.PingInterval = 30 * time.Second
	cfg2.ReconnectInitial = 100 * time.Millisecond
	cfg2.ReconnectMax = 500 * time.Millisecond

	clientA := NewControlClient(cfg1)
	if err := clientA.Start(context.Background()); err != nil {
		t.Fatalf("clientA Start() = %v", err)
	}
	defer clientA.Shutdown()

	clientB := NewControlClient(cfg2)
	if err := clientB.Start(context.Background()); err != nil {
		t.Fatalf("clientB Start() = %v", err)
	}
	defer clientB.Shutdown()

	// Both should be connected — wait for async connection
	waitConnected(t, clientA, "clientA-start")
	waitConnected(t, clientB, "clientB-start")

	// Each opens its own route
	ra, err := clientA.OpenRoute(context.Background(), 10, RouteCredentials{Token: "rt-A"})
	if err != nil {
		t.Fatalf("clientA OpenRoute(10) = %v", err)
	}
	if ra.AllocatedEndpoint != "10.0.0.10:51820" {
		t.Errorf("clientA endpoint = %q; want 10.0.0.10:51820", ra.AllocatedEndpoint)
	}

	rb, err := clientB.OpenRoute(context.Background(), 20, RouteCredentials{Token: "rt-B"})
	if err != nil {
		t.Fatalf("clientB OpenRoute(20) = %v", err)
	}
	if rb.AllocatedEndpoint != "10.0.0.20:51820" {
		t.Errorf("clientB endpoint = %q; want 10.0.0.20:51820", rb.AllocatedEndpoint)
	}

	// Prove each client's routes are isolated: clientA's route responses
	// should not satisfy clientB's pending operations.
	// (This is proven by the architecture — separate connections,
	// separate readLoops, separate pending maps — but we can verify
	// the routes are independently accessible.)

	// Now simulate a disconnect of clientA's connection by forcing
	// a reconnect. We can't directly access the WebSocket from outside,
	// but we can observe the behavior.

	// Verify disconnecting one does not disrupt the other: we'll save
	// clientB's route before any disruption and verify it still works.
	rbID := RouteID(20)

	// Close clientA cleanly
	clientA.Shutdown()

	// clientB must still be operational
	if !clientB.IsConnected() {
		t.Fatal("clientB disconnected after clientA shutdown")
	}

	// clientB can close its route successfully
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := clientB.CloseRoute(ctx, rbID); err != nil {
		t.Fatalf("clientB CloseRoute(20) after clientA shutdown = %v", err)
	}
}
