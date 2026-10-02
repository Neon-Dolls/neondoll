package relay

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

// errClosedWS is used by test handlers to signal a forced close.
type errClosedWS struct{}

func (e *errClosedWS) Error() string { return "test: ws closed" }

// waitConnected blocks until the client reports connected, with a timeout.
// If the client is already connected, it first waits for a brief disconnect
// pulse (up to 500ms), then waits for reconnection. This avoids a race where
// waitConnected returns on stale connected=true before the readLoop has set
// it false after a WebSocket drop — causing the caller to proceed before
// the reconnect cycle even begins.
func waitConnected(t *testing.T, client *ControlClient, label string) {
	t.Helper()

	// If already connected, wait briefly for a disconnect pulse that may
	// be imminent (the WS drop hasn't propagated to connected=false yet).
	// This ensures the reconnect cycle establishes a fresh connection.
	if client.IsConnected() {
		pulseDeadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(pulseDeadline) {
			if !client.IsConnected() {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsConnected() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("[%s] client did not reconnect within 5s deadline", label)
}

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
