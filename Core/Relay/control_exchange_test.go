package relay

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
