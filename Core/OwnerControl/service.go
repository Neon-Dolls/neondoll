// Package ownercontrol exposes Core owner operations only through an explicit
// local administrative boundary.
package ownercontrol

import (
	"context"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
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

type Service struct {
	enrollment  *enrollment.Service
	invitations *invitation.Service
	boundary    *Boundary
}

func NewService(enrollments *enrollment.Service, invitations *invitation.Service, boundary *Boundary) *Service {
	return &Service{enrollment: enrollments, invitations: invitations, boundary: boundary}
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
