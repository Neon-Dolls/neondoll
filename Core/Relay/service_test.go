package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Config tests ---

func TestDefaultConfig_Valid(t *testing.T) {
	t.Parallel()
	cfg := DefaultServiceConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultServiceConfig().Validate() = %v; want nil", err)
	}
}

func TestConfig_InvalidMaxRegistrations(t *testing.T) {
	t.Parallel()
	cfg := DefaultServiceConfig()
	cfg.MaxRegistrations = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for MaxRegistrations=0")
	}
}

func TestConfig_InvalidMaxRoutes(t *testing.T) {
	t.Parallel()
	cfg := DefaultServiceConfig()
	cfg.MaxRoutes = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for MaxRoutes=0")
	}
}

func TestConfig_InvalidKeepaliveInterval(t *testing.T) {
	t.Parallel()
	cfg := DefaultServiceConfig()
	cfg.KeepaliveInterval = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for KeepaliveInterval=0")
	}
}

func TestConfig_InvalidRouteTimeout(t *testing.T) {
	t.Parallel()
	cfg := DefaultServiceConfig()
	cfg.RouteTimeout = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for RouteTimeout=0")
	}
}

// --- Registry: Registration tests ---

func TestRegistry_AddRegistration(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)

	err := r.AddRegistration("reg-1", "token-hash-1")
	if err != nil {
		t.Fatalf("AddRegistration() = %v; want nil", err)
	}

	reg, ok := r.Registration("reg-1")
	if !ok {
		t.Fatal("Registration() returned false, want true")
	}
	if reg.TokenHash != "token-hash-1" {
		t.Errorf("TokenHash = %q; want %q", reg.TokenHash, "token-hash-1")
	}
	if reg.ID != "reg-1" {
		t.Errorf("ID = %q; want %q", reg.ID, "reg-1")
	}
}

func TestRegistry_AddRegistration_Duplicate(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")

	err := r.AddRegistration("reg-1", "hash-2")
	if err != ErrRegistrationExists {
		t.Fatalf("AddRegistration duplicate = %v; want ErrRegistrationExists", err)
	}
}

func TestRegistry_AddRegistration_Limit(t *testing.T) {
	t.Parallel()
	r := NewRegistry(2, defaultTestMaxRoutes)

	if err := r.AddRegistration("reg-1", "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddRegistration("reg-2", "hash-2"); err != nil {
		t.Fatal(err)
	}
	err := r.AddRegistration("reg-3", "hash-3")
	if err != ErrRegistrationLimit {
		t.Fatalf("AddRegistration past limit = %v; want ErrRegistrationLimit", err)
	}
}

func TestRegistry_RemoveRegistration(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid1 := RouteID(1)
	rid2 := RouteID(2)
	r.AllocateRoute("reg-1", rid1)
	r.AllocateRoute("reg-1", rid2)

	// remove registration — should also remove its routes
	removed := r.RemoveRegistration("reg-1")
	if removed == nil {
		t.Fatal("RemoveRegistration returned nil")
	}
	if len(removed) != 2 {
		t.Errorf("RemoveRegistration returned %d routes; want 2", len(removed))
	}

	if _, ok := r.Registration("reg-1"); ok {
		t.Error("Registration still exists after removal")
	}
	if _, ok := r.Route(rid1); ok {
		t.Error("Route still exists after registration removal")
	}
	if _, ok := r.Route(rid2); ok {
		t.Error("Route still exists after registration removal")
	}
}

func TestRegistry_RemoveRegistration_NotFound(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	if removed := r.RemoveRegistration("non-existent"); removed != nil {
		t.Errorf("RemoveRegistration(non-existent) = %v routes; want nil", len(removed))
	}
}

// --- Registry: Route tests ---

func TestRegistry_AllocateRoute(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid := RouteID(42)

	entry, err := r.AllocateRoute("reg-1", rid)
	if err != nil {
		t.Fatalf("AllocateRoute() = %v; want nil", err)
	}
	if entry == nil {
		t.Fatal("AllocateRoute() returned nil entry")
	}
	if entry.RouteID != rid {
		t.Errorf("RouteID = %v; want %v", entry.RouteID, rid)
	}
	if entry.State != RouteStateAllocated {
		t.Errorf("State = %v; want RouteStateAllocated", entry.State)
	}
	if entry.RegistrationID != "reg-1" {
		t.Errorf("RegistrationID = %q; want %q", entry.RegistrationID, "reg-1")
	}
	if entry.Credentials.Token == "" {
		t.Error("Credentials.Token is empty; should have random token")
	}
	if entry.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if entry.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestRegistry_AllocateRoute_NoRegistration(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	rid := RouteID(1)

	_, err := r.AllocateRoute("non-existent", rid)
	if err != ErrRegistrationNotFound {
		t.Fatalf("AllocateRoute() with bad reg = %v; want ErrRegistrationNotFound", err)
	}
}

func TestRegistry_AllocateRoute_Duplicate(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid := RouteID(1)
	r.AllocateRoute("reg-1", rid)

	_, err := r.AllocateRoute("reg-1", rid)
	if err != ErrRouteAlreadyExists {
		t.Fatalf("AllocateRoute duplicate = %v; want ErrRouteAlreadyExists", err)
	}
}

func TestRegistry_AllocateRoute_Limit(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, 2)
	r.AddRegistration("reg-1", "hash-1")
	r.AddRegistration("reg-2", "hash-2")

	rid1 := RouteID(1)
	rid2 := RouteID(2)
	rid3 := RouteID(3)

	r.AllocateRoute("reg-1", rid1)
	r.AllocateRoute("reg-2", rid2)

	_, err := r.AllocateRoute("reg-2", rid3)
	if err != ErrTooManyRoutes {
		t.Fatalf("AllocateRoute past limit = %v; want ErrTooManyRoutes", err)
	}
}

// --- Registry: Ownership/isolation tests ---

func TestRegistry_RouteOwnership(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	r.AddRegistration("reg-2", "hash-2")

	rid := RouteID(1)
	r.AllocateRoute("reg-1", rid)

	// reg-2 should NOT be able to open reg-1's route
	_, err := r.OpenRoute("reg-2", rid)
	if err != ErrRouteWrongOwner {
		t.Fatalf("OpenRoute cross-registration = %v; want ErrRouteWrongOwner", err)
	}

	// reg-2 should NOT be able to close reg-1's route
	_, err = r.CloseRoute("reg-2", rid)
	if err != ErrRouteWrongOwner {
		t.Fatalf("CloseRoute cross-registration = %v; want ErrRouteWrongOwner", err)
	}
}

func TestRegistry_RouteOwnershipRegistrationRoute(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid := RouteID(1)
	r.AllocateRoute("reg-1", rid)

	// Unregistered registration should fail
	_, err := r.OpenRoute("reg-99", rid)
	if err != ErrRouteWrongOwner {
		t.Fatalf("OpenRoute from unregistered = %v; want ErrRouteWrongOwner", err)
	}
}

// --- Registry: Route state transitions ---

func TestRegistry_RouteStateTransitions(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid := RouteID(1)

	// Allocate → Allocated
	entry, err := r.AllocateRoute("reg-1", rid)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != RouteStateAllocated {
		t.Errorf("after allocate: State = %v; want Allocated", entry.State)
	}

	// Open → Open
	entry, err = r.OpenRoute("reg-1", rid)
	if err != nil {
		t.Fatalf("OpenRoute() = %v; want nil", err)
	}
	if entry.State != RouteStateOpen {
		t.Errorf("after open: State = %v; want Open", entry.State)
	}

	// Close → Closed (and removed)
	entry, err = r.CloseRoute("reg-1", rid)
	if err != nil {
		t.Fatalf("CloseRoute() = %v; want nil", err)
	}
	if entry.State != RouteStateClosed {
		t.Errorf("after close: State = %v; want Closed", entry.State)
	}

	// Verify route was removed from registry
	if _, ok := r.Route(rid); ok {
		t.Error("Route still exists after close")
	}

	// Opening again should fail — route no longer exists
	_, err = r.OpenRoute("reg-1", rid)
	if err == nil {
		t.Fatal("OpenRoute after close should fail")
	}
}

func TestRegistry_RouteStateOpenAllocatedAfterAllocate(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid := RouteID(1)
	r.AllocateRoute("reg-1", rid)

	// Double-open should fail — already transitioning
	entry, err := r.OpenRoute("reg-1", rid)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != RouteStateOpen {
		t.Errorf("after second allocate: State = %v; want Allocated", entry.State)
	}

	// Third attempt on already-open state should fail
	_, err = r.OpenRoute("reg-1", rid)
	if err != ErrRouteStateTransition {
		t.Fatalf("OpenRoute on open route = %v; want ErrRouteStateTransition", err)
	}
}

func TestRegistry_CloseNonExistentRoute(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid := RouteID(1)

	_, err := r.CloseRoute("reg-1", rid)
	if err != ErrRouteNotFound {
		t.Fatalf("CloseRoute on non-existent = %v; want ErrRouteNotFound", err)
	}
}

// --- Registry: Route credentials independence ---

func TestRouteCredentials_Independence(t *testing.T) {
	t.Parallel()
	// Generate credentials in a tight loop — they must all be different
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		creds, err := GenerateRouteCredentials()
		if err != nil {
			t.Fatalf("GenerateRouteCredentials() = %v", err)
		}
		if len(creds.Token) < 32 {
			t.Fatalf("token too short: %d chars", len(creds.Token))
		}
		if seen[creds.Token] {
			t.Fatal("duplicate route credential token generated")
		}
		seen[creds.Token] = true
	}
}

func TestRouteCredentials_NotDerived(t *testing.T) {
	t.Parallel()
	// Verify that route credentials have no deterministic relationship to
	// any potential derivation source like RouteID or registration token.
	// Since generation is random, we verify: same inputs → different outputs.
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "fixed-token")
	rid := RouteID(1)

	creds1, _ := r.AllocateRoute("reg-1", rid)
	r.RemoveRegistration("reg-1")

	r.AddRegistration("reg-1", "fixed-token")
	rid2 := RouteID(1)
	creds2, _ := r.AllocateRoute("reg-1", rid2)

	if creds1 == nil || creds2 == nil {
		t.Fatal("nil credentials from AllocateRoute")
	}
	if creds1.Credentials.Token == creds2.Credentials.Token {
		t.Error("identical credentials for same inputs; route credentials must be random")
	}
}

// --- Registry: Keepalive ---

func TestRegistry_Keepalive(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")

	regBefore, _ := r.Registration("reg-1")
	before := regBefore.LastKeepalive

	time.Sleep(time.Millisecond) // ensure time passes
	err := r.Keepalive("reg-1")
	if err != nil {
		t.Fatalf("Keepalive() = %v; want nil", err)
	}

	regAfter, _ := r.Registration("reg-1")
	after := regAfter.LastKeepalive
	if !after.After(before) {
		t.Error("Keepalive did not update timestamp")
	}
}

func TestRegistry_KeepaliveNotFound(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	err := r.Keepalive("non-existent")
	if err != ErrRegistrationNotFound {
		t.Fatalf("Keepalive(non-existent) = %v; want ErrRegistrationNotFound", err)
	}
}

// --- Registry: ExpireStale ---

func TestRegistry_ExpireStale_Registration(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	r.AddRegistration("reg-2", "hash-2")
	rid := RouteID(1)
	r.AllocateRoute("reg-1", rid)

	// Expire with a very short timeout — reg-2 should stay (recently added)
	result := r.ExpireStale(0, time.Hour) // 0 reg timeout = expire all
	if len(result.ExpiredRegistrations) != 2 {
		t.Fatalf("ExpireStale(0,1h) expired %d registrations; want 2", len(result.ExpiredRegistrations))
	}

	found1, found2 := false, false
	for _, id := range result.ExpiredRegistrations {
		if id == "reg-1" {
			found1 = true
		}
		if id == "reg-2" {
			found2 = true
		}
	}
	if !found1 {
		t.Error("reg-1 should be in ExpiredRegistrations")
	}
	if !found2 {
		t.Error("reg-2 should be in ExpiredRegistrations")
	}

	// Route in reg-1 should be in RoutesExpiredViaReg (not StaleRoutes, since routeTimeout is high)
	if len(result.StaleRoutes) != 0 {
		t.Errorf("StaleRoutes = %d; want 0 (routeTimeout=1h)", len(result.StaleRoutes))
	}
	if len(result.RoutesExpiredViaReg) != 1 {
		t.Errorf("RoutesExpiredViaReg = %d; want 1", len(result.RoutesExpiredViaReg))
	}
}

func TestRegistry_ExpireStale_RouteTimeout(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	r.AddRegistration("reg-2", "hash-2")
	rid1 := RouteID(1)
	rid2 := RouteID(2)
	r.AllocateRoute("reg-1", rid1)
	r.AllocateRoute("reg-2", rid2)

	// Expire with 0 reg timeout (expire all regs + their routes)
	result := r.ExpireStale(0, time.Hour)
	if len(result.ExpiredRegistrations) != 2 {
		t.Fatalf("ExpireStale expired %d registrations; want 2", len(result.ExpiredRegistrations))
	}

	// Routes should be in RoutesExpiredViaReg (not StaleRoutes)
	if len(result.StaleRoutes) != 0 {
		t.Errorf("StaleRoutes = %d; want 0 (routeTimeout=1h)", len(result.StaleRoutes))
	}
	if len(result.RoutesExpiredViaReg) != 2 {
		t.Errorf("RoutesExpiredViaReg = %d; want 2", len(result.RoutesExpiredViaReg))
	}

	// All routes should be gone
	if r.RouteCount() != 0 {
		t.Errorf("RouteCount after expire = %d; want 0", r.RouteCount())
	}
}

func TestRegistry_ExpireStale_Nothing(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")

	result := r.ExpireStale(time.Hour, time.Hour)
	if len(result.ExpiredRegistrations) != 0 {
		t.Errorf("ExpireStale(1h,1h) expired %d registrations; want 0", len(result.ExpiredRegistrations))
	}
	if len(result.StaleRoutes) != 0 {
		t.Errorf("StaleRoutes = %d; want 0", len(result.StaleRoutes))
	}
	if len(result.RoutesExpiredViaReg) != 0 {
		t.Errorf("RoutesExpiredViaReg = %d; want 0", len(result.RoutesExpiredViaReg))
	}
}

func TestRegistry_ExpireStale_RouteOwnershipAfterExpiry(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid := RouteID(1)
	r.AllocateRoute("reg-1", rid)

	// Force-expire registration
	result := r.ExpireStale(0, time.Hour)
	if len(result.ExpiredRegistrations) != 1 {
		t.Fatalf("expired %d registrations; want 1", len(result.ExpiredRegistrations))
	}

	// Route should be in RoutesExpiredViaReg
	if len(result.RoutesExpiredViaReg) != 1 {
		t.Errorf("RoutesExpiredViaReg = %d; want 1", len(result.RoutesExpiredViaReg))
	}

	// Route count should be 0
	if r.RouteCount() != 0 {
		t.Errorf("RouteCount = %d; want 0 after registration expiry", r.RouteCount())
	}

	// Route should not be accessible
	_, err := r.OpenRoute("reg-1", rid)
	if err != ErrRouteNotFound {
		t.Errorf("OpenRoute after expiry: got %v; want ErrRouteNotFound", err)
	}
}

// --- Registry: Concurrency tests ---

func TestRegistry_ConcurrentAddRemove(t *testing.T) {
	t.Parallel()
	r := NewRegistry(1000, 1000)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			regID := RegistrationID(fmt.Sprintf("reg-%d", id))
			err := r.AddRegistration(regID, fmt.Sprintf("hash-%d", id))
			if err != nil && err != ErrRegistrationLimit {
				t.Errorf("AddRegistration(%q) = %v", regID, err)
			}
		}(i)
	}
	wg.Wait()

	if r.RegistrationCount() != 100 {
		t.Errorf("RegistrationCount = %d; want 100", r.RegistrationCount())
	}
}

func TestRegistry_ConcurrentRouteAllocation(t *testing.T) {
	t.Parallel()
	r := NewRegistry(10, 1000)
	r.AddRegistration("reg-1", "hash-1")

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rid := RouteID(id)
			_, err := r.AllocateRoute("reg-1", rid)
			if err != nil && err != ErrTooManyRoutes && err != ErrRouteAlreadyExists {
				errs <- fmt.Errorf("AllocateRoute: %w", err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	count := r.RouteCount()
	if count < 1 || count > 50 {
		t.Errorf("RouteCount = %d; expected between 1 and 50", count)
	}
}

// --- Metrics tests ---

func TestMetrics_InitialZero(t *testing.T) {
	t.Parallel()
	m := NewMetrics()

	fields := []struct {
		name string
		val  int64
	}{
		{"RoutesCreated", m.RoutesCreated()},
		{"RoutesClosed", m.RoutesClosed()},
		{"RegistrationsAdded", m.RegistrationsAdded()},
		{"ControlMessages", m.ControlMessages()},
	}
	for _, f := range fields {
		if f.val != 0 {
			t.Errorf("%s = %d; want 0", f.name, f.val)
		}
	}
}

func TestMetrics_Incrementers(t *testing.T) {
	t.Parallel()
	m := NewMetrics()

	m.incRoutesCreated()
	m.incRoutesCreated()
	m.incRoutesClosed()

	if m.RoutesCreated() != 2 {
		t.Errorf("RoutesCreated = %d; want 2", m.RoutesCreated())
	}
	if m.RoutesClosed() != 1 {
		t.Errorf("RoutesClosed = %d; want 1", m.RoutesClosed())
	}
}

func TestMetrics_Snapshot(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	m.incRoutesCreated()
	m.incControlMessages()

	snap := m.Snapshot()
	if snap["routes_created"] != 1 {
		t.Errorf("snapshot routes_created = %d; want 1", snap["routes_created"])
	}
	if snap["control_messages"] != 1 {
		t.Errorf("snapshot control_messages = %d; want 1", snap["control_messages"])
	}
	// All keys present
	expected := []string{
		"routes_created", "routes_closed", "routes_failed",
		"registrations_added", "registrations_dropped",
		"control_messages", "control_errors",
		"liveness_checks", "stale_routes_expired",
	}
	for _, k := range expected {
		if _, ok := snap[k]; !ok {
			t.Errorf("snapshot missing key %q", k)
		}
	}
}

func TestMetrics_ConcurrentSafety(t *testing.T) {
	t.Parallel()
	m := NewMetrics()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.incRoutesCreated()
			m.incControlMessages()
			_ = m.Snapshot()
		}()
	}
	wg.Wait()
	if m.RoutesCreated() != 100 {
		t.Errorf("RoutesCreated = %d; want 100", m.RoutesCreated())
	}
}

// --- Service lifecycle tests ---

func TestService_NewServiceValidConfig(t *testing.T) {
	t.Parallel()
	svc, err := NewService(DefaultServiceConfig())
	if err != nil {
		t.Fatalf("NewService() = %v; want nil", err)
	}
	if svc.State() != ServiceStateStopped {
		t.Errorf("State after NewService = %v; want Stopped", svc.State())
	}
}

func TestService_NewServiceInvalidConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultServiceConfig()
	cfg.MaxRegistrations = 0
	_, err := NewService(cfg)
	if err == nil {
		t.Fatal("NewService with MaxRegistrations=0 should fail")
	}
}

func TestService_StartAndShutdown(t *testing.T) {
	t.Parallel()
	svc, err := NewService(DefaultServiceConfig())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start() = %v; want nil", err)
	}
	if svc.State() != ServiceStateRunning {
		t.Errorf("State after Start = %v; want Running", svc.State())
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() = %v; want nil", err)
	}
	if svc.State() != ServiceStateStoppedClean {
		t.Errorf("State after Shutdown = %v; want StoppedClean", svc.State())
	}
}

func TestService_ShutdownWithoutStart(t *testing.T) {
	t.Parallel()
	svc, _ := NewService(DefaultServiceConfig())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := svc.Shutdown(shutdownCtx)
	if err == nil {
		t.Fatal("Shutdown without Start should fail")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error message = %q; should contain 'not running'", err.Error())
	}
}

func TestService_DoubleStart(t *testing.T) {
	t.Parallel()
	svc, _ := NewService(DefaultServiceConfig())
	ctx := context.Background()
	svc.Start(ctx)

	err := svc.Start(ctx)
	if err == nil {
		t.Fatal("Start on running service should fail")
	}
	if !strings.Contains(err.Error(), "already started") {
		t.Errorf("error message = %q; should contain 'already started'", err.Error())
	}

	// Clean up
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	svc.Shutdown(shutdownCtx)
}

func TestService_ShutdownExpiresAllRoutes(t *testing.T) {
	t.Parallel()
	svc, _ := NewService(DefaultServiceConfig())
	ctx := context.Background()
	svc.Start(ctx)

	reg := svc.Registry()
	reg.AddRegistration("test-reg", "hash")
	rid := RouteID(1)
	reg.AllocateRoute("test-reg", rid)

	if reg.RouteCount() != 1 {
		t.Fatalf("RouteCount before shutdown = %d; want 1", reg.RouteCount())
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	svc.Shutdown(shutdownCtx)

	if reg.RegistrationCount() != 0 {
		t.Errorf("RegistrationCount after shutdown = %d; want 0", reg.RegistrationCount())
	}
	if reg.RouteCount() != 0 {
		t.Errorf("RouteCount after shutdown = %d; want 0", reg.RouteCount())
	}
}

// --- Service diagnostics tests ---

func TestService_Diagnostics(t *testing.T) {
	t.Parallel()
	svc, _ := NewService(DefaultServiceConfig())
	svc.Start(context.Background())

	// Add a registration and route to make diagnostics interesting
	reg := svc.Registry()
	reg.AddRegistration("diag-reg", "hash")
	rid := RouteID(1)
	reg.AllocateRoute("diag-reg", rid)
	svc.Metrics().incRoutesCreated() // service-level tracking (M4.3+ wired)

	d := svc.Diagnostics()
	if d["service_state"] != "running" {
		t.Errorf("service_state = %q; want 'running'", d["service_state"])
	}
	if d["active_registrations"] != 1 {
		t.Errorf("active_registrations = %v; want 1", d["active_registrations"])
	}
	if d["active_routes"] != 1 {
		t.Errorf("active_routes = %v; want 1", d["active_routes"])
	}
	if d["routes_created"] != int64(1) {
		t.Errorf("routes_created = %v; want 1", d["routes_created"])
	}

	// Verify snapshot has all expected keys
	for _, k := range []string{"routes_created", "control_messages", "service_state", "active_registrations", "active_routes"} {
		if _, ok := d[k]; !ok {
			t.Errorf("Diagnostics missing key %q", k)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.Shutdown(shutdownCtx)
}

// --- Integration: Registry + Service basic flow ---

func TestService_RegistrationAndRouteLifecycle(t *testing.T) {
	t.Parallel()
	svc, _ := NewService(DefaultServiceConfig())
	ctx := context.Background()
	svc.Start(ctx)

	reg := svc.Registry()

	// Register
	err := reg.AddRegistration("client-1", hex.EncodeToString([]byte("token-hash")))
	if err != nil {
		t.Fatalf("AddRegistration: %v", err)
	}

	// Allocate routes
	rid1 := RouteID(10)
	rid2 := RouteID(20)

	route1, err := reg.AllocateRoute("client-1", rid1)
	if err != nil {
		t.Fatalf("AllocateRoute 1: %v", err)
	}
	if route1.State != RouteStateAllocated {
		t.Errorf("route1.State = %v; want Allocated", route1.State)
	}

	route2, err := reg.AllocateRoute("client-1", rid2)
	if err != nil {
		t.Fatalf("AllocateRoute 2: %v", err)
	}
	_ = route2

	// Open routes
	for i, rid := range []RouteID{rid1, rid2} {
		entry, err := reg.OpenRoute("client-1", rid)
		if err != nil {
			t.Fatalf("OpenRoute %d: %v", i, err)
		}
		if entry.State != RouteStateOpen {
			t.Errorf("route %d State = %v; want Open", i, entry.State)
		}
	}

	// Registration should show both routes
	client, ok := reg.Registration("client-1")
	if !ok {
		t.Fatal("Registration not found")
	}
	if rids := client.RouteIDs(); len(rids) != 2 {
		t.Errorf("RouteIDs = %v; want 2 routes", rids)
	}

	// Close one route
	_, err = reg.CloseRoute("client-1", rid1)
	if err != nil {
		t.Fatalf("CloseRoute: %v", err)
	}

	// Route count should be 1 now
	if reg.RouteCount() != 1 {
		t.Errorf("RouteCount = %d; want 1", reg.RouteCount())
	}

	// Clean up
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	svc.Shutdown(shutdownCtx)

	if reg.RegistrationCount() != 0 {
		t.Errorf("RegistrationCount after shutdown = %d; want 0", reg.RegistrationCount())
	}
}

// --- RegistrationID and RouteID ---

func TestRegistration_RouteIDs_Sort(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")

	rid1 := RouteID(3)
	rid2 := RouteID(1)
	rid3 := RouteID(2)

	r.AllocateRoute("reg-1", rid1)
	r.AllocateRoute("reg-1", rid2)
	r.AllocateRoute("reg-1", rid3)

	reg, ok := r.Registration("reg-1")
	if !ok {
		t.Fatal("Registration not found")
	}

	ids := reg.RouteIDs()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	if len(ids) != 3 {
		t.Fatalf("RouteIDs length = %d; want 3", len(ids))
	}
	if ids[0] != rid2 || ids[1] != rid3 || ids[2] != rid1 {
		t.Errorf("RouteIDs = %v; want sorted", ids)
	}
}

// --- GenerateRouteCredentials ---

func TestGenerateRouteCredentials_Deterministic(t *testing.T) {
	// This test validates that GenerateRouteCredentials does NOT produce
	// deterministically derived tokens — it produces random ones. We verify
	// by generating twice and confirming they differ.
	t.Parallel()
	c1, err := GenerateRouteCredentials()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := GenerateRouteCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if c1.Token == c2.Token {
		t.Error("two consecutive calls produced identical tokens; route credentials must be random")
	}
}

func TestGenerateRouteCredentials_Length(t *testing.T) {
	t.Parallel()
	creds, err := GenerateRouteCredentials()
	if err != nil {
		t.Fatal(err)
	}
	// 32 bytes → 64 hex chars
	if len(creds.Token) != 64 {
		t.Errorf("Token length = %d; want 64 (32 bytes hex-encoded)", len(creds.Token))
	}
}

func TestMustGenerateRouteCredentials(t *testing.T) {
	t.Parallel()
	creds := MustGenerateRouteCredentials()
	if creds.Token == "" {
		t.Fatal("MustGenerateRouteCredentials returned empty token")
	}
}

// --- Registry: token hash tracking ---

func TestRegistry_TokenHashStored(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)

	token := "my-secret-token"
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])

	r.AddRegistration("reg-1", tokenHash)

	reg, ok := r.Registration("reg-1")
	if !ok {
		t.Fatal("Registration not found")
	}
	if reg.TokenHash != tokenHash {
		t.Errorf("TokenHash = %q; want %q", reg.TokenHash, tokenHash)
	}
}

// --- RouteState string names ---

func TestRouteState_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state RouteState
		want  string
	}{
		{RouteStateAllocated, "allocated"},
		{RouteStateOpening, "opening"},
		{RouteStateOpen, "open"},
		{RouteStateClosing, "closing"},
		{RouteStateClosed, "closed"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("RouteState(%d).String() = %q; want %q", int(tt.state), got, tt.want)
		}
	}
}

func TestRouteState_String_Unknown(t *testing.T) {
	t.Parallel()
	s := RouteState(99).String()
	if !strings.HasPrefix(s, "RouteState(") {
		t.Errorf("unknown state string = %q; want RouteState(...)", s)
	}
}

// --- ServiceState string names ---

func TestServiceState_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state ServiceState
		want  string
	}{
		{ServiceStateStopped, "stopped"},
		{ServiceStateStarting, "starting"},
		{ServiceStateRunning, "running"},
		{ServiceStateStopping, "stopping"},
		{ServiceStateStoppedClean, "stopped_clean"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("ServiceState(%d).String() = %q; want %q", int(tt.state), got, tt.want)
		}
	}
}

// --- Edge cases ---

func TestRegistry_RouteCredentialsFromEntry(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	r.AddRegistration("reg-1", "hash-1")
	rid := RouteID(1)
	r.AllocateRoute("reg-1", rid)

	creds, err := r.RouteCredentialsFromEntry(rid)
	if err != nil {
		t.Fatalf("RouteCredentialsFromEntry() = %v", err)
	}
	if creds.Token == "" {
		t.Error("credentials token is empty")
	}
}

func TestRegistry_RouteCredentialsFromEntry_NotFound(t *testing.T) {
	t.Parallel()
	r := NewRegistry(defaultTestMaxReg, defaultTestMaxRoutes)
	rid := RouteID(999)
	_, err := r.RouteCredentialsFromEntry(rid)
	if err != ErrRouteNotFound {
		t.Fatalf("RouteCredentialsFromEntry(non-existent) = %v; want ErrRouteNotFound", err)
	}
}

// --- Service restart lifecycle ---

func TestService_RestartLifecycle(t *testing.T) {
	t.Parallel()
	svc, err := NewService(DefaultServiceConfig())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	goroutinesBefore := runtime.NumGoroutine()

	// First start
	ctx1 := context.Background()
	if err := svc.Start(ctx1); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if s := svc.State(); s != ServiceStateRunning {
		t.Errorf("state after first Start = %s; want running", s)
	}

	// First shutdown
	shutdownCtx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()
	if err := svc.Shutdown(shutdownCtx1); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if s := svc.State(); s != ServiceStateStoppedClean {
		t.Errorf("state after first Shutdown = %s; want stopped_clean", s)
	}

	// Second start — must work after clean shutdown
	ctx2 := context.Background()
	if err := svc.Start(ctx2); err != nil {
		t.Fatalf("second Start: %v (restart failed)", err)
	}
	if s := svc.State(); s != ServiceStateRunning {
		t.Errorf("state after second Start = %s; want running", s)
	}

	// Second shutdown
	shutdownCtx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := svc.Shutdown(shutdownCtx2); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if s := svc.State(); s != ServiceStateStoppedClean {
		t.Errorf("state after second Shutdown = %s; want stopped_clean", s)
	}

	// Check for goroutine leak (allow small baseline fluctuation)
	time.Sleep(50 * time.Millisecond)
	if delta := runtime.NumGoroutine() - goroutinesBefore; delta > 2 {
		t.Errorf("possible goroutine leak: %d goroutines above baseline after restart cycle", delta)
	}
}

// --- Concurrency: service start/shutdown from multiple goroutines ---

func TestService_ConcurrentStartShutdown(t *testing.T) {
	t.Parallel()
	var started atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc, err := NewService(DefaultServiceConfig())
			if err != nil {
				return
			}
			if err := svc.Start(context.Background()); err != nil {
				return
			}
			started.Add(1)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			svc.Shutdown(shutdownCtx)
		}()
	}
	wg.Wait()

	if started.Load() != 10 {
		t.Errorf("successful starts = %d; want 10", started.Load())
	}
}

// --- config.go helpers ---

const defaultTestMaxReg = 256
const defaultTestMaxRoutes = 1024
