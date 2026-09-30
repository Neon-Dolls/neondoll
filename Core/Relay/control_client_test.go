package relay

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ----- SDK tests: public API surface -----

func TestControlClient_Shutdown(t *testing.T) {
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

	if !client.IsConnected() {
		t.Fatal("IsConnected() = false after Start; want true")
	}

	if err := client.Shutdown(); err != nil {
		t.Fatalf("Shutdown() = %v; want nil", err)
	}

	if client.IsConnected() {
		t.Fatal("IsConnected() = true after Shutdown; want false")
	}

	// Pending operations should return ErrClientClosed
	_, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: "rt"})
	if err != ErrClientClosed && err != ErrNotConnected {
		t.Errorf("OpenRoute after Shutdown = %v; want ErrClientClosed or ErrNotConnected", err)
	}
}

func TestControlClient_IsConnected(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "token"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)

	// Not connected before Start
	if client.IsConnected() {
		t.Fatal("IsConnected() before Start = true; want false")
	}

	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	if !client.IsConnected() {
		t.Fatal("IsConnected() after Start = false; want true")
	}

	client.Shutdown()

	if client.IsConnected() {
		t.Fatal("IsConnected() after Shutdown = true; want false")
	}
}

// ----- Service tunnel integration tests -----

func TestService_StartWithTunnel(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "svc-token"))
	defer srv.Close()

	clientCfg := DefaultClientConfig()
	clientCfg.RelayURL = url
	clientCfg.RegistrationToken = "svc-token"
	clientCfg.HandshakeTimeout = 2 * time.Second
	clientCfg.ReadTimeout = 5 * time.Second
	clientCfg.PingInterval = 30 * time.Second

	svc, err := NewService(DefaultServiceConfig(), clientCfg)
	if err != nil {
		t.Fatalf("NewService() = %v", err)
	}

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		svc.Shutdown(shutdownCtx)
	}()

	if svc.State() != ServiceStateRunning {
		t.Errorf("State() = %s; want running", svc.State())
	}

	client := svc.Client()
	if client == nil {
		t.Fatal("Client() = nil; want non-nil")
	}
	if !client.IsConnected() {
		t.Fatal("Client().IsConnected() = false after Start; want true")
	}
	if client.RelayID() != "test-relay-1" {
		t.Errorf("RelayID() = %q; want %q", client.RelayID(), "test-relay-1")
	}

	// Open a route through the service's client
	opened, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: "rt-1"})
	if err != nil {
		t.Fatalf("OpenRoute() = %v", err)
	}
	if opened.RouteID != 1 {
		t.Errorf("RouteID = %d; want 1", opened.RouteID)
	}
}

func TestService_StartWithTunnel_FailsOnBadToken(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "expected-token"))
	defer srv.Close()

	clientCfg := DefaultClientConfig()
	clientCfg.RelayURL = url
	clientCfg.RegistrationToken = "wrong-token"
	clientCfg.HandshakeTimeout = 2 * time.Second
	clientCfg.ReadTimeout = 5 * time.Second
	clientCfg.PingInterval = 30 * time.Second

	svc, err := NewService(DefaultServiceConfig(), clientCfg)
	if err != nil {
		t.Fatalf("NewService() = %v", err)
	}

	err = svc.Start(context.Background())
	if err == nil {
		t.Fatal("Start() = nil; want tunnel error")
	}
	if !strings.Contains(err.Error(), "control tunnel") {
		t.Errorf("Start() error = %v; want tunnel error", err)
	}

	// After failure, service should be in Stopped state, not Running
	if svc.State() == ServiceStateRunning {
		t.Fatal("State() = running after failed tunnel start; want stopped")
	}
}

func TestService_DiagnosticsIncludeTunnel(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "diag-token"))
	defer srv.Close()

	clientCfg := DefaultClientConfig()
	clientCfg.RelayURL = url
	clientCfg.RegistrationToken = "diag-token"
	clientCfg.HandshakeTimeout = 2 * time.Second
	clientCfg.ReadTimeout = 5 * time.Second
	clientCfg.PingInterval = 30 * time.Second

	svc, err := NewService(DefaultServiceConfig(), clientCfg)
	if err != nil {
		t.Fatalf("NewService() = %v", err)
	}

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		svc.Shutdown(shutdownCtx)
	}()

	diag := svc.Diagnostics()
	if diag["tunnel_connected"] != true {
		t.Errorf("diagnostics tunnel_connected = %v; want true", diag["tunnel_connected"])
	}
	if diag["relay_id"] != "test-relay-1" {
		t.Errorf("diagnostics relay_id = %v; want test-relay-1", diag["relay_id"])
	}
}

func TestService_NoTunnelWhenNotConfigured(t *testing.T) {
	svc, err := NewService(DefaultServiceConfig(), ClientConfig{})
	if err != nil {
		t.Fatalf("NewService() = %v", err)
	}

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		svc.Shutdown(shutdownCtx)
	}()

	if svc.Client() != nil {
		t.Fatal("Client() = non-nil without RelayURL; want nil")
	}

	diag := svc.Diagnostics()
	if diag["tunnel_connected"] != false {
		t.Errorf("diagnostics tunnel_connected = %v; want false", diag["tunnel_connected"])
	}
}

func TestService_ShutdownWithTunnel(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "shutdown-token"))
	defer srv.Close()

	clientCfg := DefaultClientConfig()
	clientCfg.RelayURL = url
	clientCfg.RegistrationToken = "shutdown-token"
	clientCfg.HandshakeTimeout = 2 * time.Second
	clientCfg.ReadTimeout = 5 * time.Second
	clientCfg.PingInterval = 30 * time.Second

	svc, err := NewService(DefaultServiceConfig(), clientCfg)
	if err != nil {
		t.Fatalf("NewService() = %v", err)
	}

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	// Verify tunnel is up
	if !svc.Client().IsConnected() {
		t.Fatal("tunnel not connected before shutdown")
	}

	// Shutdown with a short timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := svc.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() = %v; want nil", err)
	}

	if svc.State() != ServiceStateStoppedClean {
		t.Errorf("State() = %s; want stopped_clean", svc.State())
	}
}
