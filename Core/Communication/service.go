// Package communication routes Doll-initiated messages through Core's
// existing durable Interaction Session event delivery boundary.
package communication

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/Core/SessionEvent"
)

const (
	Capability         = "core.communication"
	OperationSendText  = "send_text"
	OperationListPeers = "list_peers"
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
	ListPeers(context.Context) ([]Peer, error)
}

type Peer struct {
	PrincipalID           string                     `json:"principal_id"`
	Authority             externalidentity.Authority `json:"authority"`
	Platforms             []string                   `json:"platforms,omitempty"`
	IdentityCount         int                        `json:"identity_count"`
	RelationshipAvailable bool                       `json:"relationship_available"`
	ActiveGatewayRoute    bool                       `json:"active_gateway_route"`
	EligibleSessionCount  int                        `json:"eligible_session_count"`
	KnownSessionCount     int                        `json:"known_session_count"`
}

type Service struct {
	directory    externalidentity.PrincipalDirectory
	sessions     session.Service
	participants session.ParticipantService
	events       *sessionevent.Service
}

func New(directory externalidentity.PrincipalDirectory, sessions session.Service, participants session.ParticipantService, events *sessionevent.Service) (*Service, error) {
	if directory == nil || sessions == nil || participants == nil || events == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{directory: directory, sessions: sessions, participants: participants, events: events}, nil
}

func (s *Service) ListPeers(ctx context.Context) ([]Peer, error) {
	records, err := s.directory.ListPrincipals(ctx)
	if err != nil {
		return nil, err
	}
	peers := make([]Peer, 0, len(records))
	for _, record := range records {
		bindings, listErr := s.participants.ListParticipantSessions(ctx, string(record.Principal.ID))
		if listErr != nil {
			return nil, listErr
		}
		_, eligible := s.selectRoute(ctx, bindings)
		platformSet := make(map[string]struct{})
		for _, identity := range record.Identities {
			if identity.Platform != "" {
				platformSet[identity.Platform] = struct{}{}
			}
		}
		platforms := make([]string, 0, len(platformSet))
		for platform := range platformSet {
			platforms = append(platforms, platform)
		}
		sort.Strings(platforms)
		peers = append(peers, Peer{PrincipalID: string(record.Principal.ID), Authority: record.Principal.Authority, Platforms: platforms, IdentityCount: len(record.Identities), RelationshipAvailable: len(bindings) > 0, ActiveGatewayRoute: eligible > 0, EligibleSessionCount: eligible, KnownSessionCount: len(bindings)})
	}
	return peers, nil
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
	selected, _ := s.selectRoute(ctx, bindings)
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

// selectRoute is the shared eligibility rule used by discovery and sending.
func (s *Service) selectRoute(ctx context.Context, bindings []*session.ParticipantBinding) (*session.InteractionSession, int) {
	bindings = append([]*session.ParticipantBinding(nil), bindings...)
	sort.SliceStable(bindings, func(i, j int) bool {
		if bindings[i].LastSeenAt.Equal(bindings[j].LastSeenAt) {
			return string(bindings[i].SessionID) < string(bindings[j].SessionID)
		}
		return bindings[i].LastSeenAt.After(bindings[j].LastSeenAt)
	})
	var selected *session.InteractionSession
	eligible := 0
	for _, binding := range bindings {
		candidate, lookupErr := s.sessions.Lookup(ctx, binding.SessionID)
		if lookupErr == nil && candidate.Status == session.StatusActive && candidate.Owner.Kind == session.OwnerGateway {
			eligible++
			if selected == nil {
				selected = candidate
			}
		}
	}
	return selected, eligible
}

var _ Sender = (*Service)(nil)
