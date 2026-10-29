// Package communication routes Doll-initiated messages through Core's
// existing durable Interaction Session event delivery boundary.
package communication

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

const (
	Capability        = "core.communication"
	OperationSendText = "send_text"
)

var (
	ErrInvalidRequest       = errors.New("communication: invalid request")
	ErrUnknownRecipient     = errors.New("communication: unknown recipient")
	ErrRecipientUnavailable = errors.New("communication: recipient unavailable")
)

type Correlation struct {
	SessionID      string
	TurnID         string
	ToolCallID     string
	CognitiveRunID string
	IntentionID    string
}

type SendTextRequest struct {
	RecipientPrincipalID string
	Text                 string
	Correlation          Correlation
}

type Result struct {
	DeliveryID string
	SessionID  session.ID
	Status     sessionevent.DeliveryStatus
	Failure    string
}

type Sender interface {
	SendText(context.Context, SendTextRequest) (Result, error)
}

type Service struct {
	sessions     session.Service
	participants session.ParticipantService
	events       *sessionevent.Service
}

func New(sessions session.Service, participants session.ParticipantService, events *sessionevent.Service) (*Service, error) {
	if sessions == nil || participants == nil || events == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{sessions: sessions, participants: participants, events: events}, nil
}

func (s *Service) SendText(ctx context.Context, request SendTextRequest) (Result, error) {
	recipient := strings.TrimSpace(request.RecipientPrincipalID)
	text := strings.TrimSpace(request.Text)
	if recipient == "" || text == "" {
		return Result{}, ErrInvalidRequest
	}
	bindings, err := s.participants.ListParticipantSessions(ctx, recipient)
	if err != nil {
		return Result{}, err
	}
	// Prefer the most recently observed eligible conversation. Stable Session
	// ID ordering resolves equal timestamps without exposing Gateway selection
	// to the model.
	sort.SliceStable(bindings, func(i, j int) bool {
		if bindings[i].LastSeenAt.Equal(bindings[j].LastSeenAt) {
			return string(bindings[i].SessionID) < string(bindings[j].SessionID)
		}
		return bindings[i].LastSeenAt.After(bindings[j].LastSeenAt)
	})
	var selected *session.InteractionSession
	for _, binding := range bindings {
		candidate, lookupErr := s.sessions.Lookup(ctx, binding.SessionID)
		if lookupErr == nil && candidate.Status == session.StatusActive && candidate.Owner.Kind == session.OwnerGateway {
			selected = candidate
			break
		}
	}
	if selected == nil {
		if len(bindings) == 0 {
			return Result{}, ErrUnknownRecipient
		}
		return Result{}, ErrRecipientUnavailable
	}
	event, err := sessionevent.NewEvent(selected.ID, sessionevent.TypeMessage, sessionevent.MessagePayload{Parts: []sessionevent.MessagePart{{Type: sessionevent.PartText, Text: text}}}, sessionevent.Correlation{
		TurnID: request.Correlation.TurnID, ToolCallID: request.Correlation.ToolCallID,
		CognitiveRunID: request.Correlation.CognitiveRunID, IntentionID: request.Correlation.IntentionID,
	}, time.Now())
	if err != nil {
		return Result{}, err
	}
	delivery, deliveryErr := s.events.Emit(ctx, event, recipient)
	if delivery == nil {
		return Result{}, deliveryErr
	}
	return Result{DeliveryID: delivery.ID, SessionID: selected.ID, Status: delivery.Status, Failure: delivery.Error}, deliveryErr
}

var _ Sender = (*Service)(nil)
