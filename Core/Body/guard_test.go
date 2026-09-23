package body

import (
	"errors"
	"testing"
)

// ── Spy Body seam ──────────────────────────────────────────────────────

// spyBody is a deterministic fake Body that records every invocation.
// It is used to prove that denied requests never reach invocation and
// that eligible requests are not invoked in M3.
type spyBody struct {
	id       BodyID
	caps     *CapabilityRegistry
	executed int
}

func newSpyBody(id BodyID) *spyBody {
	return &spyBody{
		id:   id,
		caps: NewCapabilityRegistry(),
	}
}

func (s *spyBody) ID() BodyID { return s.id }

func (s *spyBody) Kind() BodyKind { return BodyKindLocal }

func (s *spyBody) Name() string { return string(s.id) }

func (s *spyBody) Describe() []Capability { return s.caps.All() }

func (s *spyBody) ResolveCapability(capID string, operation string) error {
	return s.caps.Resolve(capID, operation)
}

func (s *spyBody) RegisterCapability(cap Capability) error {
	return s.caps.Register(cap)
}

// Execute records the call. It must never be reached in M3.
func (s *spyBody) Execute(req ExecutionRequest) (*ExecutionResult, error) {
	s.executed++
	return &ExecutionResult{Status: StatusCompleted, Output: "spy"}, nil
}

// spyEvaluator is an authority evaluator whose decision is fixed.
type spyEvaluator struct {
	decision AuthorityDecision
	reason   string
	calls    int
}

func (e *spyEvaluator) Evaluate(req AuthorityRequest) AuthorityResult {
	e.calls++
	return AuthorityResult{Decision: e.decision, Reason: e.reason}
}

// ── Phase 3: Guarded Execution Boundary ────────────────────────────────

// TestGuard_DeniedNeverReachesInvocation proves that a denied request stops
// at the authority boundary — Body.Execute is never called.
func TestGuard_DeniedNeverReachesInvocation(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	// Explicit deny.
	ev := &spyEvaluator{decision: AuthorityDeny, reason: "explicit_deny"}
	guard := NewGuard(registry, ev)

	res, err := guard.Authorize(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       "spy::one",
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v, want nil (denial is an outcome, not an error)", err)
	}
	if !res.Denied || res.Eligible {
		t.Fatalf("Authorize() = %+v, want denied and not eligible", res)
	}
	if res.Reason != "explicit_deny" {
		t.Errorf("Authorize().Reason = %q, want %q", res.Reason, "explicit_deny")
	}
	if spy.executed != 0 {
		t.Fatalf("denied request reached invocation: executed = %d, want 0", spy.executed)
	}
}

// TestGuard_EligibleNotInvokedInM3 proves that an allowed request is marked
// eligible but Body.Execute is NOT called in M3.
func TestGuard_EligibleNotInvokedInM3(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	ev := &spyEvaluator{decision: AuthorityAllow}
	guard := NewGuard(registry, ev)

	res, err := guard.Authorize(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       "spy::one",
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v, want nil", err)
	}
	if !res.Eligible || res.Denied {
		t.Fatalf("Authorize() = %+v, want eligible and not denied", res)
	}
	if spy.executed != 0 {
		t.Fatalf("eligible request was invoked in M3: executed = %d, want 0", spy.executed)
	}
}

// TestGuard_UnsupportedNeverAuthorityApproved proves unsupported capability
// and unsupported operation fail resolution BEFORE authority — the result
// is the M2 error, not an authority-approved execution, and the evaluator
// is never consulted.
func TestGuard_UnsupportedNeverAuthorityApproved(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	// Evaluator that would say yes — it must never even be asked.
	ev := &spyEvaluator{decision: AuthorityAllow}
	guard := NewGuard(registry, ev)

	// Unsupported capability.
	_, err := guard.Authorize(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       "spy::one",
		Capability: "no.such.capability",
		Operation:  "read",
	})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("unsupported capability: err = %v, want ErrUnsupportedCapability", err)
	}

	// Unsupported operation on a known capability.
	_, err = guard.Authorize(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       "spy::one",
		Capability: "runtime.info",
		Operation:  "write",
	})
	if !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("unsupported operation: err = %v, want ErrUnsupportedOperation", err)
	}

	if ev.calls != 0 {
		t.Fatalf("evaluator consulted %d times for unsupported requests, want 0", ev.calls)
	}
	if spy.executed != 0 {
		t.Fatalf("unsupported request reached invocation: executed = %d, want 0", spy.executed)
	}
}

// TestGuard_UnavailableNeverAuthorityApproved proves an unavailable
// capability fails resolution BEFORE authority — ErrUnavailable is
// returned and the evaluator is never consulted.
func TestGuard_UnavailableNeverAuthorityApproved(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: false})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	ev := &spyEvaluator{decision: AuthorityAllow}
	guard := NewGuard(registry, ev)

	_, err := guard.Authorize(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       "spy::one",
		Capability: "runtime.info",
		Operation:  "read",
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable capability: err = %v, want ErrUnavailable", err)
	}
	if ev.calls != 0 {
		t.Fatalf("evaluator consulted %d times for unavailable request, want 0", ev.calls)
	}
}

// TestGuard_SameEntryHandlesAllowAndDeny proves the guarded entry point is
// the same for both outcomes: one Guard instance routes the same request
// shape to allow or deny depending on authority.
func TestGuard_SameEntryHandlesAllowAndDeny(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	guard := NewGuard(registry, NewRuleEvaluator(
		AuthorityRule{
			Doll:       "allowed-doll",
			Body:       "spy::one",
			Capability: "runtime.info",
			Operation:  "read",
			Decision:   AuthorityAllow,
		},
	))

	// Allow for the explicitly permitted Doll.
	res, err := guard.Authorize(AuthorityRequest{
		Doll:       "allowed-doll",
		Body:       "spy::one",
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err != nil {
		t.Fatalf("allow path: err = %v, want nil", err)
	}
	if !res.Eligible || res.Denied {
		t.Fatalf("allow path: %+v, want eligible", res)
	}

	// Deny for any other Doll via the same entry point.
	res, err = guard.Authorize(AuthorityRequest{
		Doll:       "unlisted-doll",
		Body:       "spy::one",
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err != nil {
		t.Fatalf("deny path: err = %v, want nil", err)
	}
	if !res.Denied || res.Eligible {
		t.Fatalf("deny path: %+v, want denied", res)
	}

	if spy.executed != 0 {
		t.Fatalf("requests reached invocation: executed = %d, want 0", spy.executed)
	}
}

// TestGuard_UnknownBodyIsResolutionFailure proves a request for an
// unregistered Body is a resolution failure, not an authority decision.
func TestGuard_UnknownBodyIsResolutionFailure(t *testing.T) {
	registry := NewRegistry()
	guard := NewGuard(registry, &spyEvaluator{decision: AuthorityAllow})

	_, err := guard.Authorize(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       "ghost::body",
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err == nil {
		t.Fatal("Authorize(unknown body) = nil, want error")
	}
}

// TestGuard_M2ErrorsAndDenialDistinct proves that unsupported capability,
// unsupported operation, unavailable, and denied remain four distinct
// outcomes: the first three are errors, denial is an outcome result.
func TestGuard_M2ErrorsAndDenialDistinct(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	_ = spy.caps.Register(Capability{ID: "test:blocked", Operations: []string{"do-thing"}, Available: false})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	guard := NewGuard(registry, NewRuleEvaluator()) // default deny

	// Unsupported capability → error.
	_, err := guard.Authorize(AuthorityRequest{Doll: "d", Body: "spy::one", Capability: "no.such", Operation: "read"})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Errorf("unsupported capability: err = %v, want ErrUnsupportedCapability", err)
	}

	// Unsupported operation → error.
	_, err = guard.Authorize(AuthorityRequest{Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "write"})
	if !errors.Is(err, ErrUnsupportedOperation) {
		t.Errorf("unsupported operation: err = %v, want ErrUnsupportedOperation", err)
	}

	// Unavailable → error.
	_, err = guard.Authorize(AuthorityRequest{Doll: "d", Body: "spy::one", Capability: "test:blocked", Operation: "do-thing"})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("unavailable: err = %v, want ErrUnavailable", err)
	}

	// Denied → outcome, not error.
	res, err := guard.Authorize(AuthorityRequest{Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "read"})
	if err != nil {
		t.Errorf("denial: err = %v, want nil (denial is an outcome)", err)
	}
	if !res.Denied {
		t.Errorf("denial: res = %+v, want denied", res)
	}
}
