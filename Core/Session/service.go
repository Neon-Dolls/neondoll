package session

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Manager implements the public session Service over durable Core storage.
// It holds no model context or per-session mutable state in memory.
type Manager struct {
	store Store
	clock Clock
	mu    sync.RWMutex
}

var _ Service = (*Manager)(nil)
var _ ParticipantService = (*Manager)(nil)
var _ TrustedParticipantService = (*Manager)(nil)

func New(store Store) *Manager { return NewWithClock(store, realClock{}) }

// NewTurnID creates the identity for one external interaction turn.
func NewTurnID() TurnID { return TurnID(uuid.NewString()) }

func NewWithClock(store Store, clock Clock) *Manager {
	return &Manager{store: store, clock: clock}
}

func (m *Manager) Create(ctx context.Context, request CreateRequest) (*InteractionSession, error) {
	if !validOwner(request.Owner) {
		return nil, ErrInvalidRequest
	}
	now := m.clock.Now().UTC()
	created := &InteractionSession{
		ID:                      ID(uuid.NewString()),
		Owner:                   request.Owner,
		ExternalConversationRef: request.ExternalConversationRef,
		Status:                  StatusActive,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.CreateSession(ctx, created); err != nil {
		return nil, err
	}
	return clone(created), nil
}

func (m *Manager) Lookup(ctx context.Context, id ID) (*InteractionSession, error) {
	if strings.TrimSpace(string(id)) == "" {
		return nil, ErrInvalidRequest
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	found, err := m.store.LoadSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return clone(found), nil
}

func (m *Manager) Resume(ctx context.Context, request ResumeRequest) (*InteractionSession, error) {
	byID := strings.TrimSpace(string(request.SessionID)) != ""
	byConversation := validOwner(request.Owner) && request.ExternalConversationRef != ""
	if byID == byConversation {
		return nil, ErrInvalidRequest
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var (
		found *InteractionSession
		err   error
	)
	if byID {
		found, err = m.store.ResumeSession(ctx, request.SessionID, m.clock.Now().UTC())
	} else {
		found, err = m.store.LoadActiveSessionByConversation(ctx, request.Owner, request.ExternalConversationRef)
		if err == nil {
			found, err = m.store.ResumeSession(ctx, found.ID, m.clock.Now().UTC())
		}
	}
	if err != nil {
		return nil, err
	}
	return clone(found), nil
}

func (m *Manager) Close(ctx context.Context, id ID) (*InteractionSession, error) {
	if strings.TrimSpace(string(id)) == "" {
		return nil, ErrInvalidRequest
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	closed, err := m.store.CloseSession(ctx, id, m.clock.Now().UTC())
	if err != nil {
		return nil, err
	}
	return clone(closed), nil
}

func (m *Manager) ListActive(ctx context.Context, filter *ActiveFilter) ([]*InteractionSession, error) {
	var owner *Owner
	if filter != nil {
		if !validOwner(filter.Owner) {
			return nil, ErrInvalidRequest
		}
		copy := filter.Owner
		owner = &copy
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	sessions, err := m.store.ListActiveSessions(ctx, owner)
	if err != nil {
		return nil, err
	}
	result := make([]*InteractionSession, len(sessions))
	for i, active := range sessions {
		result[i] = clone(active)
	}
	return result, nil
}

func (m *Manager) BindParticipant(ctx context.Context, sessionID ID, principalID string) (*ParticipantBinding, error) {
	if strings.TrimSpace(string(sessionID)) == "" || strings.TrimSpace(principalID) == "" {
		return nil, ErrInvalidRequest
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.BindSessionParticipant(ctx, sessionID, strings.TrimSpace(principalID), m.clock.Now().UTC())
}

func (m *Manager) BindResolvedParticipant(ctx context.Context, sessionID ID, source ParticipantSource) (*ParticipantBinding, error) {
	if strings.TrimSpace(string(sessionID)) == "" || source.SessionID != sessionID ||
		strings.TrimSpace(source.GatewayInstanceID) == "" || strings.TrimSpace(source.Platform) == "" ||
		strings.TrimSpace(source.SubjectID) == "" || strings.TrimSpace(source.PrincipalID) == "" {
		return nil, ErrInvalidRequest
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.BindResolvedSessionParticipant(ctx, sessionID, source, m.clock.Now().UTC())
}

func (m *Manager) ReconcileParticipantSources(ctx context.Context, sources []ParticipantSource) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.ReconcileSessionParticipantSources(ctx, sources)
}

func (m *Manager) ListParticipants(ctx context.Context, sessionID ID) ([]*ParticipantBinding, error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return nil, ErrInvalidRequest
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.ListSessionParticipants(ctx, sessionID)
}

func (m *Manager) ListParticipantSessions(ctx context.Context, principalID string) ([]*ParticipantBinding, error) {
	if strings.TrimSpace(principalID) == "" {
		return nil, ErrInvalidRequest
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.ListPrincipalSessions(ctx, strings.TrimSpace(principalID))
}

func validOwner(owner Owner) bool {
	return (owner.Kind == OwnerBody || owner.Kind == OwnerGateway) && strings.TrimSpace(owner.InstanceID) != ""
}

func clone(input *InteractionSession) *InteractionSession {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}
