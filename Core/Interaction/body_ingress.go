package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
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
	if strings.TrimSpace(bodyID) == "" {
		return nil, ErrUnauthenticatedBody
	}
	owner := session.Owner{Kind: session.OwnerBody, InstanceID: bodyID}
	resolved, err := i.resolveBodySession(ctx, owner)
	if err != nil {
		return nil, err
	}
	return i.router.RouteInteraction(ctx, RouteRequest{SessionID: resolved.ID, Owner: owner, Event: event})
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
