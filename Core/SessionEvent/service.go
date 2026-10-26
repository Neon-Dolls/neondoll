package sessionevent

import (
	"context"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/google/uuid"
)

type Store interface {
	CreateDelivery(context.Context, *Delivery) error
	UpdateDelivery(context.Context, string, DeliveryStatus, string, time.Time) (*Delivery, error)
	LoadDelivery(context.Context, string) (*Delivery, error)
	ListPendingDeliveries(context.Context) ([]*Delivery, error)
}

// Transport is implemented by the future Gateway host and interactive Body
// adapters. A stable Delivery ID permits transport-side deduplication.
type Transport interface {
	DeliverSessionEvent(context.Context, session.Owner, Delivery) error
}

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Service struct {
	store        Store
	sessions     session.Service
	participants session.ParticipantService
	transport    Transport
	clock        Clock
}

func New(store Store, sessions session.Service, participants session.ParticipantService, transport Transport) (*Service, error) {
	return NewWithClock(store, sessions, participants, transport, realClock{})
}

func NewWithClock(store Store, sessions session.Service, participants session.ParticipantService, transport Transport, clock Clock) (*Service, error) {
	if store == nil || sessions == nil || participants == nil || transport == nil || clock == nil {
		return nil, ErrTransportUnavailable
	}
	return &Service{store: store, sessions: sessions, participants: participants, transport: transport, clock: clock}, nil
}

func (s *Service) Emit(ctx context.Context, event Event, targetPrincipalID string) (*Delivery, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	resolved, err := s.sessions.Lookup(ctx, event.SessionID)
	if err != nil || resolved.Status != session.StatusActive {
		return nil, ErrSessionUnavailable
	}
	if targetPrincipalID != "" {
		eligible := false
		bindings, listErr := s.participants.ListParticipants(ctx, event.SessionID)
		if listErr != nil {
			return nil, listErr
		}
		for _, binding := range bindings {
			if binding.PrincipalID == targetPrincipalID {
				eligible = true
				break
			}
		}
		if !eligible {
			return nil, ErrRecipientUnavailable
		}
	}
	now := s.clock.Now().UTC()
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	delivery := &Delivery{ID: uuid.NewString(), Event: event, TargetPrincipalID: targetPrincipalID, Owner: resolved.Owner,
		Status: DeliveryPending, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateDelivery(ctx, delivery); err != nil {
		return nil, err
	}
	return s.attempt(ctx, delivery)
}

func (s *Service) ReplayPending(ctx context.Context) ([]*Delivery, error) {
	pending, err := s.store.ListPendingDeliveries(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*Delivery, 0, len(pending))
	for _, delivery := range pending {
		updated, attemptErr := s.attempt(ctx, delivery)
		if attemptErr != nil && updated == nil {
			return nil, attemptErr
		}
		result = append(result, updated)
	}
	return result, nil
}

func (s *Service) GetDelivery(ctx context.Context, id string) (*Delivery, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrInvalidEvent
	}
	return s.store.LoadDelivery(ctx, id)
}

func (s *Service) attempt(ctx context.Context, delivery *Delivery) (*Delivery, error) {
	resolved, err := s.sessions.Lookup(ctx, delivery.Event.SessionID)
	if err != nil || resolved.Status != session.StatusActive || resolved.Owner != delivery.Owner {
		updated, updateErr := s.store.UpdateDelivery(ctx, delivery.ID, DeliveryFailed, ErrSessionUnavailable.Error(), s.clock.Now().UTC())
		if updateErr != nil {
			return nil, updateErr
		}
		return updated, ErrSessionUnavailable
	}
	if err := s.transport.DeliverSessionEvent(ctx, delivery.Owner, *delivery); err != nil {
		updated, updateErr := s.store.UpdateDelivery(ctx, delivery.ID, DeliveryFailed, err.Error(), s.clock.Now().UTC())
		if updateErr != nil {
			return nil, updateErr
		}
		return updated, err
	}
	updated, err := s.store.UpdateDelivery(ctx, delivery.ID, DeliveryAccepted, "", s.clock.Now().UTC())
	if err != nil {
		return nil, err
	}
	return updated, nil
}
