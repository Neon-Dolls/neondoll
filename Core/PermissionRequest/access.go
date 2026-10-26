package permissionrequest

import (
	"context"
	"strings"
)

// AccessService is the transport-independent authenticated read boundary for
// Owner permission requests.
type AccessService struct {
	requests *Manager
	boundary OwnerDecisionBoundary
}

func NewAccessService(requests *Manager, boundary OwnerDecisionBoundary) *AccessService {
	return &AccessService{requests: requests, boundary: boundary}
}

func (s *AccessService) ListPending(ctx context.Context) ([]*Request, error) {
	actor, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	requests, err := s.requests.ListPending(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]*Request, 0, len(requests))
	for _, request := range requests {
		if request.OwnerID != actor.ID || s.boundary.AuthorizeDoll(ctx, actor, request.DollID) != nil {
			continue
		}
		visible = append(visible, request)
	}
	return visible, nil
}

func (s *AccessService) Get(ctx context.Context, id ID) (*Request, error) {
	if strings.TrimSpace(string(id)) == "" {
		return nil, ErrInvalidRequest
	}
	actor, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	request, err := s.requests.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if request.OwnerID != actor.ID || s.boundary.AuthorizeDoll(ctx, actor, request.DollID) != nil {
		return nil, ErrUnauthorized
	}
	return request, nil
}

func (s *AccessService) authenticate(ctx context.Context) (AuthenticatedOwner, error) {
	if s == nil || s.requests == nil || s.boundary == nil {
		return AuthenticatedOwner{}, ErrInvalidRequest
	}
	actor, err := s.boundary.AuthenticateOwner(ctx)
	if err != nil || strings.TrimSpace(actor.ID) == "" {
		return AuthenticatedOwner{}, ErrUnauthenticated
	}
	return actor, nil
}
