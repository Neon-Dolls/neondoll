//go:build e2e

package integration

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Body"
)

// ── Counting wrapper around the real Local Body ────────────────────────
//
// countingBody embeds a real *body.LocalBody and counts invocations so
// the e2e acceptance can prove zero/one invocation through the real
// execution function without modifying the Body implementation.
type countingBody struct {
	*body.LocalBody
	id       body.BodyID
	executed int
	lastReq  body.ExecutionRequest
}

func newCountingBody(id body.BodyID) *countingBody {
	return &countingBody{LocalBody: body.NewLocal(), id: id}
}

func (c *countingBody) ID() body.BodyID { return c.id }

func (c *countingBody) Execute(req body.ExecutionRequest) (*body.ExecutionResult, error) {
	c.executed++
	c.lastReq = req
	return c.LocalBody.Execute(req)
}

// ── Recording evaluator for argument-equivalence proof ─────────────────

type recordingEvaluator struct {
	inner body.AuthorityEvaluator
	last  body.AuthorityRequest
}

func (r *recordingEvaluator) Evaluate(req body.AuthorityRequest) body.AuthorityResult {
	r.last = req
	return r.inner.Evaluate(req)
}

// ── Acceptance: M4 Authorized Success ─────────────────────────────────

func TestM4AuthorizedSuccessThroughRealLocalBody(t *testing.T) {
	// Setup: real Local Body at the canonical local::core identity.
	registry := body.NewRegistry()
	guard := body.NewGuard(registry, body.NewRuleEvaluator(
		body.AuthorityRule{
			Doll: "test-doll", Body: body.LocalBodyID,
			Capability: "runtime.info", Operation: "read",
			Decision: body.AuthorityAllow, Reason: "explicit_allow",
		},
	))

	res := guard.Execute(body.ExecutionRequest{
		ExecutionID: "exec_123",
		Doll:        "test-doll",
		Body:        body.LocalBodyID,
		Capability:  "runtime.info",
		Operation:   "read",
		Arguments:   map[string]any{"context": "acceptance"},
	})

	// AC1+2: execution ID present and correlated.
	if res.ExecutionID != "exec_123" {
		t.Errorf("AC1+2: execution_id = %q, want %q", res.ExecutionID, "exec_123")
	}

	// AC8: authorized runtime.info/read → success through the real Local Body.
	if res.Status != body.StatusSuccess {
		t.Fatalf("AC8: status = %q, want %q", res.Status, body.StatusSuccess)
	}
	if res.Output == "" {
		t.Fatal("AC8: Output must not be empty")
	}

	// AC13: structured output with exactly the three benign fields.
	var raw map[string]any
	if err := json.Unmarshal([]byte(res.Output), &raw); err != nil {
		t.Fatalf("AC13: Output must be valid JSON: %v", err)
	}
	if len(raw) != 3 {
		t.Errorf("AC13: expected exactly 3 fields in runtime info, got %d: %v", len(raw), raw)
	}
	for k := range raw {
		switch k {
		case "os", "architecture", "go_version":
			if s, ok := raw[k].(string); !ok || s == "" {
				t.Errorf("AC13: field %q = %v, want non-empty string", k, raw[k])
			}
		default:
			t.Errorf("AC13: unexpected sensitive field in runtime info: %q", k)
		}
	}
}

// ── Acceptance: Terminal Outcome Matrix ───────────────────────────────

func TestM4TerminalOutcomes(t *testing.T) {
	registry := body.NewRegistry()

	// Register an unavailable capability on the real Local Body.
	if err := registry.RegisterCapability(body.LocalBodyID, body.Capability{
		ID: "test:blocked", Operations: []string{"do-thing"}, Available: false,
	}); err != nil {
		t.Fatalf("register blocked capability: %v", err)
	}

	// Register an available-but-unimplemented capability (Body.Execute will
	// return ErrExecutionNotAvailable → invocation failure → failed).
	if err := registry.RegisterCapability(body.LocalBodyID, body.Capability{
		ID: "test:unimplemented", Operations: []string{"run"}, Available: true,
	}); err != nil {
		t.Fatalf("register unimplemented capability: %v", err)
	}

	allowAll := body.NewGuard(registry, body.NewRuleEvaluator(
		body.AuthorityRule{
			Doll: "d", Body: body.LocalBodyID,
			Capability: "runtime.info", Operation: "read",
			Decision: body.AuthorityAllow, Reason: "allow_runtime",
		},
		body.AuthorityRule{
			Doll: "d", Body: body.LocalBodyID,
			Capability: "test:unimplemented", Operation: "run",
			Decision: body.AuthorityAllow, Reason: "allow_unimplemented",
		},
	))
	denyGuard := body.NewGuard(registry, body.NewRuleEvaluator()) // default deny

	mk := func(capID, op string) body.ExecutionRequest {
		return body.ExecutionRequest{
			ExecutionID: "exec_x",
			Doll:        "d",
			Body:        body.LocalBodyID,
			Capability:  capID,
			Operation:   op,
		}
	}

	tests := []struct {
		name     string
		guard    *body.Guard
		req      body.ExecutionRequest
		want     body.ExecutionStatus
		wantCode string
	}{
		// AC3: invalid
		{"invalid", allowAll, body.ExecutionRequest{Doll: "d", Body: body.LocalBodyID, Capability: "runtime.info", Operation: "read"}, body.StatusInvalid, "invalid_request"},
		// AC4: unsupported capability and operation
		{"unsupported-cap", allowAll, mk("no.such.capability", "read"), body.StatusUnsupported, "unsupported_capability"},
		{"unsupported-op", allowAll, mk("runtime.info", "write"), body.StatusUnsupported, "unsupported_operation"},
		// AC5: unavailable
		{"unavailable", allowAll, mk("test:blocked", "do-thing"), body.StatusUnavailable, "unavailable"},
		// AC6: denied (default)
		{"denied", denyGuard, mk("runtime.info", "read"), body.StatusDenied, "no_rule"},
		// AC7: authorized invocation failure → failed
		{"failed", allowAll, mk("test:unimplemented", "run"), body.StatusFailed, "invocation_failed"},
		// AC8: success (canonical real Local Body path)
		{"success", allowAll, mk("runtime.info", "read"), body.StatusSuccess, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req

			// AC2: every terminal result returns the same execution ID.
			// For the invalid case ExecutionID is intentionally empty;
			// both request and result will carry "" — that's the same.
			res := tc.guard.Execute(req)
			if res.ExecutionID != req.ExecutionID {
				t.Errorf("AC2: execution_id = %q, want %q", res.ExecutionID, req.ExecutionID)
			}

			if res.Status != tc.want {
				t.Errorf("status = %q, want %q; error_code=%q error=%q",
					res.Status, tc.want, res.ErrorCode, res.Error)
			}
			if tc.wantCode != "" && res.ErrorCode != tc.wantCode {
				t.Errorf("error_code = %q, want %q", res.ErrorCode, tc.wantCode)
			}
		})
	}
}

// ── Acceptance: Invocation Counts ─────────────────────────────────────

func TestM4InvocationCounts(t *testing.T) {
	registry := body.NewRegistry()
	counter := newCountingBody("local::counter")
	if err := registry.Register(counter); err != nil {
		t.Fatalf("register counting body: %v", err)
	}

	// Register blocked + unimplemented on the counting body.
	if err := counter.RegisterCapability(body.Capability{
		ID: "test:blocked", Operations: []string{"do-thing"}, Available: false,
	}); err != nil {
		t.Fatalf("register blocked: %v", err)
	}
	if err := counter.RegisterCapability(body.Capability{
		ID: "test:unimplemented", Operations: []string{"run"}, Available: true,
	}); err != nil {
		t.Fatalf("register unimplemented: %v", err)
	}

	allowAll := body.NewGuard(registry, body.NewRuleEvaluator(
		body.AuthorityRule{
			Doll: "d", Body: counter.ID(),
			Capability: "runtime.info", Operation: "read",
			Decision: body.AuthorityAllow, Reason: "allow_runtime",
		},
		body.AuthorityRule{
			Doll: "d", Body: counter.ID(),
			Capability: "test:unimplemented", Operation: "run",
			Decision: body.AuthorityAllow, Reason: "allow_unimplemented",
		},
	))
	denyGuard := body.NewGuard(registry, body.NewRuleEvaluator())

	mk := func(capID, op string) body.ExecutionRequest {
		return body.ExecutionRequest{
			ExecutionID: "exec_c",
			Doll:        "d",
			Body:        counter.ID(),
			Capability:  capID,
			Operation:   op,
		}
	}

	tests := []struct {
		name           string
		guard          *body.Guard
		req            body.ExecutionRequest
		want           body.ExecutionStatus
		wantExecutions int
	}{
		// AC9: zero invocations
		{"denied", denyGuard, mk("runtime.info", "read"), body.StatusDenied, 0},
		{"unsupported-cap", allowAll, mk("no.such", "read"), body.StatusUnsupported, 0},
		{"unsupported-op", allowAll, mk("runtime.info", "write"), body.StatusUnsupported, 0},
		{"unavailable", allowAll, mk("test:blocked", "do-thing"), body.StatusUnavailable, 0},
		{"invalid", allowAll, body.ExecutionRequest{Doll: "d", Body: counter.ID(), Capability: "runtime.info", Operation: "read"}, body.StatusInvalid, 0},
		// AC10: exactly one invocation
		{"success", allowAll, mk("runtime.info", "read"), body.StatusSuccess, 1},
		{"failed", allowAll, mk("test:unimplemented", "run"), body.StatusFailed, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req

			counter.executed = 0
			res := tc.guard.Execute(req)

			if res.Status != tc.want {
				t.Errorf("status = %q, want %q; error_code=%q error=%q",
					res.Status, tc.want, res.ErrorCode, res.Error)
			}
			if counter.executed != tc.wantExecutions {
				t.Errorf("executed = %d, want %d", counter.executed, tc.wantExecutions)
			}
		})
	}
}

// ── Acceptance: Arguments Authorized = Arguments Executed ──────────────

func TestM4ArgumentsAuthorizedAreArgumentsExecuted(t *testing.T) {
	registry := body.NewRegistry()
	counter := newCountingBody("local::args")
	if err := registry.Register(counter); err != nil {
		t.Fatalf("register counting body: %v", err)
	}

	rules := body.NewRuleEvaluator(body.AuthorityRule{
		Doll: "d", Body: counter.ID(),
		Capability: "runtime.info", Operation: "read",
		Decision: body.AuthorityAllow,
	})
	rec := &recordingEvaluator{inner: rules}
	guard := body.NewGuard(registry, rec)

	args := map[string]any{"detail": "m4-acceptance", "threshold": 0.85}
	guard.Execute(body.ExecutionRequest{
		ExecutionID: "exec_args",
		Doll:        "d",
		Body:        counter.ID(),
		Capability:  "runtime.info",
		Operation:   "read",
		Arguments:   args,
	})

	if !reflect.DeepEqual(rec.last.Arguments, args) {
		t.Errorf("AC11: authority saw different arguments: got %v, want %v", rec.last.Arguments, args)
	}
	if !reflect.DeepEqual(counter.lastReq.Arguments, args) {
		t.Errorf("AC11: body executed different arguments: got %v, want %v", counter.lastReq.Arguments, args)
	}
	if !reflect.DeepEqual(rec.last.Arguments, counter.lastReq.Arguments) {
		t.Errorf("AC11: authority arguments != executed arguments")
	}
}

// ── Acceptance: No Bypass ─────────────────────────────────────────────

func TestM4NoBypass(t *testing.T) {
	registry := body.NewRegistry()

	// AC12: Registry exposes no execution helper.
	if _, ok := any(registry).(interface {
		Execute(bodyID body.BodyID, req body.ExecutionRequest) (*body.ExecutionResult, error)
	}); ok {
		t.Error("AC12: Registry must not expose Execute")
	}

	// Guard.Execute is the single Core-mediated execution path. Default deny
	// via no rules.
	guard := body.NewGuard(registry, body.NewRuleEvaluator())
	res := guard.Execute(body.ExecutionRequest{
		ExecutionID: "exec_x",
		Doll:        "d",
		Body:        body.LocalBodyID,
		Capability:  "runtime.info",
		Operation:   "read",
	})
	if res.Status != body.StatusDenied {
		t.Errorf("AC12: default-deny through Guard.Execute = %q, want %q", res.Status, body.StatusDenied)
	}
}

// ── Acceptance: ErrExecutionNotAvailable Still Present ─────────────────

func TestM4ErrExecutionNotAvailableExists(t *testing.T) {
	// The sentinel is preserved for Body-side primitives, though production
	// callers use Guard.Execute which maps it into the failed outcome.
	b := body.NewLocal()
	_, err := b.Execute(body.ExecutionRequest{Capability: "unimplemented"})
	if !errors.Is(err, body.ErrExecutionNotAvailable) {
		t.Errorf("Execute(unimplemented) = %v, want ErrExecutionNotAvailable", err)
	}
}
