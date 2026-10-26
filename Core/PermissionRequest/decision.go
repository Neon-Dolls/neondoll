package permissionrequest

import (
	"context"
	"strings"
)

type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionDeny    Decision = "deny"
)

// AuthenticatedOwner is an identity established by a trusted Core boundary.
// It is not constructed from decision request payloads.
type AuthenticatedOwner struct {
	ID string
}

// OwnerDecisionBoundary authenticates the acting Owner and verifies their
// authority over a Doll. Implementations may be local administrative
// capabilities now or authenticated Gateway boundaries in the future.
type OwnerDecisionBoundary interface {
	AuthenticateOwner(context.Context) (AuthenticatedOwner, error)
	AuthorizeDoll(context.Context, AuthenticatedOwner, string) error
}

// DecisionRequest contains no actor identity. The boundary derives the actor
// exclusively from authenticated context.
type DecisionRequest struct {
	RequestID ID
	Decision  Decision
}

// DecisionService is the transport-independent authenticated entry point for
// Owner decisions. It records the decision but never executes the action.
type DecisionService struct {
	requests *Manager
	boundary OwnerDecisionBoundary
}

func NewDecisionService(requests *Manager, boundary OwnerDecisionBoundary) *DecisionService {
	return &DecisionService{requests: requests, boundary: boundary}
}

func (s *DecisionService) Resolve(ctx context.Context, input DecisionRequest) (*Request, error) {
	if s == nil || s.requests == nil || s.boundary == nil || strings.TrimSpace(string(input.RequestID)) == "" ||
		(input.Decision != DecisionApprove && input.Decision != DecisionDeny) {
		return nil, ErrInvalidRequest
	}
	actor, err := s.boundary.AuthenticateOwner(ctx)
	if err != nil || strings.TrimSpace(actor.ID) == "" {
		return nil, ErrUnauthenticated
	}
	request, err := s.requests.Get(ctx, input.RequestID)
	if err != nil {
		return nil, err
	}
	if request.Status == StatusExpired {
		return nil, ErrExpired
	}
	if actor.ID != request.OwnerID {
		return nil, ErrUnauthorized
	}
	if err := s.boundary.AuthorizeDoll(ctx, actor, request.DollID); err != nil {
		return nil, ErrUnauthorized
	}
	if input.Decision == DecisionApprove {
		return s.requests.Approve(ctx, input.RequestID, actor.ID)
	}
	return s.requests.Deny(ctx, input.RequestID, actor.ID)
}
