package ws

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

type authenticatedEventHandler struct {
	legacyCalls atomic.Int32
	bodyIDs     chan string
}

func (h *authenticatedEventHandler) HandleEvent(_ context.Context, event *events.Event) (*events.Event, error) {
	h.legacyCalls.Add(1)
	response := events.NewResponse(event.ID, event.DollID, "legacy")
	return &response, nil
}

func (h *authenticatedEventHandler) HandleAuthenticatedEvent(_ context.Context, bodyID string, event *events.Event) (*events.Event, error) {
	h.bodyIDs <- bodyID
	response := events.NewResponse(event.ID, event.DollID, "authenticated")
	return &response, nil
}

func TestEventIngressUsesConnectionAuthenticatedBodyIdentity(t *testing.T) {
	handler := &authenticatedEventHandler{bodyIDs: make(chan string, 1)}
	server, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, handler)
	body := dialTestBody(t, url, "fixture-token")
	defer body.Close()
	waitForConnection(t, server, true)

	event := events.NewDollMessage("event-one", "doll-one", "hello")
	event.Source = "body-forged"
	event.Payload = events.MessagePayload{Text: "hello", UserID: "body-forged"}
	if err := body.WriteJSON(event); err != nil {
		t.Fatal(err)
	}
	var response events.Event
	if err := body.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.CorrelationID != event.ID {
		t.Fatalf("response correlation = %q", response.CorrelationID)
	}
	if bodyID := <-handler.bodyIDs; bodyID != "body-1" {
		t.Fatalf("authenticated BodyID = %q, want body-1", bodyID)
	}
	if handler.legacyCalls.Load() != 0 {
		t.Fatalf("authenticated event used legacy handler %d times", handler.legacyCalls.Load())
	}
}

func TestAuthenticatedEventHandlerFailsClosedWithoutBodyIdentity(t *testing.T) {
	handler := &authenticatedEventHandler{bodyIDs: make(chan string, 1)}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, handler)
	body := dialTestBody(t, url, "invalid-token")
	defer body.Close()

	event := events.NewDollMessage("event-one", "doll-one", "hello")
	if err := body.WriteJSON(event); err != nil {
		t.Fatal(err)
	}
	var response events.Event
	if err := body.ReadJSON(&response); err != nil {
		if websocket.IsUnexpectedCloseError(err) {
			t.Fatalf("ReadJSON: %v", err)
		}
		t.Fatal(err)
	}
	payload, ok := response.Payload.(map[string]any)
	if !ok || payload["event"] != "error" || payload["data"] != "authenticated Body required" {
		t.Fatalf("unauthenticated response = %+v", response)
	}
	if handler.legacyCalls.Load() != 0 || len(handler.bodyIDs) != 0 {
		t.Fatal("unauthenticated event reached a handler")
	}
}
