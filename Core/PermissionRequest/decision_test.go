package permissionrequest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
)

type ownerContextKey struct{}

type testOwnerBoundary struct {
	allowed map[string]map[string]bool
}

func (b testOwnerBoundary) AuthenticateOwner(ctx context.Context) (permissionrequest.AuthenticatedOwner, error) {
	owner, ok := ctx.Value(ownerContextKey{}).(string)
	if !ok || owner == "" {
		return permissionrequest.AuthenticatedOwner{}, permissionrequest.ErrUnauthenticated
	}
	return permissionrequest.AuthenticatedOwner{ID: owner}, nil
}

func (b testOwnerBoundary) AuthorizeDoll(_ context.Context, owner permissionrequest.AuthenticatedOwner, dollID string) error {
	if !b.allowed[owner.ID][dollID] {
		return permissionrequest.ErrUnauthorized
	}
	return nil
}

func authenticatedOwner(ownerID string) context.Context {
	return context.WithValue(context.Background(), ownerContextKey{}, ownerID)
}

func newDecisionFixture(t *testing.T) (*fixture, *permissionrequest.DecisionService, testOwnerBoundary) {
	t.Helper()
	fx := newFixture(t)
	boundary := testOwnerBoundary{allowed: map[string]map[string]bool{
		"owner-1": {"doll-1": true},
		"owner-2": {"doll-2": true},
	}}
	return fx, permissionrequest.NewDecisionService(fx.service, boundary), boundary
}

func resolve(t *testing.T, service *permissionrequest.DecisionService, ctx context.Context, id permissionrequest.ID, decision permissionrequest.Decision) (*permissionrequest.Request, error) {
	t.Helper()
	return service.Resolve(ctx, permissionrequest.DecisionRequest{RequestID: id, Decision: decision})
}

func TestAuthenticatedOwnerApprovalAndDenial(t *testing.T) {
	fx, decisions, _ := newDecisionFixture(t)
	approved := fx.create(permissionrequest.OriginVoluntary)
	got, err := resolve(t, decisions, authenticatedOwner("owner-1"), approved.ID, permissionrequest.DecisionApprove)
	if err != nil || got.Status != permissionrequest.StatusApproved || got.ResolvingAuthority != "owner-1" {
		t.Fatalf("approval = %#v, %v", got, err)
	}

	denied := fx.create(permissionrequest.OriginVoluntary)
	got, err = resolve(t, decisions, authenticatedOwner("owner-1"), denied.ID, permissionrequest.DecisionDeny)
	if err != nil || got.Status != permissionrequest.StatusDenied || got.ResolvingAuthority != "owner-1" {
		t.Fatalf("denial = %#v, %v", got, err)
	}
}

func TestDecisionRejectsUnauthenticatedAndUnrelatedOwner(t *testing.T) {
	fx, decisions, _ := newDecisionFixture(t)
	request := fx.create(permissionrequest.OriginVoluntary)
	if _, err := resolve(t, decisions, context.Background(), request.ID, permissionrequest.DecisionApprove); !errors.Is(err, permissionrequest.ErrUnauthenticated) {
		t.Fatalf("unauthenticated error = %v", err)
	}
	if _, err := resolve(t, decisions, authenticatedOwner("owner-2"), request.ID, permissionrequest.DecisionApprove); !errors.Is(err, permissionrequest.ErrUnauthorized) {
		t.Fatalf("unrelated owner error = %v", err)
	}
}

func TestDecisionRejectsWrongDollAndOwner(t *testing.T) {
	fx, _, _ := newDecisionFixture(t)
	request := fx.create(permissionrequest.OriginVoluntary)

	wrongDollBoundary := testOwnerBoundary{allowed: map[string]map[string]bool{"owner-1": {"doll-2": true}}}
	decisions := permissionrequest.NewDecisionService(fx.service, wrongDollBoundary)
	if _, err := resolve(t, decisions, authenticatedOwner("owner-1"), request.ID, permissionrequest.DecisionApprove); !errors.Is(err, permissionrequest.ErrUnauthorized) {
		t.Fatalf("wrong Doll error = %v", err)
	}
	if _, err := resolve(t, decisions, authenticatedOwner("owner-2"), request.ID, permissionrequest.DecisionApprove); !errors.Is(err, permissionrequest.ErrUnauthorized) {
		t.Fatalf("wrong Owner error = %v", err)
	}
}

func TestDecisionRejectsExpiredRequest(t *testing.T) {
	fx, decisions, _ := newDecisionFixture(t)
	request := fx.create(permissionrequest.OriginVoluntary)
	fx.clock.now = request.ExpiresAt
	if _, err := resolve(t, decisions, authenticatedOwner("owner-1"), request.ID, permissionrequest.DecisionApprove); !errors.Is(err, permissionrequest.ErrExpired) {
		t.Fatalf("expired error = %v", err)
	}
	stored, err := fx.service.Get(context.Background(), request.ID)
	if err != nil || stored.Status != permissionrequest.StatusExpired {
		t.Fatalf("stored expired request = %#v, %v", stored, err)
	}
}

func TestAuthenticatedDecisionIdempotenceAndConflict(t *testing.T) {
	fx, decisions, _ := newDecisionFixture(t)
	request := fx.create(permissionrequest.OriginVoluntary)
	owner := authenticatedOwner("owner-1")
	first, err := resolve(t, decisions, owner, request.ID, permissionrequest.DecisionApprove)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolve(t, decisions, owner, request.ID, permissionrequest.DecisionApprove)
	if err != nil || second.ResolvingAuthority != first.ResolvingAuthority || !second.ResolvedAt.Equal(*first.ResolvedAt) {
		t.Fatalf("idempotent decision = %#v, %v", second, err)
	}
	if _, err := resolve(t, decisions, owner, request.ID, permissionrequest.DecisionDeny); !errors.Is(err, permissionrequest.ErrInvalidTransition) {
		t.Fatalf("conflicting decision error = %v", err)
	}
	if _, err := resolve(t, decisions, authenticatedOwner("owner-2"), request.ID, permissionrequest.DecisionApprove); !errors.Is(err, permissionrequest.ErrUnauthorized) {
		t.Fatalf("other actor replay error = %v", err)
	}
}

func TestAuthenticatedDecisionPersistsAcrossRestart(t *testing.T) {
	fx, decisions, boundary := newDecisionFixture(t)
	request := fx.create(permissionrequest.OriginVoluntary)
	resolved, err := resolve(t, decisions, authenticatedOwner("owner-1"), request.ID, permissionrequest.DecisionDeny)
	if err != nil {
		t.Fatal(err)
	}
	fx.reopen()
	decisions = permissionrequest.NewDecisionService(fx.service, boundary)
	replayed, err := resolve(t, decisions, authenticatedOwner("owner-1"), request.ID, permissionrequest.DecisionDeny)
	if err != nil || replayed.Status != permissionrequest.StatusDenied || replayed.ResolvingAuthority != "owner-1" ||
		replayed.ResolvedAt == nil || resolved.ResolvedAt == nil || !replayed.ResolvedAt.Equal(*resolved.ResolvedAt) {
		t.Fatalf("restarted decision = %#v, %v", replayed, err)
	}
}

func TestDecisionRejectsInvalidDecision(t *testing.T) {
	fx, decisions, _ := newDecisionFixture(t)
	request := fx.create(permissionrequest.OriginVoluntary)
	_, err := resolve(t, decisions, authenticatedOwner("owner-1"), request.ID, permissionrequest.Decision("later"))
	if !errors.Is(err, permissionrequest.ErrInvalidRequest) {
		t.Fatalf("invalid decision error = %v", err)
	}
}
