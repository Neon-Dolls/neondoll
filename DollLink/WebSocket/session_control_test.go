package ws

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Authentication"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/SessionControl"
)

type selectionHandler struct {
	mu       sync.Mutex
	selected []string
	controls int
}

type pendingBodyAuthenticator struct{}

func (pendingBodyAuthenticator) BeginAuthentication(context.Context, string, authentication.Hello) (authentication.Challenge, error) {
	return authentication.Challenge{}, errors.New("unexpected authentication hello")
}

func (pendingBodyAuthenticator) ActivateAuthentication(context.Context, string, authentication.Proof, func(authentication.Accept, string) error) error {
	return errors.New("unexpected authentication proof")
}

func (pendingBodyAuthenticator) CancelAuthentication(string) {}

func (h *selectionHandler) HandleEvent(_ context.Context, event *events.Event) (*events.Event, error) {
	response := events.NewResponse(event.ID, event.DollID, "legacy")
	return &response, nil
}

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
