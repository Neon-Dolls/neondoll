package body

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// ── Spy Body seam ──────────────────────────────────────────────────────

// spyBody is a deterministic fake Body that records every invocation.
type spyBody struct {
	id       BodyID
	caps     *CapabilityRegistry
	executed int
	lastReq  ExecutionRequest // M4: captured for argument-equivalence tests
	failErr  error            // M4: when non-nil, Execute returns this error
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

// Execute records the call and optionally returns the configured error.
func (s *spyBody) Execute(req ExecutionRequest) (*ExecutionResult, error) {
	s.executed++
	s.lastReq = req
	if s.failErr != nil {
		return nil, s.failErr
	}
	return &ExecutionResult{Status: StatusSuccess, Output: "spy"}, nil
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

// recordingEvaluator records the AuthorityRequest it evaluated.
type recordingEvaluator struct {
	inner   AuthorityEvaluator
	lastReq AuthorityRequest
}

func (r *recordingEvaluator) Evaluate(req AuthorityRequest) AuthorityResult {
	r.lastReq = req
	return r.inner.Evaluate(req)
}

// ── Phase 3: Guarded Execution Boundary (M3 — Authorize) ──────────────

func TestGuard_DeniedNeverReachesInvocation(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

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
		t.Fatalf("eligible request was invoked via Authorize: executed = %d, want 0", spy.executed)
	}
}

func TestGuard_UnsupportedNeverAuthorityApproved(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	ev := &spyEvaluator{decision: AuthorityAllow}
	guard := NewGuard(registry, ev)

	_, err := guard.Authorize(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       "spy::one",
		Capability: "no.such.capability",
		Operation:  "read",
	})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("unsupported capability: err = %v, want ErrUnsupportedCapability", err)
	}

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

func TestGuard_M2ErrorsAndDenialDistinct(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	_ = spy.caps.Register(Capability{ID: "test:blocked", Operations: []string{"do-thing"}, Available: false})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	guard := NewGuard(registry, NewRuleEvaluator()) // default deny

	_, err := guard.Authorize(AuthorityRequest{Doll: "d", Body: "spy::one", Capability: "no.such", Operation: "read"})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Errorf("unsupported capability: err = %v, want ErrUnsupportedCapability", err)
	}

	_, err = guard.Authorize(AuthorityRequest{Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "write"})
	if !errors.Is(err, ErrUnsupportedOperation) {
		t.Errorf("unsupported operation: err = %v, want ErrUnsupportedOperation", err)
	}

	_, err = guard.Authorize(AuthorityRequest{Doll: "d", Body: "spy::one", Capability: "test:blocked", Operation: "do-thing"})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("unavailable: err = %v, want ErrUnavailable", err)
	}

	res, err := guard.Authorize(AuthorityRequest{Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "read"})
	if err != nil {
		t.Errorf("denial: err = %v, want nil (denial is an outcome)", err)
	}
	if !res.Denied {
		t.Errorf("denial: res = %+v, want denied", res)
	}
}

// ── Phase 3: Guard.Execute — M4 Production Execution Path ─────────────

func TestGuardExecute_InvalidZeroInvocation(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}
	ev := &spyEvaluator{decision: AuthorityAllow}
	guard := NewGuard(registry, ev)

	tests := []struct {
		name string
		req  ExecutionRequest
	}{
		{"missing-execution-id", ExecutionRequest{Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "read"}},
		{"missing-doll", ExecutionRequest{ExecutionID: "e1", Body: "spy::one", Capability: "runtime.info", Operation: "read"}},
		{"missing-body", ExecutionRequest{ExecutionID: "e2", Doll: "d", Capability: "runtime.info", Operation: "read"}},
		{"missing-capability", ExecutionRequest{ExecutionID: "e3", Doll: "d", Body: "spy::one", Operation: "read"}},
		{"missing-operation", ExecutionRequest{ExecutionID: "e4", Doll: "d", Body: "spy::one", Capability: "runtime.info"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := guard.Execute(tc.req)
			if res.Status != StatusInvalid {
				t.Errorf("status = %q, want %q", res.Status, StatusInvalid)
			}
			if res.ErrorCode != "invalid_request" {
				t.Errorf("error_code = %q, want %q", res.ErrorCode, "invalid_request")
			}
			if res.ExecutionID != tc.req.ExecutionID {
				t.Errorf("execution_id = %q, want %q", res.ExecutionID, tc.req.ExecutionID)
			}
		})
	}
	if spy.executed != 0 {
		t.Fatalf("invalid requests reached invocation: executed = %d, want 0", spy.executed)
	}
	if ev.calls != 0 {
		t.Fatalf("evaluator consulted %d times for invalid requests, want 0", ev.calls)
	}
}

func TestGuardExecute_DeniedZeroInvocation(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	ev := &spyEvaluator{decision: AuthorityDeny, reason: "explicit_deny"}
	guard := NewGuard(registry, ev)

	res := guard.Execute(ExecutionRequest{
		ExecutionID: "exec-x",
		Doll:        "neko-chan",
		Body:        "spy::one",
		Capability:  "runtime.info",
		Operation:   "read",
	})
	if res.Status != StatusDenied {
		t.Errorf("status = %q, want %q", res.Status, StatusDenied)
	}
	if res.ErrorCode != "explicit_deny" {
		t.Errorf("error_code = %q, want %q", res.ErrorCode, "explicit_deny")
	}
	if res.ExecutionID != "exec-x" {
		t.Errorf("execution_id = %q, want %q", res.ExecutionID, "exec-x")
	}
	if spy.executed != 0 {
		t.Fatalf("denied request reached invocation: executed = %d, want 0", spy.executed)
	}
}

func TestGuardExecute_UnsupportedZeroInvocation(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	ev := &spyEvaluator{decision: AuthorityAllow}
	guard := NewGuard(registry, ev)

	tests := []struct {
		name     string
		req      ExecutionRequest
		want     ExecutionStatus
		wantCode string
	}{
		{"unknown-capability", ExecutionRequest{ExecutionID: "e1", Doll: "d", Body: "spy::one", Capability: "no.such", Operation: "read"}, StatusUnsupported, "unsupported_capability"},
		{"unknown-operation", ExecutionRequest{ExecutionID: "e2", Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "write"}, StatusUnsupported, "unsupported_operation"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := guard.Execute(tc.req)
			if res.Status != tc.want {
				t.Errorf("status = %q, want %q", res.Status, tc.want)
			}
			if res.ErrorCode != tc.wantCode {
				t.Errorf("error_code = %q, want %q", res.ErrorCode, tc.wantCode)
			}
			if res.ExecutionID != tc.req.ExecutionID {
				t.Errorf("execution_id = %q, want %q", res.ExecutionID, tc.req.ExecutionID)
			}
		})
	}
	if spy.executed != 0 {
		t.Fatalf("unsupported requests reached invocation: executed = %d, want 0", spy.executed)
	}
	if ev.calls != 0 {
		t.Fatalf("evaluator consulted %d times for unsupported requests, want 0", ev.calls)
	}
}

func TestGuardExecute_UnavailableZeroInvocation(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: false})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	ev := &spyEvaluator{decision: AuthorityAllow}
	guard := NewGuard(registry, ev)

	res := guard.Execute(ExecutionRequest{
		ExecutionID: "exec-u",
		Doll:        "d",
		Body:        "spy::one",
		Capability:  "runtime.info",
		Operation:   "read",
	})
	if res.Status != StatusUnavailable {
		t.Errorf("status = %q, want %q", res.Status, StatusUnavailable)
	}
	if res.ErrorCode != "unavailable" {
		t.Errorf("error_code = %q, want %q", res.ErrorCode, "unavailable")
	}
	if res.ExecutionID != "exec-u" {
		t.Errorf("execution_id = %q, want %q", res.ExecutionID, "exec-u")
	}
	if spy.executed != 0 {
		t.Fatalf("unavailable request reached invocation: executed = %d, want 0", spy.executed)
	}
	if ev.calls != 0 {
		t.Fatalf("evaluator consulted %d times for unavailable request, want 0", ev.calls)
	}
}

func TestGuardExecute_BodyNotFoundIsInvalid(t *testing.T) {
	registry := NewRegistry()
	guard := NewGuard(registry, &spyEvaluator{decision: AuthorityAllow})

	res := guard.Execute(ExecutionRequest{
		ExecutionID: "exec-b",
		Doll:        "d",
		Body:        "ghost::body",
		Capability:  "runtime.info",
		Operation:   "read",
	})
	if res.Status != StatusInvalid {
		t.Errorf("status = %q, want %q", res.Status, StatusInvalid)
	}
	if res.ErrorCode != "body_not_found" {
		t.Errorf("error_code = %q, want %q", res.ErrorCode, "body_not_found")
	}
	if res.ExecutionID != "exec-b" {
		t.Errorf("execution_id = %q, want %q", res.ExecutionID, "exec-b")
	}
}

func TestGuardExecute_AllowInvokesExactlyOnce(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	guard := NewGuard(registry, &spyEvaluator{decision: AuthorityAllow})

	res := guard.Execute(ExecutionRequest{
		ExecutionID: "exec-y",
		Doll:        "neko-chan",
		Body:        "spy::one",
		Capability:  "runtime.info",
		Operation:   "read",
	})
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", res.Status, StatusSuccess)
	}
	if res.Output != "spy" {
		t.Errorf("output = %q, want %q", res.Output, "spy")
	}
	if res.ExecutionID != "exec-y" {
		t.Errorf("execution_id = %q, want %q", res.ExecutionID, "exec-y")
	}
	if spy.executed != 1 {
		t.Fatalf("execute count = %d, want 1", spy.executed)
	}
}

func TestGuardExecute_DefaultDenyRule(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	guard := NewGuard(registry, NewRuleEvaluator()) // default deny

	res := guard.Execute(ExecutionRequest{
		ExecutionID: "exec-d",
		Doll:        "d",
		Body:        "spy::one",
		Capability:  "runtime.info",
		Operation:   "read",
	})
	if res.Status != StatusDenied {
		t.Errorf("status = %q, want %q", res.Status, StatusDenied)
	}
	if res.ErrorCode != "no_rule" {
		t.Errorf("error_code = %q, want %q", res.ErrorCode, "no_rule")
	}
	if res.ExecutionID != "exec-d" {
		t.Errorf("execution_id = %q, want %q", res.ExecutionID, "exec-d")
	}
	if spy.executed != 0 {
		t.Fatalf("default-denied request reached invocation: executed = %d, want 0", spy.executed)
	}
}

func TestGuardExecute_InvocationErrorFailed(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	// Configure spy to fail on invocation.
	spy.failErr = fmt.Errorf("internal error")

	guard := NewGuard(registry, &spyEvaluator{decision: AuthorityAllow})

	res := guard.Execute(ExecutionRequest{
		ExecutionID: "exec-f",
		Doll:        "d",
		Body:        "spy::one",
		Capability:  "runtime.info",
		Operation:   "read",
	})
	if res.Status != StatusFailed {
		t.Errorf("status = %q, want %q", res.Status, StatusFailed)
	}
	if res.ErrorCode != "invocation_failed" {
		t.Errorf("error_code = %q, want %q", res.ErrorCode, "invocation_failed")
	}
	if res.ExecutionID != "exec-f" {
		t.Errorf("execution_id = %q, want %q", res.ExecutionID, "exec-f")
	}
	if spy.executed != 1 {
		t.Fatalf("Body invoked %d times, want 1 (invocation counts even when it fails)", spy.executed)
	}
}

func TestGuardExecute_PreservesExecutionID(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	guard := NewGuard(registry, &spyEvaluator{decision: AuthorityAllow})

	// Success path.
	res := guard.Execute(ExecutionRequest{ExecutionID: "s1", Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "read"})
	if res.ExecutionID != "s1" {
		t.Errorf("success: execution_id = %q, want %q", res.ExecutionID, "s1")
	}

	// Denied path (different evaluator).
	denyGuard := NewGuard(registry, &spyEvaluator{decision: AuthorityDeny})
	res = denyGuard.Execute(ExecutionRequest{ExecutionID: "d1", Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "read"})
	if res.ExecutionID != "d1" {
		t.Errorf("denied: execution_id = %q, want %q", res.ExecutionID, "d1")
	}
}

func TestGuardExecute_ArgumentsAuthorizedEqualsArgumentsExecuted(t *testing.T) {
	registry := NewRegistry()
	spy := newSpyBody("spy::one")
	_ = spy.caps.Register(Capability{ID: "runtime.info", Operations: []string{"read"}, Available: true})
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register spy Body: %v", err)
	}

	rules := NewRuleEvaluator(
		AuthorityRule{
			Doll: "d", Body: "spy::one", Capability: "runtime.info", Operation: "read",
			Decision: AuthorityAllow,
		},
	)
	recorder := &recordingEvaluator{inner: rules}
	guard := NewGuard(registry, recorder)

	args := map[string]any{"key1": "value1", "nested": map[string]any{"inner": 42}}

	guard.Execute(ExecutionRequest{
		ExecutionID: "exec-a",
		Doll:        "d",
		Body:        "spy::one",
		Capability:  "runtime.info",
		Operation:   "read",
		Arguments:   args,
	})

	if !reflect.DeepEqual(recorder.lastReq.Arguments, args) {
		t.Errorf("authority saw different arguments: got %v, want %v", recorder.lastReq.Arguments, args)
	}
	if !reflect.DeepEqual(spy.lastReq.Arguments, args) {
		t.Errorf("body executed different arguments: got %v, want %v", spy.lastReq.Arguments, args)
	}
	if !reflect.DeepEqual(recorder.lastReq.Arguments, spy.lastReq.Arguments) {
		t.Error("authorized arguments differ from executed arguments")
	}
}
