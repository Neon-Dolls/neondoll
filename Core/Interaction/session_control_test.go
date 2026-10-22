package interaction_test

import (
	"context"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/SessionControl"
)

func controlRequest(kind sessioncontrol.MessageType, id, sessionID string) sessioncontrol.Request {
	return sessioncontrol.Request{Version: sessioncontrol.Version, Type: kind, ID: id, Payload: sessioncontrol.RequestPayload{SessionID: sessionID}}
}

func TestBodyIngressSessionControlLifecycleAndAuthorization(t *testing.T) {
	ctx := context.Background()
	sessions := openRouterSessions(t)
	handler := &ingressHandler{}
	ingress := newBodyIngress(t, sessions, handler)

	created, selected := ingress.HandleSessionControl(ctx, "body-one", "", controlRequest(sessioncontrol.TypeCreate, "create-1", ""))
	if created.Payload.Status != sessioncontrol.StatusOK || created.CorrelationID != "create-1" || created.Payload.Session == nil || selected != "" {
		t.Fatalf("create response = %+v, selected %q", created, selected)
	}
	sessionID := created.Payload.Session.ID

	listed, _ := ingress.HandleSessionControl(ctx, "body-one", "", controlRequest(sessioncontrol.TypeList, "list-1", ""))
	if listed.Payload.Status != sessioncontrol.StatusOK || len(listed.Payload.Sessions) != 1 || listed.Payload.Sessions[0].ID != sessionID {
		t.Fatalf("own list = %+v", listed)
	}
	foreignList, _ := ingress.HandleSessionControl(ctx, "body-two", "", controlRequest(sessioncontrol.TypeList, "list-2", ""))
	if foreignList.Payload.Status != sessioncontrol.StatusOK || len(foreignList.Payload.Sessions) != 0 {
		t.Fatalf("foreign list leaked session = %+v", foreignList)
	}

	foreign, next := ingress.HandleSessionControl(ctx, "body-two", "prior", controlRequest(sessioncontrol.TypeSelect, "select-foreign", sessionID))
	if foreign.Payload.Status != sessioncontrol.StatusError || foreign.Payload.Error == nil || foreign.Payload.Error.Code != sessioncontrol.ErrorSessionUnavailable || next != "prior" {
		t.Fatalf("foreign select = %+v, next %q", foreign, next)
	}
	selectedResponse, next := ingress.HandleSessionControl(ctx, "body-one", "", controlRequest(sessioncontrol.TypeSelect, "select-1", sessionID))
	if selectedResponse.Payload.Status != sessioncontrol.StatusOK || next != sessionID {
		t.Fatalf("select = %+v, next %q", selectedResponse, next)
	}

	event := events.NewDollMessage("event-1", "doll-1", "selected")
	if _, err := ingress.HandleSelectedSessionEvent(ctx, "body-one", next, &event); err != nil {
		t.Fatalf("selected event: %v", err)
	}
	handler.mu.Lock()
	routed := handler.sessions[len(handler.sessions)-1]
	handler.mu.Unlock()
	if string(routed) != sessionID {
		t.Fatalf("event routed to %q, want %q", routed, sessionID)
	}

	closed, next := ingress.HandleSessionControl(ctx, "body-one", next, controlRequest(sessioncontrol.TypeClose, "close-1", sessionID))
	if closed.Payload.Status != sessioncontrol.StatusOK || closed.Payload.Session == nil || closed.Payload.Session.Status != string(session.StatusClosed) || next != "" {
		t.Fatalf("close = %+v, next %q", closed, next)
	}
	for _, bodyID := range []string{"body-one", "body-two"} {
		rejected, kept := ingress.HandleSessionControl(ctx, bodyID, "keep", controlRequest(sessioncontrol.TypeSelect, "select-closed", sessionID))
		if rejected.Payload.Status != sessioncontrol.StatusError || rejected.Payload.Error == nil || rejected.Payload.Error.Code != sessioncontrol.ErrorSessionUnavailable || kept != "keep" {
			t.Fatalf("closed select for %s = %+v, next %q", bodyID, rejected, kept)
		}
	}
}

func TestBodyIngressSessionControlVersionAndImplicitDefault(t *testing.T) {
	ctx := context.Background()
	sessions := openRouterSessions(t)
	handler := &ingressHandler{}
	ingress := newBodyIngress(t, sessions, handler)

	unsupported := controlRequest(sessioncontrol.TypeList, "list-v2", "")
	unsupported.Version = 2
	response, selected := ingress.HandleSessionControl(ctx, "body-one", "selected-before", unsupported)
	if response.Payload.Status != sessioncontrol.StatusError || response.Payload.Error == nil || response.Payload.Error.Code != sessioncontrol.ErrorUnsupportedVersion || selected != "selected-before" {
		t.Fatalf("unsupported response = %+v, selected %q", response, selected)
	}

	event := events.NewDollMessage("event-default", "doll-1", "implicit")
	if _, err := ingress.HandleSelectedSessionEvent(ctx, "body-one", "", &event); err != nil {
		t.Fatalf("implicit event: %v", err)
	}
	active, err := sessions.ListActive(ctx, &session.ActiveFilter{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-one"}})
	if err != nil || len(active) != 1 || active[0].ExternalConversationRef != "doll-link/default" {
		t.Fatalf("implicit sessions = %+v, %v", active, err)
	}
}
