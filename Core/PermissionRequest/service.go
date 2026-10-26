package permissionrequest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/google/uuid"
)

type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Manager struct {
	store Store
	clock Clock
}

var _ Service = (*Manager)(nil)

func New(store Store) *Manager { return NewWithClock(store, realClock{}) }

func NewWithClock(store Store, clock Clock) *Manager {
	return &Manager{store: store, clock: clock}
}

func (m *Manager) Create(ctx context.Context, input CreateRequest) (*Request, error) {
	now := m.clock.Now().UTC()
	if err := validateCreate(input, now); err != nil {
		return nil, err
	}
	value := &Request{
		ID: ID(uuid.NewString()), DollID: strings.TrimSpace(input.DollID), OwnerID: strings.TrimSpace(input.OwnerID),
		Origin: input.Origin, Action: strings.TrimSpace(input.Action), Question: strings.TrimSpace(input.Question),
		Capability: strings.TrimSpace(input.Capability), Operation: strings.TrimSpace(input.Operation),
		Arguments: append(json.RawMessage(nil), input.Arguments...), Status: StatusPending,
		CreatedAt: now, ExpiresAt: input.ExpiresAt.UTC(), Correlation: input.Correlation,
	}
	if err := m.store.CreatePermissionRequest(ctx, value); err != nil {
		return nil, err
	}
	return clone(value), nil
}

func (m *Manager) Get(ctx context.Context, id ID) (*Request, error) {
	if strings.TrimSpace(string(id)) == "" {
		return nil, ErrInvalidRequest
	}
	value, err := m.store.LoadPermissionRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	value, err = m.expireIfDue(ctx, value)
	if err != nil {
		return nil, err
	}
	return clone(value), nil
}

func (m *Manager) ListPending(ctx context.Context) ([]*Request, error) {
	values, err := m.store.ListPendingPermissionRequests(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*Request, 0, len(values))
	for _, value := range values {
		value, err = m.expireIfDue(ctx, value)
		if err != nil {
			return nil, err
		}
		if value.Status == StatusPending {
			result = append(result, clone(value))
		}
	}
	return result, nil
}

func (m *Manager) Approve(ctx context.Context, id ID, authority string) (*Request, error) {
	return m.resolve(ctx, id, StatusApproved, authority)
}

func (m *Manager) Deny(ctx context.Context, id ID, authority string) (*Request, error) {
	return m.resolve(ctx, id, StatusDenied, authority)
}

func (m *Manager) Cancel(ctx context.Context, id ID) (*Request, error) {
	return m.resolve(ctx, id, StatusCancelled, "")
}

func (m *Manager) Expire(ctx context.Context, id ID) (*Request, error) {
	if strings.TrimSpace(string(id)) == "" {
		return nil, ErrInvalidRequest
	}
	current, err := m.store.LoadPermissionRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status == StatusExpired {
		return clone(current), nil
	}
	if current.Status != StatusPending || m.clock.Now().UTC().Before(current.ExpiresAt) {
		return nil, ErrInvalidTransition
	}
	return m.transition(ctx, current, StatusExpired, "")
}

func (m *Manager) resolve(ctx context.Context, id ID, target Status, authority string) (*Request, error) {
	if strings.TrimSpace(string(id)) == "" || ((target == StatusApproved || target == StatusDenied) && strings.TrimSpace(authority) == "") {
		return nil, ErrInvalidRequest
	}
	current, err := m.store.LoadPermissionRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status == target {
		return clone(current), nil
	}
	if current.Status != StatusPending {
		return nil, ErrInvalidTransition
	}
	if !m.clock.Now().UTC().Before(current.ExpiresAt) {
		_, err := m.transition(ctx, current, StatusExpired, "")
		if err != nil && !errors.Is(err, ErrInvalidTransition) {
			return nil, err
		}
		return nil, ErrInvalidTransition
	}
	return m.transition(ctx, current, target, strings.TrimSpace(authority))
}

func (m *Manager) expireIfDue(ctx context.Context, current *Request) (*Request, error) {
	if current.Status != StatusPending || m.clock.Now().UTC().Before(current.ExpiresAt) {
		return current, nil
	}
	updated, err := m.transition(ctx, current, StatusExpired, "")
	if errors.Is(err, ErrInvalidTransition) {
		return m.store.LoadPermissionRequest(ctx, current.ID)
	}
	return updated, err
}

func (m *Manager) transition(ctx context.Context, current *Request, target Status, authority string) (*Request, error) {
	changed, err := m.store.TransitionPermissionRequest(ctx, current.ID, target, m.clock.Now().UTC(), authority)
	if err != nil {
		return nil, err
	}
	updated, err := m.store.LoadPermissionRequest(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	if !changed && updated.Status != target {
		return nil, ErrInvalidTransition
	}
	return clone(updated), nil
}

func validateCreate(input CreateRequest, now time.Time) error {
	if strings.TrimSpace(input.DollID) == "" || strings.TrimSpace(input.OwnerID) == "" || !input.ExpiresAt.After(now) {
		return ErrInvalidRequest
	}
	if input.Origin != OriginPolicyRequired && input.Origin != OriginVoluntary {
		return ErrInvalidRequest
	}
	hasAction := strings.TrimSpace(input.Action) != ""
	if input.PolicyDecision == actionpolicy.DecisionDeny ||
		(input.Origin == OriginPolicyRequired && input.PolicyDecision != actionpolicy.DecisionRequireApproval) ||
		(hasAction && input.Origin == OriginVoluntary && input.PolicyDecision != actionpolicy.DecisionAllow) {
		return ErrInvalidRequest
	}
	if !hasAction && strings.TrimSpace(input.Question) == "" {
		return ErrInvalidRequest
	}
	if input.Origin == OriginPolicyRequired && (strings.TrimSpace(input.Capability) == "" || strings.TrimSpace(input.Operation) == "") {
		return ErrInvalidRequest
	}
	if len(input.Arguments) > 0 && !json.Valid(input.Arguments) {
		return ErrInvalidRequest
	}
	return nil
}

func clone(value *Request) *Request {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Arguments = append(json.RawMessage(nil), value.Arguments...)
	if value.ResolvedAt != nil {
		resolved := *value.ResolvedAt
		copy.ResolvedAt = &resolved
	}
	return &copy
}
