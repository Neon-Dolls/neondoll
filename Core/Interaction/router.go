package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
)

var (
	ErrInvalidRoute        = errors.New("interaction: invalid session route")
	ErrUnknownSession      = errors.New("interaction: unknown session")
	ErrUnauthorizedSession = errors.New("interaction: unauthorized session")
	ErrClosedSession       = errors.New("interaction: closed session")
)

// RouteRequest is supplied by a trusted Core ingress after it has established
// the owning Body or Gateway instance identity. Event fields are never used as
// proof of session ownership.
type RouteRequest struct {
	SessionID session.ID
	Owner     session.Owner
	Event     *events.Event
	// onAuthorized is available only inside the Interaction package. Trusted
	// ingress uses it for state that may be recorded only after Router verifies
	// Session ownership and active lifecycle.
	onAuthorized func(context.Context, *session.InteractionSession) error
}

// SessionHandler receives an interaction only after Router has resolved and
// authorized its active Core session.
type SessionHandler interface {
	HandleInteraction(context.Context, *session.InteractionSession, *events.Event) (*events.Event, error)
}

// SessionRouter is the transport-independent interaction routing boundary.
// It contains no session state or model context of its own.
type SessionRouter interface {
	RouteInteraction(context.Context, RouteRequest) (*events.Event, error)
}

// SessionAuthorizer exposes the Router's existing ownership and lifecycle
// checks to trusted structured-event ingress without fabricating a legacy
// text interaction.
type SessionAuthorizer interface {
	AuthorizeSession(context.Context, session.ID, session.Owner, func(context.Context, *session.InteractionSession) error) (*session.InteractionSession, error)
}

type Router struct {
	sessions session.Service
	handler  SessionHandler
}

var _ SessionRouter = (*Router)(nil)

func NewRouter(sessions session.Service, handler SessionHandler) (*Router, error) {
	if sessions == nil || handler == nil {
		return nil, ErrInvalidRoute
	}
	return &Router{sessions: sessions, handler: handler}, nil
}

func (r *Router) RouteInteraction(ctx context.Context, request RouteRequest) (*events.Event, error) {
	if strings.TrimSpace(string(request.SessionID)) == "" || !validRouteOwner(request.Owner) || request.Event == nil {
		return nil, ErrInvalidRoute
	}
	resolved, err := r.AuthorizeSession(ctx, request.SessionID, request.Owner, request.onAuthorized)
	if err != nil {
		return nil, err
	}
	return r.handler.HandleInteraction(ctx, resolved, request.Event)
}

func (r *Router) AuthorizeSession(ctx context.Context, sessionID session.ID, owner session.Owner, onAuthorized func(context.Context, *session.InteractionSession) error) (*session.InteractionSession, error) {
	if strings.TrimSpace(string(sessionID)) == "" || !validRouteOwner(owner) {
		return nil, ErrInvalidRoute
	}
	resolved, err := r.sessions.Lookup(ctx, sessionID)
	if errors.Is(err, session.ErrNotFound) {
		return nil, ErrUnknownSession
	}
	if err != nil {
		return nil, fmt.Errorf("interaction: lookup session: %w", err)
	}
	if resolved.Owner != owner {
		return nil, ErrUnauthorizedSession
	}
	if resolved.Status != session.StatusActive {
		return nil, ErrClosedSession
	}

	// Resume atomically rechecks the active lifecycle transition and updates
	// session activity before dispatch. If a concurrent close wins that
	// transition, this route is rejected rather than dispatched.
	resolved, err = r.sessions.Resume(ctx, session.ResumeRequest{SessionID: sessionID})
	if errors.Is(err, session.ErrNotFound) {
		return nil, ErrUnknownSession
	}
	if errors.Is(err, session.ErrInvalidTransition) {
		return nil, ErrClosedSession
	}
	if err != nil {
		return nil, fmt.Errorf("interaction: resume session: %w", err)
	}
	if onAuthorized != nil {
		if err := onAuthorized(ctx, resolved); err != nil {
			return nil, fmt.Errorf("interaction: authorized route hook: %w", err)
		}
	}
	return resolved, nil
}

func validRouteOwner(owner session.Owner) bool {
	return (owner.Kind == session.OwnerBody || owner.Kind == session.OwnerGateway) && strings.TrimSpace(owner.InstanceID) != ""
}
