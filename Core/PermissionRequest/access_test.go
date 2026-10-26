package permissionrequest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
)

func createQuestion(t *testing.T, fx *fixture, dollID, ownerID, question string) *permissionrequest.Request {
	t.Helper()
	request, err := fx.service.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: dollID, OwnerID: ownerID, Origin: permissionrequest.OriginVoluntary,
		Question: question, ExpiresAt: fx.clock.now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestAuthenticatedOwnerAccess(t *testing.T) {
	fx, _, boundary := newDecisionFixture(t)
	owned := createQuestion(t, fx, "doll-1", "owner-1", "Owned?")
	createQuestion(t, fx, "doll-2", "owner-2", "Unrelated?")
	access := permissionrequest.NewAccessService(fx.service, boundary)

	requests, err := access.ListPending(authenticatedOwner("owner-1"))
	if err != nil || len(requests) != 1 || requests[0].ID != owned.ID {
		t.Fatalf("pending requests = %#v, %v", requests, err)
	}
	got, err := access.Get(authenticatedOwner("owner-1"), owned.ID)
	if err != nil || got.ID != owned.ID || got.Correlation != owned.Correlation {
		t.Fatalf("request = %#v, %v", got, err)
	}
}

func TestPermissionAccessRejectsUnauthenticatedAndUnrelatedActors(t *testing.T) {
	fx, _, boundary := newDecisionFixture(t)
	owned := createQuestion(t, fx, "doll-1", "owner-1", "Owned?")
	access := permissionrequest.NewAccessService(fx.service, boundary)

	if _, err := access.ListPending(context.Background()); !errors.Is(err, permissionrequest.ErrUnauthenticated) {
		t.Fatalf("unauthenticated list error = %v", err)
	}
	if _, err := access.Get(context.Background(), owned.ID); !errors.Is(err, permissionrequest.ErrUnauthenticated) {
		t.Fatalf("unauthenticated get error = %v", err)
	}
	if _, err := access.Get(authenticatedOwner("owner-2"), owned.ID); !errors.Is(err, permissionrequest.ErrUnauthorized) {
		t.Fatalf("unrelated get error = %v", err)
	}
	requests, err := access.ListPending(authenticatedOwner("owner-2"))
	if err != nil || len(requests) != 0 {
		t.Fatalf("unrelated list = %#v, %v", requests, err)
	}
}

func TestPermissionAccessEmptyAndExpiredResults(t *testing.T) {
	fx, _, boundary := newDecisionFixture(t)
	access := permissionrequest.NewAccessService(fx.service, boundary)
	owner := authenticatedOwner("owner-1")
	requests, err := access.ListPending(owner)
	if err != nil || len(requests) != 0 {
		t.Fatalf("empty list = %#v, %v", requests, err)
	}

	expiring := createQuestion(t, fx, "doll-1", "owner-1", "Still pending?")
	fx.clock.now = expiring.ExpiresAt
	requests, err = access.ListPending(owner)
	if err != nil || len(requests) != 0 {
		t.Fatalf("expired pending list = %#v, %v", requests, err)
	}
	detail, err := access.Get(owner, expiring.ID)
	if err != nil || detail.Status != permissionrequest.StatusExpired || detail.ResolvedAt == nil {
		t.Fatalf("expired detail = %#v, %v", detail, err)
	}
}

func TestPermissionAccessSurvivesRestart(t *testing.T) {
	fx, _, boundary := newDecisionFixture(t)
	created := createQuestion(t, fx, "doll-1", "owner-1", "After restart?")
	fx.reopen()
	access := permissionrequest.NewAccessService(fx.service, boundary)

	requests, err := access.ListPending(authenticatedOwner("owner-1"))
	if err != nil || len(requests) != 1 || requests[0].ID != created.ID {
		t.Fatalf("restarted list = %#v, %v", requests, err)
	}
	detail, err := access.Get(authenticatedOwner("owner-1"), created.ID)
	if err != nil || detail.ID != created.ID {
		t.Fatalf("restarted detail = %#v, %v", detail, err)
	}
}
