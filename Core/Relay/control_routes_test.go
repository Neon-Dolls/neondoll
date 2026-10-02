package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
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

func TestControlClient_OpenRoute_RejectsZeroRouteID(t *testing.T) {
	client := NewControlClient(DefaultClientConfig())
	_, err := client.OpenRoute(context.Background(), 0, RouteCredentials{Token: "x"})
	if err == nil {
		t.Fatal("OpenRoute(0) = nil; expected error")
	}
}

func TestControlClient_OpenRoute_RejectsEmptyCredentials(t *testing.T) {
	client := NewControlClient(DefaultClientConfig())
	_, err := client.OpenRoute(context.Background(), 1, RouteCredentials{})
	if err == nil {
		t.Fatal("OpenRoute with empty credentials = nil; expected error")
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

	_, err := client.OpenRoute(context.Background(), 2, RouteCredentials{Token: "route-token"})
	if err != nil {
		t.Fatalf("OpenRoute() = %v", err)
	}

	if err := client.CloseRoute(context.Background(), 2); err != nil {
		t.Fatalf("CloseRoute() = %v", err)
	}
}

func TestControlClient_CloseRoute_RejectsZeroRouteID(t *testing.T) {
	client := NewControlClient(DefaultClientConfig())
	err := client.CloseRoute(context.Background(), 0)
	if err == nil {
		t.Fatal("CloseRoute(0) = nil; expected error")
	}
}

// ── M4.6 Integration Tests: credential verification through real ControlServer ──

// findFreePort returns a TCP address on a free port (e.g. "127.0.0.1:54321").
// The caller must start listening before calling connect, as the port is only
// reserved during the call.
func findFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("findFreePort: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// clientRegID extracts the registration ID that the ControlServer assigned
// to a ControlClient's connection. Must be called after the client has
// completed registration (i.e. waitConnected succeeded).
func clientRegID(t *testing.T, cs *ControlServer) RegistrationID {
	t.Helper()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for id := range cs.conns {
		return id
	}
	t.Fatal("no connected cores in cs.conns")
	return ""
}

// TestControlServer_RouteCredentials_CorrectCredential proves that the
// ControlServer's RouteOpen handler accepts a correctly-presented credential
// against the route's independently established credential.
//
// M4.1/M4.2 contract: route credentials are independently established.
// RouteOpen must verify the presented credential against the established one.
// A matching credential must open the route.
func TestControlServer_RouteCredentials_CorrectCredential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	svcCfg := DefaultServiceConfig()
	// Set Relay credentials so registration passes.
	hash := sha256.Sum256([]byte("test-token-1"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	// Connect a real ControlClient.
	cfg := DefaultClientConfig()
	cfg.RelayURL = "ws://" + addr + "/relay"
	cfg.RegistrationToken = "test-token-1"
	cfg.HandshakeTimeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	defer client.Shutdown()
	waitConnected(t, client, "cred-correct")

	// Retrieve the regID the ControlServer assigned to this connection.
	regID := clientRegID(t, cs)

	// Pre-allocate a route under this regID so that the RouteOpen handler
	// hits the existing-route path (credential verification codepath).
	_, err = svc.Registry().AllocateRoute(regID, RouteID(60))
	if err != nil {
		t.Fatalf("AllocateRoute: %v", err)
	}

	// Retrieve the server-generated credentials.
	stored, err := svc.Registry().RouteCredentialsFromEntry(RouteID(60))
	if err != nil {
		t.Fatalf("RouteCredentialsFromEntry: %v", err)
	}
	if stored.Token == "" {
		t.Fatal("server-generated route credentials token is empty")
	}

	// Open the route with the CORRECT credentials → must succeed.
	opened, err := client.OpenRoute(ctx, 60, stored)
	if err != nil {
		t.Fatalf("OpenRoute with correct creds = %v; want success", err)
	}
	if opened.RouteID != 60 {
		t.Errorf("RouteID = %d; want 60", opened.RouteID)
	}
	if opened.AllocatedEndpoint == "" {
		t.Error("AllocatedEndpoint is empty; want non-empty")
	}
}

// TestControlServer_RouteCredentials_WrongCredential proves that the
// ControlServer's RouteOpen handler rejects a wrong credential with an
// auth error, fail-closed.
//
// M4.1/M4.2 contract: a wrong credential must fail closed with the
// appropriate authentication error.
func TestControlServer_RouteCredentials_WrongCredential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	svcCfg := DefaultServiceConfig()
	// Set Relay credentials so registration passes.
	hash := sha256.Sum256([]byte("test-token-2"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)
	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	cfg := DefaultClientConfig()
	cfg.RelayURL = "ws://" + addr + "/relay"
	cfg.RegistrationToken = "test-token-2"
	cfg.HandshakeTimeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	if err := client.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	defer client.Shutdown()
	waitConnected(t, client, "cred-wrong")

	regID := clientRegID(t, cs)

	// Pre-allocate route with server-generated creds.
	_, err = svc.Registry().AllocateRoute(regID, RouteID(61))
	if err != nil {
		t.Fatalf("AllocateRoute: %v", err)
	}

	// Retrieve the actual server-generated creds so we know what they are.
	stored, err := svc.Registry().RouteCredentialsFromEntry(RouteID(61))
	if err != nil {
		t.Fatalf("RouteCredentialsFromEntry: %v", err)
	}
	if stored.Token == "" {
		t.Fatal("server-generated route credentials token is empty")
	}

	// Present WRONG credentials → must fail with auth error.
	wrongCreds := RouteCredentials{Token: "definitely-not-the-server-generated-token"}
	if wrongCreds.Token == stored.Token {
		t.Fatal("test invariant: wrong token should differ from stored token")
	}

	_, err = client.OpenRoute(ctx, 61, wrongCreds)
	if err == nil {
		t.Fatal("OpenRoute with wrong creds = nil; expected auth error")
	}
	if !strings.Contains(err.Error(), "auth_failed") &&
		!strings.Contains(err.Error(), "route credentials mismatch") {
		t.Errorf("OpenRoute error = %q; want auth error (auth_failed or route credentials mismatch)", err)
	}
}

// TestControlServer_RegistrationIDGenerationFailure proves that when
// RegistrationID generation fails (crypto/rand unavailable), the server
// does NOT panic and creates NO registration state. The connection is
// rejected with an internal error, and the server remains operational.
//
// The smallest test seam: override the ControlServer's generateRegID
// field with a function that always returns an error.
func TestControlServer_RegistrationIDGenerationFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	svcCfg := DefaultServiceConfig()
	hash := sha256.Sum256([]byte("fail-reg-token"))
	svcCfg.Credentials = []string{hex.EncodeToString(hash[:])}
	svc, err := NewService(svcCfg, ClientConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}
	defer func() {
		sc, scCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scCancel()
		_ = svc.Shutdown(sc)
	}()

	addr := findFreePort(t)
	cs := NewControlServer(svc, addr, nil)

	// Smallest test seam: override generateRegID to simulate crypto/rand failure.
	cs.generateRegID = func() (RegistrationID, error) {
		return RegistrationID(""), fmt.Errorf("crypto/rand: simulated failure")
	}

	csCtx, csCancel := context.WithCancel(ctx)
	defer csCancel()
	if err := cs.Start(csCtx); err != nil {
		t.Fatalf("cs.Start: %v", err)
	}

	// Verify server is alive before connecting.
	if cs.Addr() == "" {
		t.Fatal("ControlServer not listening before test")
	}
	if cs.ActiveRegistrations() != 0 {
		t.Errorf("ActiveRegistrations before connect = %d; want 0", cs.ActiveRegistrations())
	}
	if svc.Registry().RegistrationCount() != 0 {
		t.Errorf("RegistrationCount before connect = %d; want 0", svc.Registry().RegistrationCount())
	}

	// Attempt client registration — must fail because generateRegID fails.
	cfg := DefaultClientConfig()
	cfg.RelayURL = "ws://" + addr + "/relay"
	cfg.RegistrationToken = "fail-reg-token"
	cfg.HandshakeTimeout = 5 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	err = client.Start(ctx)
	if err == nil {
		client.Shutdown()
		t.Fatal("client.Start should have failed (generateRegID simulated failure)")
	}
	defer client.Shutdown()

	// Verify the error mentions registration or internal error.
	if !strings.Contains(err.Error(), "registration") &&
		!strings.Contains(err.Error(), "internal_error") {
		t.Errorf("error = %q; want something containing 'registration' or 'internal_error'", err.Error())
	}

	// Verify NO registration state was created anywhere.
	if cs.ActiveRegistrations() != 0 {
		t.Errorf("ActiveRegistrations after failure = %d; want 0 (no state created)", cs.ActiveRegistrations())
	}
	if svc.Registry().RegistrationCount() != 0 {
		t.Errorf("RegistrationCount after failure = %d; want 0 (no state created)", svc.Registry().RegistrationCount())
	}

	// Verify server is still running (did not panic / crash).
	if cs.Addr() == "" {
		t.Error("ControlServer is not running after failed registration")
	}
	if cs.ActiveRegistrations() != 0 {
		t.Errorf("server is in inconsistent state after failure: ActiveRegistrations = %d", cs.ActiveRegistrations())
	}
}
