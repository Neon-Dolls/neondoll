package sessionevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/google/uuid"
)

type Store interface {
	CreateDelivery(context.Context, *Delivery) error
	CreatePermissionDelivery(context.Context, *Delivery, PermissionPresentation) error
	UpdateDelivery(context.Context, string, DeliveryStatus, string, time.Time) (*Delivery, error)
	LoadDelivery(context.Context, string) (*Delivery, error)
	ListPendingDeliveries(context.Context) ([]*Delivery, error)
	LoadPermissionPresentation(context.Context, string) (*PermissionPresentation, error)
}

// Transport is implemented by the future Gateway host and interactive Body
// adapters. A stable Delivery ID permits transport-side deduplication.
type Transport interface {
	DeliverSessionEvent(context.Context, session.Owner, Delivery) error
}

type terminalDeliveryError struct{ error }

// TerminalDeliveryError marks an adapter error as non-retryable. Ordinary
// transport errors remain pending and are retried with the stable delivery ID.
func TerminalDeliveryError(err error) error {
	if err == nil {
		return nil
	}
	return terminalDeliveryError{error: err}
}

func (terminalDeliveryError) Terminal() bool  { return true }
func (e terminalDeliveryError) Unwrap() error { return e.error }

func isTerminalDeliveryError(err error) bool {
	var terminal interface{ Terminal() bool }
	return errors.As(err, &terminal) && terminal.Terminal()
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
	return s.emit(ctx, event, targetPrincipalID, "")
}

func (s *Service) EmitPermission(ctx context.Context, event Event, targetPrincipalID, requestID string) (*Delivery, error) {
	if strings.TrimSpace(requestID) == "" || event.Type != TypePermissionRequested || event.Correlation.RequestID != requestID {
		return nil, ErrInvalidEvent
	}
	return s.emit(ctx, event, targetPrincipalID, requestID)
}

func (s *Service) emit(ctx context.Context, event Event, targetPrincipalID, requestID string) (*Delivery, error) {
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
	var createErr error
	if requestID == "" {
		createErr = s.store.CreateDelivery(ctx, delivery)
	} else {
		createErr = s.store.CreatePermissionDelivery(ctx, delivery, PermissionPresentation{
			RequestID: requestID, SessionID: event.SessionID, DeliveryID: delivery.ID, CreatedAt: now,
		})
	}
	if createErr != nil {
		return nil, createErr
	}
	return s.attempt(ctx, delivery)
}

func (s *Service) RetryDelivery(ctx context.Context, id string) (*Delivery, error) {
	delivery, err := s.GetDelivery(ctx, id)
	if err != nil {
		return nil, err
	}
	if delivery.Status != DeliveryPending {
		return delivery, nil
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
	if delivery.TargetPrincipalID != "" {
		bindings, listErr := s.participants.ListParticipants(ctx, delivery.Event.SessionID)
		if listErr != nil {
			return s.recordAttempt(ctx, delivery, listErr, false)
		}
		eligible := false
		for _, binding := range bindings {
			if binding.PrincipalID == delivery.TargetPrincipalID {
				eligible = true
				break
			}
		}
		if !eligible {
			return s.recordAttempt(ctx, delivery, ErrRecipientUnavailable, true)
		}
	}
	if err := s.transport.DeliverSessionEvent(ctx, delivery.Owner, *delivery); err != nil {
		return s.recordAttempt(ctx, delivery, err, isTerminalDeliveryError(err))
	}
	updated, err := s.store.UpdateDelivery(ctx, delivery.ID, DeliveryAccepted, "", s.clock.Now().UTC())
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Service) recordAttempt(ctx context.Context, delivery *Delivery, attemptErr error, terminal bool) (*Delivery, error) {
	status := DeliveryPending
	if terminal {
		status = DeliveryFailed
	}
	updated, err := s.store.UpdateDelivery(ctx, delivery.ID, status, attemptErr.Error(), s.clock.Now().UTC())
	if err != nil {
		return nil, err
	}
	return updated, attemptErr
}
