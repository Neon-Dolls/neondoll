package sessionevent

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/Neon-Dolls/neondoll/Core/ExternalIdentity"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

type PermissionBridge struct {
	requests     *permissionrequest.Manager
	principals   externalidentity.PrincipalResolver
	sessions     session.Service
	participants session.ParticipantService
	delivery     *Service
}

func NewPermissionBridge(requests *permissionrequest.Manager, principals externalidentity.PrincipalResolver, sessions session.Service, participants session.ParticipantService, delivery *Service) (*PermissionBridge, error) {
	if requests == nil || principals == nil || sessions == nil || participants == nil || delivery == nil {
		return nil, ErrInvalidEvent
	}
	return &PermissionBridge{requests: requests, principals: principals, sessions: sessions, participants: participants, delivery: delivery}, nil
}

// Present emits a structured permission request to one active Session in
// which the configured Owner principal participates. Membership selects a
// destination; configured Owner authority is checked independently.
func (b *PermissionBridge) Present(ctx context.Context, requestID permissionrequest.ID) (*Delivery, error) {
	request, err := b.requests.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if request.Status == permissionrequest.StatusExpired {
		return nil, permissionrequest.ErrExpired
	}
	if request.Status != permissionrequest.StatusPending {
		return nil, permissionrequest.ErrInvalidTransition
	}
	principal, err := b.principals.LookupPrincipal(ctx, externalidentity.PrincipalID(request.OwnerID))
	if err != nil || principal.Authority != externalidentity.AuthorityOwner {
		return nil, permissionrequest.ErrUnauthorized
	}
	bindings, err := b.participants.ListParticipantSessions(ctx, request.OwnerID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(bindings, func(i, j int) bool { return string(bindings[i].SessionID) < string(bindings[j].SessionID) })
	var selected session.ID
	for _, binding := range bindings {
		candidate, lookupErr := b.sessions.Lookup(ctx, binding.SessionID)
		if lookupErr == nil && candidate.Status == session.StatusActive {
			if request.Correlation.SessionID == string(candidate.ID) {
				selected = candidate.ID
				break
			}
			if selected == "" {
				selected = candidate.ID
			}
		}
	}
	if selected == "" {
		return nil, ErrRecipientUnavailable
	}
	question := request.Question
	if question == "" {
		question = request.Action
	}
	payload := PermissionRequestedPayload{
		RequestID: string(request.ID), Question: question, Origin: string(request.Origin), ExpiresAt: request.ExpiresAt,
		Action: ActionDetails{Action: request.Action, Capability: request.Capability, Operation: request.Operation, Arguments: request.Arguments},
	}
	correlation := Correlation{RequestID: string(request.ID), TurnID: request.Correlation.TurnID,
		ToolCallID: request.Correlation.ToolCallID, CognitiveRunID: request.Correlation.CognitiveRunID}
	event, err := NewEvent(selected, TypePermissionRequested, payload, correlation, b.delivery.clock.Now())
	if err != nil {
		return nil, err
	}
	return b.delivery.Emit(ctx, event, request.OwnerID)
}

// Decide records one structured decision. Approval remains state only and
// never executes or resumes the requested action.
func (b *PermissionBridge) Decide(ctx context.Context, sessionID session.ID, acting externalidentity.Principal, payload PermissionDecidedPayload) (*permissionrequest.Request, error) {
	configured, err := b.principals.LookupPrincipal(ctx, acting.ID)
	if err != nil || configured != acting || configured.Authority != externalidentity.AuthorityOwner {
		return nil, permissionrequest.ErrUnauthorized
	}
	bindings, err := b.participants.ListParticipants(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	bound := false
	for _, binding := range bindings {
		if binding.PrincipalID == string(acting.ID) {
			bound = true
			break
		}
	}
	if !bound {
		return nil, permissionrequest.ErrUnauthorized
	}
	request, err := b.requests.Get(ctx, permissionrequest.ID(payload.RequestID))
	if err != nil {
		return nil, err
	}
	if request.Status == permissionrequest.StatusExpired {
		return nil, permissionrequest.ErrExpired
	}
	if request.OwnerID != string(acting.ID) {
		return nil, permissionrequest.ErrUnauthorized
	}
	switch payload.Decision {
	case permissionrequest.DecisionApprove:
		return b.requests.Approve(ctx, request.ID, string(acting.ID))
	case permissionrequest.DecisionDeny:
		return b.requests.Deny(ctx, request.ID, string(acting.ID))
	default:
		return nil, permissionrequest.ErrInvalidRequest
	}
}

func DecodePermissionDecision(event Event) (PermissionDecidedPayload, error) {
	if event.Type != TypePermissionDecided || event.Validate() != nil {
		return PermissionDecidedPayload{}, ErrInvalidEvent
	}
	var payload PermissionDecidedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return PermissionDecidedPayload{}, ErrInvalidEvent
	}
	return payload, nil
}
