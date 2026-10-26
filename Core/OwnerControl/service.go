// Package ownercontrol exposes Core owner operations only through an explicit
// local administrative boundary.
package ownercontrol

import (
	"context"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

const localOwnerID = "local-admin"

type authorizationKey struct{}

// Boundary is an unforgeable in-process capability paired with the local
// Unix-socket server. Approval codes and caller-supplied owner IDs never grant
// owner authority.
type Boundary struct{ token *struct{} }

func NewBoundary() *Boundary { return &Boundary{token: &struct{}{}} }

func (b *Boundary) AuthorizeEnrollment(ctx context.Context, _ string, _ enrollment.OwnerAction) error {
	if b == nil || ctx.Value(authorizationKey{}) != b.token {
		return enrollment.ErrUnauthorized
	}
	return nil
}

func (b *Boundary) authorize(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorizationKey{}, b.token)
}

func (b *Boundary) AuthenticateOwner(ctx context.Context) (permissionrequest.AuthenticatedOwner, error) {
	if b == nil || ctx.Value(authorizationKey{}) != b.token {
		return permissionrequest.AuthenticatedOwner{}, permissionrequest.ErrUnauthenticated
	}
	return permissionrequest.AuthenticatedOwner{ID: localOwnerID}, nil
}

func (b *Boundary) AuthorizeDoll(ctx context.Context, actor permissionrequest.AuthenticatedOwner, dollID string) error {
	if b == nil || ctx.Value(authorizationKey{}) != b.token || actor.ID != localOwnerID || dollID == "" {
		return permissionrequest.ErrUnauthorized
	}
	return nil
}

// AuthenticatePermissionDecisions marks a context as originating from the
// existing local administrative boundary. The returned context carries an
// unforgeable in-process capability, not a caller-supplied Owner ID.
func (b *Boundary) AuthenticatePermissionDecisions(ctx context.Context) context.Context {
	return b.authorize(ctx)
}

var _ permissionrequest.OwnerDecisionBoundary = (*Boundary)(nil)

type Service struct {
	enrollment  *enrollment.Service
	invitations *invitation.Service
	boundary    *Boundary
	permissions *permissionrequest.AccessService
}

func NewService(enrollments *enrollment.Service, invitations *invitation.Service, boundary *Boundary, permissions *permissionrequest.AccessService) *Service {
	return &Service{enrollment: enrollments, invitations: invitations, boundary: boundary, permissions: permissions}
}

func (s *Service) ListPending(ctx context.Context) ([]*enrollment.PendingEnrollment, error) {
	return s.enrollment.ListPending(s.boundary.authorize(ctx), localOwnerID)
}

func (s *Service) Approve(ctx context.Context, code string, permissions []string) (*dollnetwork.EnrollmentReceipt, error) {
	return s.enrollment.Approve(s.boundary.authorize(ctx), code, localOwnerID, permissions)
}

func (s *Service) Deny(ctx context.Context, code string) error {
	return s.enrollment.Deny(s.boundary.authorize(ctx), code, localOwnerID)
}

func (s *Service) CreateInvitation(ctx context.Context, lifetime time.Duration, permissions []string, endpoints dollnetwork.Endpoints) (*invitation.CreatedInvitation, error) {
	authorized := s.boundary.authorize(ctx)
	if err := s.boundary.AuthorizeEnrollment(authorized, localOwnerID, enrollment.OwnerAction{Operation: "create_invitation"}); err != nil {
		return nil, err
	}
	return s.invitations.Create(authorized, invitation.CreateParams{Lifetime: lifetime, Permissions: permissions, BootstrapEndpoints: endpoints})
}

func (s *Service) ListBodies(ctx context.Context) ([]*enrollment.BodyRecord, error) {
	return s.enrollment.ListBodies(s.boundary.authorize(ctx), localOwnerID)
}

func (s *Service) Revoke(ctx context.Context, bodyID string) error {
	return s.enrollment.Revoke(s.boundary.authorize(ctx), bodyID, localOwnerID)
}

func (s *Service) ListPermissionRequests(ctx context.Context) ([]*permissionrequest.Request, error) {
	if s.permissions == nil {
		return nil, permissionrequest.ErrInvalidRequest
	}
	return s.permissions.ListPending(s.boundary.authorize(ctx))
}

func (s *Service) GetPermissionRequest(ctx context.Context, id permissionrequest.ID) (*permissionrequest.Request, error) {
	if s.permissions == nil {
		return nil, permissionrequest.ErrInvalidRequest
	}
	return s.permissions.Get(s.boundary.authorize(ctx), id)
}
