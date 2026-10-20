package ws

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Actions"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
)

const testAuthHeader = "X-Neondoll-Test-Credential"

type echoEventHandler struct{}

func (echoEventHandler) HandleEvent(_ context.Context, event *events.Event) (*events.Event, error) {
	response := events.NewResponse(event.ID, event.DollID, "echo")
	return &response, nil
}

func trustedTestAuthenticator(r *http.Request) (string, error) {
	if r.Header.Get(testAuthHeader) != "fixture-token" {
		return "", errors.New("test credential missing")
	}
	return "body-1", nil
}

func startWSTestServer(t *testing.T, cfg Config, handler Handler) (*Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := New(cfg, &testLogger{t}, handler)
	server.SetListener(listener)
	done := make(chan error, 1)
	go func() { done <- server.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("Start: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("server did not stop")
		}
	})
	return server, "ws://" + listener.Addr().String() + "/ws"
}

func dialTestBody(t *testing.T, url, credential string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	if credential != "" {
		header.Set(testAuthHeader, credential)
	}
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func waitForConnection(t *testing.T, server *Server, authenticated bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		connected := len(server.conns) > 0
		if authenticated {
			connected = server.execution["body-1"] != nil
		}
		server.mu.Unlock()
		if connected {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("WebSocket connection was not registered")
}

func TestExecutionRoundTripOverRealWebSocket(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	body := dialTestBody(t, url, "fixture-token")
	waitForConnection(t, server, true)

	bodyDone := make(chan error, 1)
	go func() {
		var request execution.Request
		if err := body.ReadJSON(&request); err != nil {
			bodyDone <- err
			return
		}
		result := execution.Result{
			Type:          execution.TypeResult,
			ID:            "result-1",
			Timestamp:     time.Now().UTC(),
			DollID:        request.DollID,
			BodyID:        request.BodyID,
			CorrelationID: request.ID,
			Payload: execution.ResultPayload{
				ExecutionID: request.Payload.ExecutionID,
				Status:      execution.StatusSuccess,
				Result:      map[string]any{"state": "on"},
			},
		}
		bodyDone <- body.WriteJSON(result)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := server.Execute(ctx, "body-1", "doll-1", execution.RequestPayload{
		ExecutionID: "execution-1",
		Capability:  "lights",
		Operation:   "set",
		Arguments:   map[string]any{"state": "on"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload.ExecutionID != "execution-1" || result.Payload.Status != execution.StatusSuccess {
		t.Fatalf("unexpected result: %#v", result)
	}
	if err := <-bodyDone; err != nil {
		t.Fatal(err)
	}
}

func TestExecutionDisconnectReleasesPendingRequest(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, nil)
	body := dialTestBody(t, url, "fixture-token")
	waitForConnection(t, server, true)
	requestRead := make(chan error, 1)
	go func() {
		var request execution.Request
		requestRead <- body.ReadJSON(&request)
	}()

	done := make(chan error, 1)
	go func() {
		_, err := server.Execute(context.Background(), "body-1", "", execution.RequestPayload{
			ExecutionID: "disconnect", Capability: "lights", Operation: "set", Arguments: map[string]any{},
		})
		done <- err
	}()
	if err := <-requestRead; err != nil {
		t.Fatal(err)
	}
	body.Close()
	select {
	case err := <-done:
		if !errors.Is(err, execution.ErrDisconnected) {
			t.Fatalf("Execute error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending request was not released on disconnect")
	}
}

func TestExecutionAuthenticatedBodyCannotCompleteAnotherSessionRequest(t *testing.T) {
	authenticator := func(request *http.Request) (string, error) {
		switch request.Header.Get(testAuthHeader) {
		case "token-a":
			return "body-a", nil
		case "token-b":
			return "body-b", nil
		default:
			return "", errors.New("test credential missing")
		}
	}
	server, url := startWSTestServer(t, Config{AuthenticateExecution: authenticator}, nil)
	bodyA := dialTestBody(t, url, "token-a")
	bodyB := dialTestBody(t, url, "token-b")

	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		ready := server.execution["body-a"] != nil && server.execution["body-b"] != nil
		server.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authenticated Body sessions were not registered")
		}
		time.Sleep(time.Millisecond)
	}

	type outcome struct {
		result execution.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := server.Execute(context.Background(), "body-b", "doll-1", execution.RequestPayload{
			ExecutionID: "shared-execution-id", Capability: "test", Operation: "run", Arguments: map[string]any{},
		})
		done <- outcome{result: result, err: err}
	}()

	var request execution.Request
	if err := bodyB.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	forged := execution.Result{
		Type: execution.TypeResult, ID: "forged", Timestamp: time.Now().UTC(),
		DollID: request.DollID, BodyID: "body-b", CorrelationID: request.ID,
		Payload: execution.ResultPayload{
			ExecutionID: request.Payload.ExecutionID, Status: execution.StatusSuccess,
			Result: map[string]any{"body": "body-a"},
		},
	}
	if err := bodyA.WriteJSON(forged); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-done:
		t.Fatalf("Body A completed Body B request: %#v, %v", outcome.result, outcome.err)
	case <-time.After(20 * time.Millisecond):
	}

	actual := forged
	actual.ID = "actual"
	actual.Payload.Result = map[string]any{"body": "body-b"}
	if err := bodyB.WriteJSON(actual); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.Payload.Result["body"] != "body-b" {
			t.Fatalf("Body B result = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated Body B result did not complete its request")
	}
}

func TestUnauthenticatedConnectionCannotExecute(t *testing.T) {
	server, url := startWSTestServer(t, Config{}, nil)
	_ = dialTestBody(t, url, "")
	waitForConnection(t, server, false)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := server.Execute(ctx, "body-1", "", execution.RequestPayload{
		ExecutionID: "blocked", Capability: "lights", Operation: "set", Arguments: map[string]any{},
	})
	if !errors.Is(err, ErrNoAuthenticatedSession) {
		t.Fatalf("Execute error = %v, want ErrNoAuthenticatedSession", err)
	}
}

func TestAuthenticatedConnectionPreservesEventsAndActions(t *testing.T) {
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, echoEventHandler{})
	body := dialTestBody(t, url, "fixture-token")
	waitForConnection(t, server, true)

	event := events.NewDollMessage("event-1", "doll-1", "hello")
	if err := body.WriteJSON(event); err != nil {
		t.Fatal(err)
	}
	var response events.Event
	if err := body.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.CorrelationID != event.ID {
		t.Fatalf("event correlation = %q", response.CorrelationID)
	}

	if err := server.SendAction(context.Background(), actions.Action{
		Type: actions.TypeSendMessage, Payload: actions.SendMessagePayload{Text: "action"},
	}); err != nil {
		t.Fatal(err)
	}
	var action events.Event
	if err := body.ReadJSON(&action); err != nil {
		t.Fatal(err)
	}
	if action.Type != events.TypeMessage {
		t.Fatalf("action event type = %q", action.Type)
	}
}
