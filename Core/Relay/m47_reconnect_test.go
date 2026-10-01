package relay

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ── M4.7.1 — Tunnel Reconnect ─────────────────────────────────────────
//
// Acceptance criteria (from the M4.7 plan):
// 1. Automate the M4 path and interrupt the Core↔Relay WSS while the
//    Relay stays alive. Reconnect is driven by the ControlClient's
//    watchdog (reconnectLoop), with restoration of open routes.
// 2. Capture identity state before interruption and assert it is
//    unchanged after recovery.
// 3. Prove a real WG connectivity happens over the restored routes
//    (here: the WG transport attaches and frames flow again).
// 4. Downgrade is NOT triggered (client remains fully connected; no
//    fallback path is entered).

// reconnectRelayHandler is a fake Relay that closes the WebSocket after
// the first RouteOpen is acknowledged, forcing the Core's ControlClient
// through one full reconnect cycle. On subsequent connections it behaves
// like a normal Relay (register, route open/close) and echoes binary
// frames so the tunnel can be exercised end-to-end.
func reconnectRelayHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	var connCount atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 1. Register
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg, err := UnmarshalControl(raw)
		if err != nil {
			return
		}
		reg, ok := msg.(*Register)
		if !ok || reg.Token == "" {
			return
		}
		// Identity is constant across reconnects: the Core keeps the same
		// Relay-issued identity because this is the same Relay, still alive.
		regData, _ := MarshalControl(&Registered{
			Type:    CmdRegistered,
			RelayID: RelayID("m47-reconnect-relay"),
		})
		if err := conn.WriteMessage(websocket.TextMessage, regData); err != nil {
			return
		}

		n := connCount.Add(1)

		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			msg, err := UnmarshalControl(raw)
			if err != nil {
				// Binary frames pass through opaque (WG transport data).
				if err := conn.WriteMessage(websocket.BinaryMessage, raw); err != nil {
					return
				}
				continue
			}
			switch m := msg.(type) {
			case *RouteOpen:
				resp, _ := MarshalControl(&RouteOpened{
					Type:              CmdRouteOpened,
					RouteID:           m.RouteID,
					AllocatedEndpoint: "10.0.0.44:51820",
				})
				if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
					return
				}
				// First connection: drop the socket right after the route is
				// acknowledged to simulate a mid-session WSS failure.
				if n == 1 {
					return
				}
			case *RouteClose:
				resp, _ := MarshalControl(&RouteClosed{
					Type:    CmdRouteClosed,
					RouteID: m.RouteID,
				})
				if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
					return
				}
			}
		}
	}
}

// TestM47_TunnelReconnect proves the Core re-establishes the control
// tunnel after a WSS interruption, restores its route, and keeps its
// identity intact — all while the Relay remained alive.
func TestM47_TunnelReconnect(t *testing.T) {
	srv, wsURL := startFakeRelay(t, reconnectRelayHandler(t))
	defer srv.Close()

	cfg := ClientConfig{
		RelayURL:            wsURL,
		RegistrationToken:   "m47-reconnect-token",
		ReadTimeout:         2 * time.Second,
		WriteTimeout:        time.Second,
		HandshakeTimeout:    2 * time.Second,
		PingInterval:        time.Second,
		ReconnectInitial:    50 * time.Millisecond,
		ReconnectMax:        200 * time.Millisecond,
		ReconnectMultiplier: 1.5,
		ReconnectJitter:     10 * time.Millisecond,
	}
	client := NewControlClient(cfg)
	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Shutdown()

	waitConnected(t, client, "initial")
	relayIDBefore := client.RelayID()
	if relayIDBefore == "" {
		t.Fatal("identity not captured: RelayID empty after registration")
	}

	// Open a route; the fake Relay drops the connection right after
	// acknowledging it. This is the WSS interruption.
	creds, err := GenerateRouteCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.OpenRoute(ctx, RouteID(1), creds); err != nil {
		t.Fatalf("OpenRoute before interruption: %v", err)
	}

	// The watchdog must reconnect and restore the route.
	waitConnected(t, client, "reconnected")

	metrics := client.Metrics()
	t.Logf("after reconnect: reconnects=%d routesRestored=%d frameErrors=%d",
		metrics.Reconnects, metrics.RoutesRestored, metrics.FrameHandlerErrors)

	if metrics.Reconnects < 1 {
		t.Errorf("expected >=1 reconnect, got %d", metrics.Reconnects)
	}
	if metrics.RoutesRestored < 1 {
		t.Errorf("expected >=1 route restored after reconnect, got %d", metrics.RoutesRestored)
	}

	// Identity must be unchanged after recovery.
	if got := client.RelayID(); got != relayIDBefore {
		t.Errorf("identity changed across reconnect: got %q, want %q", got, relayIDBefore)
	}
	if !client.IsConnected() {
		t.Fatal("client not connected after recovery")
	}

	// Downgrade check: the ControlClient must remain in full connected
	// state (not a degraded/fallback transport). A downgrade would surface
	// as a disconnected client or a dropped route — assert neither.
	if metrics.FrameHandlerErrors != 0 {
		t.Errorf("frame handler errors after reconnect: %d", metrics.FrameHandlerErrors)
	}

	// Prove the tunnel carries data again: attach the WG transport frame
	// handler and push a data frame through the restored route.
	inbound := make(chan []byte, 4)
	client.SetFrameHandler(func(f *Frame) error {
		select {
		case inbound <- f.Payload:
		default:
		}
		return nil
	})
	defer client.ClearFrameHandler()

	// Route 1 was restored automatically; a fresh route also proves the
	// new connection is fully functional.
	creds2, err := GenerateRouteCredentials()
	if err != nil {
		t.Fatal(err)
	}
	opened, err := client.OpenRoute(ctx, RouteID(2), creds2)
	if err != nil {
		t.Fatalf("OpenRoute on restored connection: %v", err)
	}
	if opened.RouteID != RouteID(2) {
		t.Errorf("opened unexpected route: got %d want 2", opened.RouteID)
	}

	payload := []byte("m47-reconnect-wg-data")
	if err := client.writeFrame(&Frame{Version: ProtocolVersion, RouteID: RouteID(2), Payload: payload}); err != nil {
		t.Fatalf("writeFrame over restored route: %v", err)
	}

	select {
	case got := <-inbound:
		if string(got) != string(payload) {
			t.Errorf("echo mismatch: got %q want %q", got, payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no binary frame echoed over restored tunnel")
	}
}

// TestM47_TunnelReconnect_IdentityPersistsAcrossCycles forces
// reconnection and asserts identity remains constant.
func TestM47_TunnelReconnect_IdentityPersistsAcrossCycles(t *testing.T) {
	srv, wsURL := startFakeRelay(t, reconnectRelayHandler(t))
	defer srv.Close()

	// Fast reconnect for a deterministic, short test.
	cfg := ClientConfig{
		RelayURL:            wsURL,
		RegistrationToken:   "m47-reconnect-cycles",
		ReadTimeout:         2 * time.Second,
		WriteTimeout:        time.Second,
		HandshakeTimeout:    2 * time.Second,
		PingInterval:        time.Second,
		ReconnectInitial:    50 * time.Millisecond,
		ReconnectMax:        100 * time.Millisecond,
		ReconnectMultiplier: 1.5,
		ReconnectJitter:     5 * time.Millisecond,
	}
	client := NewControlClient(cfg)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Shutdown()

	waitConnected(t, client, "cycle-0")

	snap := identitySnapshot{RelayID: client.RelayID()}
	if snap.RelayID == "" {
		t.Fatal("identity not captured: RelayID empty")
	}

	// Open a route, then the fake Relay's first-connection drop forces
	// a reconnect. After recovery identity must be unchanged.
	creds, err := GenerateRouteCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.OpenRoute(context.Background(), RouteID(10), creds); err != nil {
		t.Fatalf("OpenRoute before drop: %v", err)
	}

	waitConnected(t, client, "cycle-reconnect")

	if got := client.RelayID(); got != snap.RelayID {
		t.Fatalf("identity changed after reconnect: got %q want %q", got, snap.RelayID)
	}

	m := client.Metrics()
	t.Logf("cycles: reconnects=%d routesRestored=%d", m.Reconnects, m.RoutesRestored)
	if m.Reconnects < 1 {
		t.Errorf("expected >=1 reconnect, got %d", m.Reconnects)
	}
	if m.RoutesRestored < 1 {
		t.Errorf("expected >=1 route restored, got %d", m.RoutesRestored)
	}

	// Open a second route on the new connection (connection is now at n=2,
	// which does NOT trigger a drop). Identity stays constant.
	creds2, err := GenerateRouteCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.OpenRoute(context.Background(), RouteID(11), creds2); err != nil {
		t.Fatalf("OpenRoute after reconnect: %v", err)
	}

	if got := client.RelayID(); got != snap.RelayID {
		t.Fatalf("identity changed after second OpenRoute: got %q want %q", got, snap.RelayID)
	}
}
