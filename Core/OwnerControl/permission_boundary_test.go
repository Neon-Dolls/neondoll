package ownercontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
)

func TestLocalBoundaryAuthenticatesPermissionOwner(t *testing.T) {
	boundary := NewBoundary()
	if _, err := boundary.AuthenticateOwner(context.Background()); !errors.Is(err, permissionrequest.ErrUnauthenticated) {
		t.Fatalf("unauthenticated error = %v", err)
	}
	authenticated := boundary.AuthenticatePermissionDecisions(context.Background())
	actor, err := boundary.AuthenticateOwner(authenticated)
	if err != nil || actor.ID != localOwnerID {
		t.Fatalf("actor = %#v, %v", actor, err)
	}
	if err := boundary.AuthorizeDoll(authenticated, actor, "doll-1"); err != nil {
		t.Fatalf("AuthorizeDoll: %v", err)
	}
}
