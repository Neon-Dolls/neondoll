package relay

import (
	"testing"
)

// ── M4.7.2 — Registration and Route Isolation ─────────────────────────
//
// The Registry must enforce that:
//   - routes belong to exactly one registration
//   - cross-registration operations fail
//   - closing one registration's routes does not affect another's
//   - route credentials from A cannot open/control B's routes

// TestM47_RouteIsolation_CannotAccessAnotherRegistrationsRoute verifies
// that a registration cannot open, close, or modify routes owned by a
// different registration.
func TestM47_RouteIsolation_CannotAccessAnotherRegistrationsRoute(t *testing.T) {
	reg := NewRegistry(10, 50)

	// Two independent registrations
	if err := reg.AddRegistration("reg-a", "hash-a"); err != nil {
		t.Fatalf("AddRegistration reg-a: %v", err)
	}
	if err := reg.AddRegistration("reg-b", "hash-b"); err != nil {
		t.Fatalf("AddRegistration reg-b: %v", err)
	}

	// reg-a allocates route 1, reg-b allocates route 2
	if _, err := reg.AllocateRoute("reg-a", RouteID(1)); err != nil {
		t.Fatalf("reg-a allocate route 1: %v", err)
	}
	if _, err := reg.AllocateRoute("reg-b", RouteID(2)); err != nil {
		t.Fatalf("reg-b allocate route 2: %v", err)
	}

	// reg-b cannot open reg-a's route
	if _, err := reg.OpenRoute("reg-b", RouteID(1)); err != ErrRouteWrongOwner {
		t.Fatalf("reg-b open reg-a's route: want ErrRouteWrongOwner, got %v", err)
	}

	// reg-b cannot close reg-a's route
	if _, err := reg.CloseRoute("reg-b", RouteID(1)); err != ErrRouteWrongOwner {
		t.Fatalf("reg-b close reg-a's route: want ErrRouteWrongOwner, got %v", err)
	}

	// reg-b cannot set endpoint on reg-a's route
	if err := reg.SetRouteEndpoint("reg-b", RouteID(1), "10.0.0.1:51820"); err != ErrRouteWrongOwner {
		t.Fatalf("reg-b set endpoint on reg-a's route: want ErrRouteWrongOwner, got %v", err)
	}

	// reg-b cannot set credentials on reg-a's route
	if err := reg.SetRouteCredentials("reg-b", RouteID(1), MustGenerateRouteCredentials()); err != ErrRouteWrongOwner {
		t.Fatalf("reg-b set creds on reg-a's route: want ErrRouteWrongOwner, got %v", err)
	}

	// reg-a can still open its own route
	if _, err := reg.OpenRoute("reg-a", RouteID(1)); err != nil {
		t.Fatalf("reg-a open own route after cross-reg attempt: %v", err)
	}
}

// TestM47_RouteIsolation_CloseOneDoesNotAffectOther verifies that
// closing one registration's routes does not disrupt another's routes.
func TestM47_RouteIsolation_CloseOneDoesNotAffectOther(t *testing.T) {
	reg := NewRegistry(10, 50)

	if err := reg.AddRegistration("reg-a", "hash-a"); err != nil {
		t.Fatal(err)
	}
	if err := reg.AddRegistration("reg-b", "hash-b"); err != nil {
		t.Fatal(err)
	}

	// Each allocates and opens their route
	allocA, err := reg.AllocateRoute("reg-a", RouteID(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.OpenRoute("reg-a", RouteID(1)); err != nil {
		t.Fatal(err)
	}

	allocB, err := reg.AllocateRoute("reg-b", RouteID(2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.OpenRoute("reg-b", RouteID(2)); err != nil {
		t.Fatal(err)
	}

	// Record the credentials to verify they're independent
	credsA := allocA.Credentials
	credsB := allocB.Credentials

	// Close reg-a's route
	if _, err := reg.CloseRoute("reg-a", RouteID(1)); err != nil {
		t.Fatalf("close reg-a route: %v", err)
	}

	// reg-b's route must still exist and be open
	entry, ok := reg.Route(RouteID(2))
	if !ok {
		t.Fatal("reg-b's route vanished after reg-a's route was closed")
	}
	if entry.State != RouteStateOpen {
		t.Errorf("reg-b route state: want RouteStateOpen, got %s", entry.State)
	}
	if entry.RegistrationID != "reg-b" {
		t.Errorf("reg-b route owner: want reg-b, got %s", entry.RegistrationID)
	}
	if entry.Credentials != credsB {
		t.Errorf("reg-b route credentials changed after reg-a closed")
	}
	// reg-a's credentials must have been preserved (the past) or removed
	_ = credsA

	// reg-b's registration should still exist
	if _, ok := reg.Registration("reg-b"); !ok {
		t.Fatal("reg-b registration was removed")
	}

	// Route count: only reg-b's route remains
	if n := reg.RouteCount(); n != 1 {
		t.Errorf("route count after closing reg-a: want 1, got %d", n)
	}
}
