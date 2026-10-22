package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollLink/SessionControl"
	"github.com/google/uuid"
)

const BodyConversationReference = "doll-link/default"

var ErrUnauthenticatedBody = errors.New("interaction: authenticated Body required")

type EventHandler interface {
	HandleEvent(context.Context, *events.Event) (*events.Event, error)
}

// BodyIngress adapts authenticated Doll Link events to the Core session
// router. Each enrolled Body has one implicit Doll Link conversation because
// the existing event contract carries no Core session selector.
type BodyIngress struct {
	sessions session.Service
	router   SessionRouter
	legacy   EventHandler
}

func NewBodyIngress(sessions session.Service, router SessionRouter, legacy EventHandler) (*BodyIngress, error) {
	if sessions == nil || router == nil || legacy == nil {
		return nil, ErrInvalidRoute
	}
	return &BodyIngress{sessions: sessions, router: router, legacy: legacy}, nil
}

// HandleEvent preserves the legacy Core 4 event path for transports that do
// not expose authenticated Body identity.
func (i *BodyIngress) HandleEvent(ctx context.Context, event *events.Event) (*events.Event, error) {
	return i.legacy.HandleEvent(ctx, event)
}

// HandleAuthenticatedEvent derives ownership exclusively from the BodyID
// bound to the connection by Doll Link authentication.
func (i *BodyIngress) HandleAuthenticatedEvent(ctx context.Context, bodyID string, event *events.Event) (*events.Event, error) {
	return i.HandleSelectedSessionEvent(ctx, bodyID, "", event)
}

// HandleSelectedSessionEvent routes through the connection's explicit
// selection, or preserves the implicit doll-link/default behavior when no
// selection exists.
func (i *BodyIngress) HandleSelectedSessionEvent(ctx context.Context, bodyID, selectedSessionID string, event *events.Event) (*events.Event, error) {
	if strings.TrimSpace(bodyID) == "" {
		return nil, ErrUnauthenticatedBody
	}
	owner := session.Owner{Kind: session.OwnerBody, InstanceID: bodyID}
	if selectedSessionID != "" {
		return i.router.RouteInteraction(ctx, RouteRequest{SessionID: session.ID(selectedSessionID), Owner: owner, Event: event})
	}
	resolved, err := i.resolveBodySession(ctx, owner)
	if err != nil {
		return nil, err
	}
	return i.router.RouteInteraction(ctx, RouteRequest{SessionID: resolved.ID, Owner: owner, Event: event})
}

// HandleSessionInteraction routes one event through an explicit session. The
// WebSocket transport retains ownership of its connection-wide selection, so
// this operation cannot modify it.
func (i *BodyIngress) HandleSessionInteraction(ctx context.Context, bodyID, sessionID string, event *events.Event) (*events.Event, error) {
	if strings.TrimSpace(bodyID) == "" {
		return nil, ErrUnauthenticatedBody
	}
	owner := session.Owner{Kind: session.OwnerBody, InstanceID: bodyID}
	return i.router.RouteInteraction(ctx, RouteRequest{SessionID: session.ID(sessionID), Owner: owner, Event: event})
}

// HandleSessionControl applies an authenticated Body's session lifecycle
// operation. selectedSessionID is transport-owned state and the returned value
// replaces it only after the response has been produced.
func (i *BodyIngress) HandleSessionControl(ctx context.Context, bodyID, selectedSessionID string, request sessioncontrol.Request) (sessioncontrol.Response, string) {
	response := sessioncontrol.Response{Version: sessioncontrol.Version, Type: sessioncontrol.TypeResult, ID: uuid.NewString(), CorrelationID: request.ID,
		Payload: sessioncontrol.ResponsePayload{Operation: request.Type, Status: sessioncontrol.StatusError}}
	fail := func(code sessioncontrol.ErrorCode, message string) (sessioncontrol.Response, string) {
		response.Payload.Error = &sessioncontrol.Error{Code: code, Message: message}
		return response, selectedSessionID
	}
	if strings.TrimSpace(bodyID) == "" {
		return fail(sessioncontrol.ErrorInvalidRequest, "authenticated Body required")
	}
	if err := request.Validate(); err != nil {
		if errors.Is(err, sessioncontrol.ErrUnsupportedVersion) {
			return fail(sessioncontrol.ErrorUnsupportedVersion, "unsupported session-control version")
		}
		return fail(sessioncontrol.ErrorInvalidRequest, "invalid session-control request")
	}
	owner := session.Owner{Kind: session.OwnerBody, InstanceID: bodyID}
	switch request.Type {
	case sessioncontrol.TypeCreate:
		created, err := i.sessions.Create(ctx, session.CreateRequest{Owner: owner, ExternalConversationRef: request.Payload.ExternalConversationRef})
		if err != nil {
			if errors.Is(err, session.ErrInvalidRequest) || errors.Is(err, session.ErrConversationInUse) {
				return fail(sessioncontrol.ErrorInvalidRequest, "session could not be created")
			}
			return fail(sessioncontrol.ErrorInternal, "session service unavailable")
		}
		response.Payload.Status = sessioncontrol.StatusOK
		response.Payload.Session = wireSession(created)
	case sessioncontrol.TypeList:
		active, err := i.sessions.ListActive(ctx, &session.ActiveFilter{Owner: owner})
		if err != nil {
			return fail(sessioncontrol.ErrorInternal, "sessions could not be listed")
		}
		response.Payload.Status = sessioncontrol.StatusOK
		response.Payload.Sessions = make([]sessioncontrol.Session, 0, len(active))
		for _, candidate := range active {
			response.Payload.Sessions = append(response.Payload.Sessions, *wireSession(candidate))
		}
	case sessioncontrol.TypeSelect:
		resolved, err := i.ownedActiveSession(ctx, owner, session.ID(request.Payload.SessionID))
		if err != nil {
			return fail(sessioncontrol.ErrorSessionUnavailable, "session is not available")
		}
		response.Payload.Status = sessioncontrol.StatusOK
		response.Payload.Session = wireSession(resolved)
		selectedSessionID = string(resolved.ID)
	case sessioncontrol.TypeClose:
		resolved, err := i.ownedActiveSession(ctx, owner, session.ID(request.Payload.SessionID))
		if err != nil {
			return fail(sessioncontrol.ErrorSessionUnavailable, "session is not available")
		}
		closed, err := i.sessions.Close(ctx, resolved.ID)
		if err != nil {
			return fail(sessioncontrol.ErrorSessionUnavailable, "session is not available")
		}
		response.Payload.Status = sessioncontrol.StatusOK
		response.Payload.Session = wireSession(closed)
		if selectedSessionID == string(resolved.ID) {
			selectedSessionID = ""
		}
	}
	return response, selectedSessionID
}

func (i *BodyIngress) ownedActiveSession(ctx context.Context, owner session.Owner, id session.ID) (*session.InteractionSession, error) {
	resolved, err := i.sessions.Lookup(ctx, id)
	if err != nil || resolved.Owner != owner || resolved.Status != session.StatusActive {
		return nil, session.ErrNotFound
	}
	return resolved, nil
}

func wireSession(value *session.InteractionSession) *sessioncontrol.Session {
	return &sessioncontrol.Session{ID: string(value.ID), ExternalConversationRef: value.ExternalConversationRef, Status: string(value.Status), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func (i *BodyIngress) resolveBodySession(ctx context.Context, owner session.Owner) (*session.InteractionSession, error) {
	request := session.ResumeRequest{Owner: owner, ExternalConversationRef: BodyConversationReference}
	resolved, err := i.sessions.Resume(ctx, request)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, session.ErrNotFound) {
		return nil, fmt.Errorf("interaction: resume Body session: %w", err)
	}
	created, err := i.sessions.Create(ctx, session.CreateRequest{Owner: owner, ExternalConversationRef: BodyConversationReference})
	if err == nil {
		return created, nil
	}
	if errors.Is(err, session.ErrConversationInUse) {
		// Concurrent first interactions may race to establish the single
		// implicit Body conversation. Resume the durable winner.
		resolved, resumeErr := i.sessions.Resume(ctx, request)
		if resumeErr == nil {
			return resolved, nil
		}
		return nil, fmt.Errorf("interaction: resume concurrent Body session: %w", resumeErr)
	}
	return nil, fmt.Errorf("interaction: create Body session: %w", err)
}
