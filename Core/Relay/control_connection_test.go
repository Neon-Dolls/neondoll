package relay

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestControlClient_ConnectAndRegister(t *testing.T) {
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

	if !client.IsConnected() {
		t.Fatal("IsConnected() = false; want true")
	}

	if id := client.RelayID(); id != "test-relay-1" {
		t.Errorf("RelayID() = %q; want %q", id, "test-relay-1")
	}
}

func TestControlClient_RegisterAuthFailed(t *testing.T) {
	srv, url := startFakeRelay(t, fakeRelayHandler(t, "expected"))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.RelayURL = url
	cfg.RegistrationToken = "wrong"
	cfg.HandshakeTimeout = 500 * time.Millisecond
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 30 * time.Second

	client := NewControlClient(cfg)
	err := client.Start(context.Background())
	if err == nil {
		client.Shutdown()
		t.Fatal("Start() = nil; expected auth failure")
	}
	if !strings.Contains(err.Error(), "registration rejected") &&
		!strings.Contains(err.Error(), "invalid token") {
		t.Errorf("Start() error = %v; want registration failure", err)
	}
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
