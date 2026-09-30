package relay

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestControlClient_ReconnectAndRestoreRoutes(t *testing.T) {
	var connectionCount atomic.Int32

	handler := func(w http.ResponseWriter, r *http.Request) {
		cn := connectionCount.Add(1)
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := UnmarshalControl(raw)
		if err != nil {
			return
		}
		if _, ok := msg.(*Register); !ok {
			return
		}

		resp, _ := MarshalControl(&Registered{
			Type:    CmdRegistered,
			RelayID: "relay-A",
		})
		conn.WriteMessage(websocket.TextMessage, resp)

		// On first connection, accept one RouteOpen then close to force reconnect
		if cn == 1 {
			// Read RouteOpen and respond
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			msg, err := UnmarshalControl(raw)
			if err != nil {
				return
			}
			m, ok := msg.(*RouteOpen)
			if !ok {
				return
			}
			resp, _ := MarshalControl(&RouteOpened{
				Type:              CmdRouteOpened,
				RouteID:           m.RouteID,
				AllocatedEndpoint: "10.0.0.1:51820",
			})
			conn.WriteMessage(websocket.TextMessage, resp)
			return
		}

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
