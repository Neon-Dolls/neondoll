package relay

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ----- helpers -----

// fakeRelayHandler returns a WebSocket handler that simulates a Relay.
// It validates the token, then accepts route operations.
func fakeRelayHandler(t *testing.T, expectedToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Expect Register
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := UnmarshalControl(raw)
		if err != nil {
			return
		}
		reg, ok := msg.(*Register)
		if !ok {
			return
		}

		if reg.Token != expectedToken {
			data, _ := MarshalControl(&RelayError{
				Type:    CmdError,
				Code:    ErrAuthFailed,
				Message: "invalid token",
			})
			conn.WriteMessage(websocket.TextMessage, data)
			return
		}

		// Accept registration
		data, _ := MarshalControl(&Registered{
			Type:    CmdRegistered,
			RelayID: RelayID("test-relay-1"),
		})
		conn.WriteMessage(websocket.TextMessage, data)

		// Process further messages until connection closes
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			msg, err := UnmarshalControl(raw)
			if err != nil {
				continue
			}

			switch m := msg.(type) {
			case *RouteOpen:
				if m.RouteID == 0 || m.Credentials.Token == "" {
					rerr, _ := MarshalControl(&RelayError{
						Type:    CmdError,
						Code:    ErrAuthFailed,
						Message: "invalid route credentials",
						RouteID: m.RouteID,
					})
					conn.WriteMessage(websocket.TextMessage, rerr)
					continue
				}
				resp, _ := MarshalControl(&RouteOpened{
					Type:              CmdRouteOpened,
					RouteID:           m.RouteID,
					AllocatedEndpoint: "127.0.0.1:51820",
				})
				conn.WriteMessage(websocket.TextMessage, resp)

			case *RouteClose:
				resp, _ := MarshalControl(&RouteClosed{
					Type:    CmdRouteClosed,
					RouteID: m.RouteID,
				})
				conn.WriteMessage(websocket.TextMessage, resp)

			default:
				// Unknown — ignore
			}
		}
	}
}

// startFakeRelay starts an HTTP server that upgrades to WebSocket at /relay
// and returns the server plus the ws:// URL.
func startFakeRelay(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/relay", handler)
	srv := httptest.NewServer(mux)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/relay"
	return srv, wsURL
}

// ----- ControlClient tests -----

func TestControlClient_ConnectAndRegister(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "valid-token"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "valid-token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second // disable rapid pings in test

	client := NewControlClient(cfg)
	err := client.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() = %v; want nil", err)
	}
	defer client.Shutdown()

	if !client.IsConnected() {
		t.Fatal("IsConnected() = false; want true")
	}
	if client.RelayID() != "test-relay-1" {
		t.Errorf("RelayID() = %q; want %q", client.RelayID(), "test-relay-1")
	}
}

func TestControlClient_RegisterAuthFailed(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "expected-token"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "wrong-token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	err := client.Start(context.Background())
	if err == nil {
		t.Fatal("Start() = nil; want registration error")
	}
	if !strings.Contains(err.Error(), ErrRegistrationFailed.Error()) {
		t.Errorf("Start() error = %v; want %v", err, ErrRegistrationFailed)
	}
	if client.IsConnected() {
		t.Fatal("IsConnected() = true after failed registration; want false")
	}
}

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

	opened, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: "route-token-1"})
	if err != nil {
		t.Fatalf("OpenRoute() = %v; want nil", err)
	}
	if opened.RouteID != 1 {
		t.Errorf("RouteID = %d; want 1", opened.RouteID)
	}
	if opened.AllocatedEndpoint != "127.0.0.1:51820" {
		t.Errorf("AllocatedEndpoint = %q; want 127.0.0.1:51820", opened.AllocatedEndpoint)
	}
}

func TestControlClient_OpenRouteRejected(t *testing.T) {
	// Use a handler that rejects open routes with empty credentials
	handler := func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, raw, _ := conn.ReadMessage()
		msg, _ := UnmarshalControl(raw)
		reg, _ := msg.(*Register)
		if reg.Token != "token" {
			return
		}
		data, _ := MarshalControl(&Registered{Type: CmdRegistered, RelayID: "test-relay"})
		conn.WriteMessage(websocket.TextMessage, data)

		_, raw, _ = conn.ReadMessage()
		msg, _ = UnmarshalControl(raw)
		ro, ok := msg.(*RouteOpen)
		if !ok {
			return
		}
		errResp, _ := MarshalControl(&RelayError{
			Type:    CmdError,
			Code:    ErrAuthFailed,
			Message: "invalid route credentials",
			RouteID: ro.RouteID,
		})
		conn.WriteMessage(websocket.TextMessage, errResp)
	}

	srv, url := startFakeRelay(t, handler)
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

	_, err := client.OpenRoute(context.Background(), 42, RouteCredentials{Token: "bad-creds"})
	if err == nil {
		t.Fatal("OpenRoute() = nil; want error")
	}
	if !strings.Contains(err.Error(), ErrAuthFailed) {
		t.Errorf("OpenRoute() error = %v; want %q in error", err, ErrAuthFailed)
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

	// Open a route first
	_, err := client.OpenRoute(context.Background(), 7, RouteCredentials{Token: "rt"})
	if err != nil {
		t.Fatalf("OpenRoute() = %v", err)
	}

	// Close it
	if err := client.CloseRoute(context.Background(), 7); err != nil {
		t.Fatalf("CloseRoute() = %v; want nil", err)
	}
}

func TestControlClient_ReconnectAndRestoreRoutes(t *testing.T) {
	// We'll use a handler that tracks whether it's the first or second connection
	var connectionCount atomic.Int32

	handler := func(w http.ResponseWriter, r *http.Request) {
		connCount := connectionCount.Add(1)
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Accept Register
		_, raw, _ := conn.ReadMessage()
		msg, _ := UnmarshalControl(raw)
		reg, _ := msg.(*Register)
		if reg.Token != "token" {
			data, _ := MarshalControl(&RelayError{Type: CmdError, Code: ErrAuthFailed, Message: "bad token"})
			conn.WriteMessage(websocket.TextMessage, data)
			return
		}

		data, _ := MarshalControl(&Registered{Type: CmdRegistered, RelayID: "test-relay"})
		conn.WriteMessage(websocket.TextMessage, data)

		// On the first connection, accept a RouteOpen, then close.
		// On subsequent connections, we should see RouteOpen for the same route.
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			msg, err := UnmarshalControl(raw)
			if err != nil {
				continue
			}

			switch m := msg.(type) {
			case *RouteOpen:
				resp, _ := MarshalControl(&RouteOpened{
					Type:              CmdRouteOpened,
					RouteID:           m.RouteID,
					AllocatedEndpoint: "192.168.1.1:51820",
				})
				conn.WriteMessage(websocket.TextMessage, resp)

				// On first connection, close the connection after accepting RouteOpen
				if connCount == 1 {
					conn.WriteControl(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, "simulated-drop"),
						time.Now().Add(time.Second))
					return
				}

			default:
				// ignore
			}
		}
	}

	srv, url := startFakeRelay(t, handler)
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 3 * time.Second // short timeout so reconnection is quick
	cfg.PingInterval = 30 * time.Second
	cfg.ReconnectInitial = 100 * time.Millisecond
	cfg.ReconnectMax = 500 * time.Millisecond
	cfg.ReconnectMultiplier = 1.5
	cfg.ReconnectJitter = 50 * time.Millisecond

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer client.Shutdown()

	// Open a route — this triggers the first connection
	_, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: "route-token"})
	if err != nil {
		t.Fatalf("OpenRoute() = %v", err)
	}

	// Wait for reconnection and route restoration
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsConnected() && connectionCount.Load() >= 2 && client.RoutesRestored.Load() >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !client.IsConnected() {
		t.Fatal("client did not reconnect within deadline")
	}
	if connectionCount.Load() < 2 {
		t.Errorf("connectionCount = %d; want >= 2", connectionCount.Load())
	}
	if client.RoutesRestored.Load() == 0 {
		t.Errorf("RoutesRestored = %d; want >= 1", client.RoutesRestored.Load())
	}

	// Verify the route is still tracked
	metrics := client.Metrics()
	if metrics.RoutesRestored == 0 {
		t.Errorf("Metrics().RoutesRestored = %d; want >= 1", metrics.RoutesRestored)
	}
	if metrics.Reconnects == 0 {
		t.Errorf("Metrics().Reconnects = %d; want >= 1", metrics.Reconnects)
	}
}

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

func TestControlClient_ReconnectClosesOldSocket(t *testing.T) {
	// Verify that repeated disconnect/reconnect does not leak
	// goroutines or WebSocket connections.
	// Handler closes itself on connections 1 and 2 (forcing two reconnects),
	// then stays alive on connection 3+.
	var connCount atomic.Int32

	handler := func(w http.ResponseWriter, r *http.Request) {
		cn := connCount.Add(1)
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			msg, err := UnmarshalControl(raw)
			if err != nil {
				continue
			}

			switch m := msg.(type) {
			case *Register:
				resp, _ := MarshalControl(&Registered{
					Type:    CmdRegistered,
					RelayID: "relay-A",
				})
				conn.WriteMessage(websocket.TextMessage, resp)

				// Close after Register on connections 1 and 2 to force reconnects
				if cn <= 2 {
					return
				}

			case *RouteOpen:
				resp, _ := MarshalControl(&RouteOpened{
					Type:              CmdRouteOpened,
					RouteID:           m.RouteID,
					AllocatedEndpoint: "10.0.0.1:51820",
				})
				conn.WriteMessage(websocket.TextMessage, resp)

			case *RouteClose:
				resp, _ := MarshalControl(&RouteClosed{
					Type:    CmdRouteClosed,
					RouteID: m.RouteID,
				})
				conn.WriteMessage(websocket.TextMessage, resp)

			default:
			}
		}
	}

	srv, url := startFakeRelay(t, handler)
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 3 * time.Second
	cfg.PingInterval = 30 * time.Second
	cfg.ReconnectInitial = 50 * time.Millisecond
	cfg.ReconnectMax = 200 * time.Millisecond

	goroutinesBefore := runtime.NumGoroutine()

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer client.Shutdown()

	// Wait for connection to stabilize through reconnect cycles.
	// Connection 1 closes after Register → reconnect.
	// Connection 2 closes after Register → reconnect.
	// Connection 3 stays alive — wait until it is connected, registered,
	// and has established all three connections.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsConnected() && connCount.Load() >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !client.IsConnected() || connCount.Load() < 3 {
		t.Fatalf("client not stable after reconnect cycles: connected=%v connCount=%d",
			client.IsConnected(), connCount.Load())
	}

	// Now open routes on the stable connection 3
	for _, id := range []RouteID{1, 2, 3} {
		r, err := client.OpenRoute(context.Background(), id, RouteCredentials{Token: "rt-" + strconv.Itoa(int(id))})
		if err != nil {
			t.Fatalf("OpenRoute(%d) = %v", id, err)
		}
		if r.AllocatedEndpoint != "10.0.0.1:51820" {
			t.Errorf("route %d endpoint = %q; want 10.0.0.1:51820", int(id), r.AllocatedEndpoint)
		}
	}

	// Verify goroutine count hasn't grown significantly
	goroutinesAfter := runtime.NumGoroutine()
	leaked := goroutinesAfter - goroutinesBefore
	if leaked > 10 {
		t.Fatalf("goroutine leak: %d -> %d (+%d)", goroutinesBefore, goroutinesAfter, leaked)
	}
	t.Logf("goroutines: %d -> %d (+%d)", goroutinesBefore, goroutinesAfter, leaked)
}

// errClosedWS is used by test handlers to signal a forced close.
type errClosedWS struct{}

func (e *errClosedWS) Error() string { return "test: ws closed" }

// waitConnected blocks until the client reports connected, with a timeout.
func waitConnected(t *testing.T, client *ControlClient, label string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsConnected() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("[%s] client did not reconnect within 5s deadline", label)
}

// ----- M4.3: Validate received control messages -----

func TestControlClient_InvalidControlMessages(t *testing.T) {
	// Sub-tests proving fail-closed behavior for each message type.

	t.Run("Registered empty RelayID", func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()

			// Read Register
			_, raw, _ := conn.ReadMessage()
			msg, _ := UnmarshalControl(raw)
			if _, ok := msg.(*Register); !ok {
				return
			}

			// Send Registered with empty RelayID — MUST be rejected
			data, _ := MarshalControl(&Registered{Type: CmdRegistered, RelayID: ""})
			conn.WriteMessage(websocket.TextMessage, data)
		}

		srv, url := startFakeRelay(t, handler)
		defer srv.Close()

		cfg := DefaultClientConfig()
		cfg.RelayURL = url
		cfg.RegistrationToken = "token"
		cfg.HandshakeTimeout = 500 * time.Millisecond

		client := NewControlClient(cfg)
		err := client.Start(context.Background())
		if err == nil {
			client.Shutdown()
			t.Fatal("Start() succeeded; expected error for empty RelayID")
		}
		if !strings.Contains(err.Error(), "invalid Registered") && !strings.Contains(err.Error(), "validation") {
			t.Errorf("Start() error = %v; want invalid Registered error", err)
		}
	})

	t.Run("RouteOpened missing endpoint", func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()

			// Handshake normally
			_, raw, _ := conn.ReadMessage()
			msg, _ := UnmarshalControl(raw)
			if _, ok := msg.(*Register); !ok {
				return
			}
			data, _ := MarshalControl(&Registered{Type: CmdRegistered, RelayID: "test-relay"})
			conn.WriteMessage(websocket.TextMessage, data)

			// Read the RouteOpen
			_, raw, _ = conn.ReadMessage()
			msg, _ = UnmarshalControl(raw)
			ro, ok := msg.(*RouteOpen)
			if !ok {
				return
			}

			// Send RouteOpened with missing AllocatedEndpoint — must be rejected
			badResp, _ := MarshalControl(&RouteOpened{
				Type:    CmdRouteOpened,
				RouteID: ro.RouteID,
			})
			conn.WriteMessage(websocket.TextMessage, badResp)
		}

		srv, url := startFakeRelay(t, handler)
		defer srv.Close()

		cfg := DefaultClientConfig()
		cfg.RelayURL = url
		cfg.RegistrationToken = "token"
		cfg.HandshakeTimeout = 500 * time.Millisecond
		cfg.ReadTimeout = 1 * time.Second

		client := NewControlClient(cfg)
		if err := client.Start(context.Background()); err != nil {
			t.Fatalf("Start() = %v", err)
		}
		defer client.Shutdown()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := client.OpenRoute(ctx, 1, RouteCredentials{Token: "rt-1"})
		if err == nil {
			t.Fatal("OpenRoute() succeeded; expected error for missing endpoint")
		}
	})

	t.Run("RouteClosed missing RouteID", func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()

			// Normal handshake
			_, raw, _ := conn.ReadMessage()
			msg, _ := UnmarshalControl(raw)
			if _, ok := msg.(*Register); !ok {
				return
			}
			data, _ := MarshalControl(&Registered{Type: CmdRegistered, RelayID: "test-relay"})
			conn.WriteMessage(websocket.TextMessage, data)

			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					return
				}
				msg, err := UnmarshalControl(raw)
				if err != nil {
					continue
				}
				switch m := msg.(type) {
				case *RouteOpen:
					resp, _ := MarshalControl(&RouteOpened{
						Type:              CmdRouteOpened,
						RouteID:           m.RouteID,
						AllocatedEndpoint: "10.0.0.1:51820",
					})
					conn.WriteMessage(websocket.TextMessage, resp)

				case *RouteClose:
					// Respond with RouteClosed missing RouteID — must be rejected
					badResp, _ := MarshalControl(&RouteClosed{
						Type: CmdRouteClosed,
					})
					conn.WriteMessage(websocket.TextMessage, badResp)
				}
			}
		}

		srv, url := startFakeRelay(t, handler)
		defer srv.Close()

		cfg := DefaultClientConfig()
		cfg.RelayURL = url
		cfg.RegistrationToken = "token"
		cfg.HandshakeTimeout = 500 * time.Millisecond
		cfg.ReadTimeout = 1 * time.Second

		client := NewControlClient(cfg)
		if err := client.Start(context.Background()); err != nil {
			t.Fatalf("Start() = %v", err)
		}
		defer client.Shutdown()

		_, err := client.OpenRoute(context.Background(), 1, RouteCredentials{Token: "rt-1"})
		if err != nil {
			t.Fatalf("OpenRoute() = %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = client.CloseRoute(ctx, 1)
		if err == nil {
			t.Fatal("CloseRoute() succeeded; expected error for missing RouteID")
		}
	})

	t.Run("RelayError empty code", func(t *testing.T) {
		handler := func(w http.ResponseWriter, r *http.Request) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()

			// Normal handshake
			_, raw, _ := conn.ReadMessage()
			msg, _ := UnmarshalControl(raw)
			if _, ok := msg.(*Register); !ok {
				return
			}
			data, _ := MarshalControl(&Registered{Type: CmdRegistered, RelayID: "test-relay"})
			conn.WriteMessage(websocket.TextMessage, data)

			// Read the RouteOpen, send a RelayError with empty code — must be dropped
			_, raw, _ = conn.ReadMessage()
			msg, _ = UnmarshalControl(raw)
			if _, ok := msg.(*RouteOpen); ok {
				// Send RelayError with empty code (invalid)
				badResp, _ := MarshalControl(&RelayError{
					Type:    CmdError,
					RouteID: 1,
					Message: "access denied",
				})
				conn.WriteMessage(websocket.TextMessage, badResp)
			}
		}

		srv, url := startFakeRelay(t, handler)
		defer srv.Close()

		cfg := DefaultClientConfig()
		cfg.RelayURL = url
		cfg.RegistrationToken = "token"
		cfg.HandshakeTimeout = 500 * time.Millisecond
		cfg.ReadTimeout = 1 * time.Second

		client := NewControlClient(cfg)
		if err := client.Start(context.Background()); err != nil {
			t.Fatalf("Start() = %v", err)
		}
		defer client.Shutdown()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := client.OpenRoute(ctx, 1, RouteCredentials{Token: "rt-1"})
		if err == nil {
			t.Fatal("OpenRoute() succeeded; expected error for empty code RelayError")
		}
	})
}

// ----- M4.3: Pending ops fail on connection loss -----

func TestControlClient_PendingOpsFailOnDisconnect(t *testing.T) {
	// Handler: conducts a normal handshake, then for the first RouteOpen
	// on the initial connection (connection count 1), reads the request
	// but closes without sending a response — leaving a pending operation.
	// The client should promptly fail that pending operation with
	// ErrConnectionLost when the readLoop exits and reconnect triggers.
	var connCount atomic.Int32

	handler := func(w http.ResponseWriter, r *http.Request) {
		cn := connCount.Add(1)
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Normal Register
		_, raw, _ := conn.ReadMessage()
		msg, _ := UnmarshalControl(raw)
		if _, ok := msg.(*Register); !ok {
			return
		}
		data, _ := MarshalControl(&Registered{
			Type:    CmdRegistered,
			RelayID: RelayID("test-relay"),
		})
		conn.WriteMessage(websocket.TextMessage, data)

		// On the first connection, read a RouteOpen but don't respond —
		// just close, leaving a pending operation.
		if cn == 1 {
			_, raw, _ := conn.ReadMessage()
			msg, _ := UnmarshalControl(raw)
			if _, ok := msg.(*RouteOpen); ok {
				// Close without responding — pending operation will fail
				conn.Close()
				return
			}
		}

		// On subsequent connections (reconnect), respond normally.
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			msg, err := UnmarshalControl(raw)
			if err != nil {
				continue
			}
			switch m := msg.(type) {
			case *RouteOpen:
				resp, _ := MarshalControl(&RouteOpened{
					Type:              CmdRouteOpened,
					RouteID:           m.RouteID,
					AllocatedEndpoint: "10.0.0.1:51820",
				})
				conn.WriteMessage(websocket.TextMessage, resp)
			default:
			}
		}
	}

	srv, url := startFakeRelay(t, handler)
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "token"
	cfg.HandshakeTimeout = 500 * time.Millisecond
	cfg.ReadTimeout = 2 * time.Second
	cfg.PingInterval = 30 * time.Second
	cfg.ReconnectInitial = 100 * time.Millisecond
	cfg.ReconnectMax = 500 * time.Millisecond

	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	defer client.Shutdown()

	// Wait for initial registration
	if !client.IsConnected() {
		t.Fatal("client not connected after Start")
	}

	// Open a route; the first connection will close without responding,
	// leaving this operation pending. It should fail with ErrConnectionLost.
	_, err := client.OpenRoute(context.Background(), 42, RouteCredentials{Token: "rt-42"})
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("OpenRoute() error = %v; want ErrConnectionLost", err)
	}

	// After the first connection fails, the client should reconnect and
	// subsequent operations should succeed on the new connection.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsConnected() && connCount.Load() >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !client.IsConnected() {
		t.Fatal("client did not reconnect after connection loss")
	}
	if connCount.Load() < 2 {
		t.Fatalf("connCount = %d; want >= 2 (two connections)", connCount.Load())
	}

	// Now a new OpenRoute should succeed on the reconnected session
	_, err = client.OpenRoute(context.Background(), 43, RouteCredentials{Token: "rt-43"})
	if err != nil {
		t.Fatalf("OpenRoute() after reconnect = %v", err)
	}
}

// ----- M4.3: Two-Core isolation integration test -----

// multiClientRelayHandler accepts registrations with any non-empty token
// and manages independent routes for each client.
func multiClientRelayHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Register
		_, raw, _ := conn.ReadMessage()
		msg, _ := UnmarshalControl(raw)
		reg, ok := msg.(*Register)
		if !ok || reg.Token == "" {
			return
		}
		data, _ := MarshalControl(&Registered{
			Type:    CmdRegistered,
			RelayID: RelayID("multi-relay"),
		})
		conn.WriteMessage(websocket.TextMessage, data)

		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			msg, err := UnmarshalControl(raw)
			if err != nil {
				continue
			}
			switch m := msg.(type) {
			case *RouteOpen:
				resp, _ := MarshalControl(&RouteOpened{
					Type:              CmdRouteOpened,
					RouteID:           m.RouteID,
					AllocatedEndpoint: "10.0.0." + strconv.Itoa(int(m.RouteID)) + ":51820",
				})
				conn.WriteMessage(websocket.TextMessage, resp)
			case *RouteClose:
				resp, _ := MarshalControl(&RouteClosed{
					Type:    CmdRouteClosed,
					RouteID: m.RouteID,
				})
				conn.WriteMessage(websocket.TextMessage, resp)
			default:
			}
		}
	}
}

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
