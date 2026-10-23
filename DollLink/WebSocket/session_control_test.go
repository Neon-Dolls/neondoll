package ws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Authentication"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/SessionControl"
)

type selectionHandler struct {
	mu              sync.Mutex
	selected        []string
	overrides       []string
	controls        int
	rejectOverrides bool
}

func (h *selectionHandler) HandleSessionInteraction(_ context.Context, _, sessionID string, event *events.Event) (*events.Event, error) {
	h.mu.Lock()
	h.overrides = append(h.overrides, sessionID)
	reject := h.rejectOverrides
	h.mu.Unlock()
	if reject {
		return nil, errors.New("foreign, closed, or unknown session detail")
	}
	response := events.NewResponse(event.ID, event.DollID, "override")
	return &response, nil
}

type pendingBodyAuthenticator struct{}

type concurrentInteractionHandler struct {
	entered chan string
	release chan struct{}
}

type nilInteractionHandler struct{}

func (nilInteractionHandler) HandleSelectedSessionEvent(_ context.Context, _, _ string, event *events.Event) (*events.Event, error) {
	response := events.NewResponse(event.ID, event.DollID, "legacy")
	return &response, nil
}

func (nilInteractionHandler) HandleSessionInteraction(context.Context, string, string, *events.Event) (*events.Event, error) {
	return nil, nil
}

type cancelInteractionHandler struct {
	entered  chan struct{}
	canceled chan struct{}
}

func (h *cancelInteractionHandler) HandleSelectedSessionEvent(_ context.Context, _, _ string, event *events.Event) (*events.Event, error) {
	response := events.NewResponse(event.ID, event.DollID, "legacy")
	return &response, nil
}

func (h *cancelInteractionHandler) HandleSessionInteraction(ctx context.Context, _ string, _ string, _ *events.Event) (*events.Event, error) {
	close(h.entered)
	<-ctx.Done()
	close(h.canceled)
	return nil, ctx.Err()
}

func (h *concurrentInteractionHandler) HandleSelectedSessionEvent(_ context.Context, _, _ string, event *events.Event) (*events.Event, error) {
	response := events.NewResponse(event.ID, event.DollID, "legacy")
	return &response, nil
}

func (h *concurrentInteractionHandler) HandleSessionInteraction(ctx context.Context, _ string, sessionID string, event *events.Event) (*events.Event, error) {
	h.entered <- sessionID
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	response := events.NewResponse(event.ID, event.DollID, "done")
	return &response, nil
}

func (pendingBodyAuthenticator) BeginAuthentication(context.Context, string, authentication.Hello) (authentication.Challenge, error) {
	return authentication.Challenge{}, errors.New("unexpected authentication hello")
}

func (pendingBodyAuthenticator) ActivateAuthentication(context.Context, string, authentication.Proof, func(authentication.Accept, string) error) error {
	return errors.New("unexpected authentication proof")
}

func (pendingBodyAuthenticator) CancelAuthentication(string) {}

func (h *selectionHandler) HandleSelectedSessionEvent(_ context.Context, _, selected string, event *events.Event) (*events.Event, error) {
	h.mu.Lock()
	h.selected = append(h.selected, selected)
	h.mu.Unlock()
	response := events.NewResponse(event.ID, event.DollID, "selected")
	return &response, nil
}

func (h *selectionHandler) HandleSessionControl(_ context.Context, _ string, current string, request sessioncontrol.Request) (sessioncontrol.Response, string) {
	h.mu.Lock()
	h.controls++
	h.mu.Unlock()
	response := sessioncontrol.Response{Version: 1, Type: sessioncontrol.TypeResult, ID: "result-" + request.ID, CorrelationID: request.ID,
		Payload: sessioncontrol.ResponsePayload{Operation: request.Type, Status: sessioncontrol.StatusOK}}
	if request.Version != 1 {
		response.Payload.Status = sessioncontrol.StatusError
		response.Payload.Error = &sessioncontrol.Error{Code: sessioncontrol.ErrorUnsupportedVersion, Message: "unsupported"}
		return response, current
	}
	if request.Type == sessioncontrol.TypeSelect {
		current = request.Payload.SessionID
	}
	if request.Type == sessioncontrol.TypeClose && current == request.Payload.SessionID {
		current = ""
	}
	return response, current
}

func TestSessionControlDuplicateRequestIsNotReapplied(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	body := dialTestBody(t, url, "body-one")
	defer body.Close()
	request := sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeCreate, ID: "create-1"}
	first := sendControl(t, body, request)
	second := sendControl(t, body, request)
	if first.ID != second.ID || first.CorrelationID != second.CorrelationID {
		t.Fatalf("duplicate response changed: first %+v, second %+v", first, second)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.controls != 1 {
		t.Fatalf("handler calls = %d, want 1", handler.controls)
	}
}

func TestSessionControlDuplicateRequestIDRejectsDifferentContent(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	body := dialTestBody(t, url, "body-one")
	defer body.Close()

	first := sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeSelect, ID: "request-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-one"}}
	if response := sendControl(t, body, first); response.Payload.Status != sessioncontrol.StatusOK {
		t.Fatalf("first response = %+v", response)
	}
	conflicts := []sessioncontrol.Request{
		{Version: 1, Type: sessioncontrol.TypeClose, ID: "request-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-one"}},
		{Version: 1, Type: sessioncontrol.TypeSelect, ID: "request-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-two"}},
	}
	for _, request := range conflicts {
		response := sendControl(t, body, request)
		if response.CorrelationID != request.ID || response.Payload.Error == nil || response.Payload.Error.Code != sessioncontrol.ErrorRequestIDConflict {
			t.Fatalf("conflict response = %+v", response)
		}
	}
	sendEvent(t, body, "event-one")
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.controls != 1 || len(handler.selected) != 1 || handler.selected[0] != "session-one" {
		t.Fatalf("conflicting request changed state: controls=%d selected=%#v", handler.controls, handler.selected)
	}
}

func TestSessionControlMalformedRequestReturnsCorrelatedError(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	body := dialTestBody(t, url, "body-one")
	defer body.Close()

	malformed := []byte(`{"version":"not-an-integer","type":"session.select","id":"malformed-1","payload":[]}`)
	if err := body.WriteMessage(websocket.TextMessage, malformed); err != nil {
		t.Fatal(err)
	}
	var response sessioncontrol.Response
	if err := body.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.CorrelationID != "malformed-1" || response.Payload.Operation != sessioncontrol.TypeSelect || response.Payload.Error == nil || response.Payload.Error.Code != sessioncontrol.ErrorInvalidRequest {
		t.Fatalf("malformed response = %+v", response)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.controls != 0 {
		t.Fatalf("malformed request reached handler %d times", handler.controls)
	}
}

func TestSessionControlUnavailableHandlerReturnsCorrelatedError(t *testing.T) {
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, echoEventHandler{})
	body := dialTestBody(t, url, "body-one")
	defer body.Close()
	response := sendControl(t, body, sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeList, ID: "list-1"})
	if response.CorrelationID != "list-1" || response.Payload.Error == nil || response.Payload.Error.Code != sessioncontrol.ErrorHandlerUnavailable {
		t.Fatalf("unavailable response = %+v", response)
	}
}

func bodyHeaderAuthenticator(request *http.Request) (string, error) {
	bodyID := request.Header.Get(testAuthHeader)
	if bodyID == "" {
		return "", errors.New("missing Body identity")
	}
	return bodyID, nil
}

func sendControl(t *testing.T, body interface {
	WriteJSON(any) error
	ReadJSON(any) error
}, request sessioncontrol.Request) sessioncontrol.Response {
	t.Helper()
	if err := body.WriteJSON(request); err != nil {
		t.Fatal(err)
	}
	var response sessioncontrol.Response
	if err := body.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func sendEvent(t *testing.T, body interface {
	WriteJSON(any) error
	ReadJSON(any) error
}, id string) {
	t.Helper()
	event := events.NewDollMessage(id, "doll-one", "hello")
	if err := body.WriteJSON(event); err != nil {
		t.Fatal(err)
	}
	var response events.Event
	if err := body.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
}

func sendSessionInteraction(t *testing.T, body interface {
	WriteJSON(any) error
	ReadJSON(any) error
}, sessionID, eventID string) events.Event {
	t.Helper()
	event := events.NewDollMessage(eventID, "doll-one", "hello")
	message := sessioncontrol.Interaction{Version: sessioncontrol.Version, Type: sessioncontrol.TypeInteraction, SessionID: sessionID, Event: event}
	if err := body.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
	var response events.Event
	if err := body.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func writeSessionInteraction(t *testing.T, body interface{ WriteJSON(any) error }, sessionID, eventID string) {
	t.Helper()
	event := events.NewDollMessage(eventID, "doll-one", "hello")
	message := sessioncontrol.Interaction{Version: sessioncontrol.Version, Type: sessioncontrol.TypeInteraction, SessionID: sessionID, Event: event}
	if err := body.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
}

func TestPerInteractionOverrideDoesNotChangeConnectionSelection(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	body := dialTestBody(t, url, "body-one")
	defer body.Close()
	sendControl(t, body, sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeSelect, ID: "select-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-default"}})
	response := sendSessionInteraction(t, body, "session-override", "event-override")
	if response.CorrelationID != "event-override" {
		t.Fatalf("override correlation = %q", response.CorrelationID)
	}
	sendEvent(t, body, "event-default")
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.overrides) != 1 || handler.overrides[0] != "session-override" || len(handler.selected) != 1 || handler.selected[0] != "session-default" {
		t.Fatalf("overrides=%#v selected=%#v", handler.overrides, handler.selected)
	}
}

func TestPerInteractionOverrideAfterReconnectLeavesImplicitDefault(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	body := dialTestBody(t, url, "body-one")
	sendControl(t, body, sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeSelect, ID: "select-1", Payload: sessioncontrol.RequestPayload{SessionID: "old-default"}})
	_ = body.Close()

	reconnected := dialTestBody(t, url, "body-one")
	defer reconnected.Close()
	sendSessionInteraction(t, reconnected, "session-override", "event-override")
	sendEvent(t, reconnected, "event-default")
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.overrides) != 1 || handler.overrides[0] != "session-override" || len(handler.selected) != 1 || handler.selected[0] != "" {
		t.Fatalf("reconnect overrides=%#v selected=%#v", handler.overrides, handler.selected)
	}
}

func TestPerInteractionOverridesRunConcurrently(t *testing.T) {
	handler := &concurrentInteractionHandler{entered: make(chan string, 2), release: make(chan struct{})}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	body := dialTestBody(t, url, "body-one")
	defer body.Close()
	for index, sessionID := range []string{"session-one", "session-two"} {
		event := events.NewDollMessage(fmt.Sprintf("event-%d", index), "doll-one", "hello")
		message := sessioncontrol.Interaction{Version: 1, Type: sessioncontrol.TypeInteraction, SessionID: sessionID, Event: event}
		if err := body.WriteJSON(message); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	seen := make(map[string]bool)
	for len(seen) < 2 {
		select {
		case sessionID := <-handler.entered:
			seen[sessionID] = true
		case <-ctx.Done():
			t.Fatal("second session interaction did not enter concurrently")
		}
	}
	close(handler.release)
	for range 2 {
		var response events.Event
		if err := body.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPerInteractionConcurrencyLimitAndRecovery(t *testing.T) {
	handler := &concurrentInteractionHandler{entered: make(chan string, 3), release: make(chan struct{})}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator, MaxConcurrentInteractions: 2}, handler)
	body := dialTestBody(t, url, "body-one")
	defer body.Close()
	writeSessionInteraction(t, body, "session-one", "event-one")
	writeSessionInteraction(t, body, "session-two", "event-two")
	for range 2 {
		select {
		case <-handler.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("interaction did not occupy capacity")
		}
	}
	writeSessionInteraction(t, body, "session-three", "event-overload")
	var overloaded events.Event
	if err := body.ReadJSON(&overloaded); err != nil {
		t.Fatal(err)
	}
	payload, ok := overloaded.Payload.(map[string]any)
	if overloaded.CorrelationID != "event-overload" || !ok || payload["event"] != "error" || payload["data"] != "session interaction unavailable" {
		t.Fatalf("overload response = %+v", overloaded)
	}

	close(handler.release)
	for range 2 {
		var response events.Event
		if err := body.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
	}
	response := sendSessionInteraction(t, body, "session-four", "event-recovered")
	if response.CorrelationID != "event-recovered" {
		t.Fatalf("recovered response = %+v", response)
	}
}

func TestPerInteractionNilResponseReturnsCorrelatedError(t *testing.T) {
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, nilInteractionHandler{})
	body := dialTestBody(t, url, "body-one")
	defer body.Close()
	response := sendSessionInteraction(t, body, "session-one", "event-nil")
	payload, ok := response.Payload.(map[string]any)
	if response.CorrelationID != "event-nil" || !ok || payload["event"] != "error" || payload["data"] != "session interaction failed" {
		t.Fatalf("nil response result = %+v", response)
	}
}

func TestPerInteractionDisconnectCancelsAndJoinsWorker(t *testing.T) {
	handler := &cancelInteractionHandler{entered: make(chan struct{}), canceled: make(chan struct{})}
	server, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator, MaxConcurrentInteractions: 1}, handler)
	body := dialTestBody(t, url, "body-one")
	writeSessionInteraction(t, body, "session-one", "event-one")
	select {
	case <-handler.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("interaction did not start")
	}
	server.mu.Lock()
	var disconnected <-chan struct{}
	for _, connection := range server.conns {
		disconnected = connection.done
		break
	}
	server.mu.Unlock()
	if disconnected == nil {
		t.Fatal("connection was not registered")
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handler.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not cancel interaction")
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnect did not join interaction worker")
	}
}

func TestRejectedPerInteractionOverrideDoesNotFallBackOrChangeDefault(t *testing.T) {
	handler := &selectionHandler{rejectOverrides: true}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	body := dialTestBody(t, url, "body-one")
	defer body.Close()
	sendControl(t, body, sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeSelect, ID: "select-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-default"}})
	response := sendSessionInteraction(t, body, "forged-session", "event-forged")
	payload, ok := response.Payload.(map[string]any)
	if !ok || payload["event"] != "error" || payload["data"] != "session is not available" {
		t.Fatalf("rejected response = %+v", response)
	}
	sendEvent(t, body, "event-default")
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.overrides) != 1 || len(handler.selected) != 1 || handler.selected[0] != "session-default" {
		t.Fatalf("rejected override fell back or changed default: overrides=%#v selected=%#v", handler.overrides, handler.selected)
	}
}

func TestPerInteractionVersionAndAuthenticationRejection(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, handler)
	unauthenticated := dialTestBody(t, url, "invalid-token")
	response := sendSessionInteraction(t, unauthenticated, "session-one", "event-unauthenticated")
	if response.CorrelationID != "event-unauthenticated" {
		t.Fatalf("unauthenticated correlation = %q", response.CorrelationID)
	}
	_ = unauthenticated.Close()

	authenticated := dialTestBody(t, url, "fixture-token")
	defer authenticated.Close()
	event := events.NewDollMessage("event-version", "doll-one", "hello")
	message := sessioncontrol.Interaction{Version: 2, Type: sessioncontrol.TypeInteraction, SessionID: "session-one", Event: event}
	if err := authenticated.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
	if err := authenticated.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.CorrelationID != "event-version" {
		t.Fatalf("version correlation = %q", response.CorrelationID)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.overrides) != 0 {
		t.Fatalf("rejected interactions reached handler: %#v", handler.overrides)
	}
}

func TestSessionSelectionIsConnectionLocalAndClearedOnReconnect(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	bodyOne := dialTestBody(t, url, "body-one")
	bodyTwo := dialTestBody(t, url, "body-two")

	selected := sendControl(t, bodyOne, sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeSelect, ID: "select-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-one"}})
	if selected.CorrelationID != "select-1" || selected.Payload.Status != sessioncontrol.StatusOK {
		t.Fatalf("select response = %+v", selected)
	}
	sendEvent(t, bodyOne, "event-one")
	sendEvent(t, bodyTwo, "event-two")
	_ = bodyOne.Close()
	_ = bodyTwo.Close()

	reconnected := dialTestBody(t, url, "body-one")
	defer reconnected.Close()
	sendEvent(t, reconnected, "event-reconnected")

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.selected) != 3 || handler.selected[0] != "session-one" || handler.selected[1] != "" || handler.selected[2] != "" {
		t.Fatalf("observed selections = %#v", handler.selected)
	}
}

func TestSessionControlUnsupportedVersionPreservesSelection(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: bodyHeaderAuthenticator}, handler)
	body := dialTestBody(t, url, "body-one")
	defer body.Close()
	sendControl(t, body, sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeSelect, ID: "select-1", Payload: sessioncontrol.RequestPayload{SessionID: "session-one"}})
	response := sendControl(t, body, sessioncontrol.Request{Version: 2, Type: sessioncontrol.TypeList, ID: "list-v2"})
	if response.Payload.Error == nil || response.Payload.Error.Code != sessioncontrol.ErrorUnsupportedVersion {
		t.Fatalf("unsupported response = %+v", response)
	}
	sendEvent(t, body, "event-one")
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.selected) != 1 || handler.selected[0] != "session-one" {
		t.Fatalf("selection after unsupported request = %#v", handler.selected)
	}
}

func TestUnauthenticatedConnectionCannotControlSessions(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{AuthenticateExecution: trustedTestAuthenticator}, handler)
	body := dialTestBody(t, url, "invalid-token")
	defer body.Close()
	request := sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeCreate, ID: "create-1"}
	response := sendControl(t, body, request)
	if response.CorrelationID != "create-1" || response.Payload.Error == nil || response.Payload.Error.Code != sessioncontrol.ErrorUnauthenticated {
		t.Fatalf("unauthenticated response = %+v", response)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.controls != 0 || len(handler.selected) != 0 {
		t.Fatalf("unauthenticated calls: controls=%d selected=%#v", handler.controls, handler.selected)
	}
}

func TestPreAuthenticationSessionControlReturnsCorrelatedError(t *testing.T) {
	handler := &selectionHandler{}
	_, url := startWSTestServer(t, Config{BodyAuthenticator: pendingBodyAuthenticator{}}, handler)
	body := dialTestBody(t, url, "")
	defer body.Close()
	response := sendControl(t, body, sessioncontrol.Request{Version: 1, Type: sessioncontrol.TypeList, ID: "pre-auth-1"})
	if response.CorrelationID != "pre-auth-1" || response.Payload.Error == nil || response.Payload.Error.Code != sessioncontrol.ErrorUnauthenticated {
		t.Fatalf("pre-authentication response = %+v", response)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.controls != 0 {
		t.Fatalf("pre-authentication request reached handler %d times", handler.controls)
	}
}
