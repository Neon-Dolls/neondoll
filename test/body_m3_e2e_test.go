//go:build e2e

package integration

import (
	"errors"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Body"
)

// TestM3AuthorityGuardsExecution proves Milestone 3 acceptance through the
// real Local Body and its real runtime.info / read capability, exercising
// the full guarded Core path:
//
//	capability exists + available
//	        ↓
//	request enters guarded Core path
//	        ↓
//	authority check
//	    ┌───┴───┐
//	  deny      allow
//	   ↓          ↓
//	denied      eligible
//	   ↓          ↓
//	 stop       stop (M3)
func TestM3AuthorityGuardsExecution(t *testing.T) {
	// ── Setup: real Local Body ──────────────────────────────────────
	registry := body.NewRegistry()

	// AC1+AC5: explicit allow for test Doll on local::core runtime.info/read.
	allowEvaluator := body.NewRuleEvaluator(
		body.AuthorityRule{
			Doll:       "test-doll",
			Body:       body.LocalBodyID,
			Capability: "runtime.info",
			Operation:  "read",
			Decision:   body.AuthorityAllow,
			Reason:     "explicit_allow",
		},
	)

	guard := body.NewGuard(registry, allowEvaluator)

	// ── AC5: runtime.info/read explicitly authorized through real Local Body ──
	// Prove the capability exists and is available.
	if err := registry.ResolveCapability(body.LocalBodyID, "runtime.info", "read"); err != nil {
		t.Fatalf("AC5: runtime.info/read must resolve: %v", err)
	}

	res, err := guard.Authorize(body.AuthorityRequest{
		Doll:       "test-doll",
		Body:       body.LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
		Arguments:  map[string]any{"context": "acceptance"},
	})
	if err != nil {
		t.Fatalf("AC5: Authorize = %v, want nil error", err)
	}
	if !res.Eligible {
		t.Errorf("AC5: Authorize = %+v, want eligible", res)
	}
	if res.Denied {
		t.Errorf("AC5: Authorize = %+v, want not denied", res)
	}

	// AC10: no real capability execution is added in M3.
	localBody, _ := registry.Local()
	_, execErr := localBody.Execute(body.ExecutionRequest{Capability: "runtime.info"})
	if !errors.Is(execErr, body.ErrExecutionNotAvailable) {
		t.Errorf("AC10: Execute(runtime.info) = %v, want ErrExecutionNotAvailable", execErr)
	}

	// ── AC6: same available operation explicitly denied ─────────────────
	denyEvaluator := body.NewRuleEvaluator(
		body.AuthorityRule{
			Doll:       "test-doll",
			Body:       body.LocalBodyID,
			Capability: "runtime.info",
			Operation:  "read",
			Decision:   body.AuthorityDeny,
			Reason:     "explicit_deny",
		},
	)
	denyGuard := body.NewGuard(registry, denyEvaluator)

	res, err = denyGuard.Authorize(body.AuthorityRequest{
		Doll:       "test-doll",
		Body:       body.LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err != nil {
		t.Fatalf("AC6: Authorize = %v, want nil error", err)
	}
	if !res.Denied {
		t.Errorf("AC6: Authorize = %+v, want denied", res)
	}
	if res.Eligible {
		t.Errorf("AC6: Authorize = %+v, want not eligible", res)
	}
	if res.Reason != "explicit_deny" {
		t.Errorf("AC6: Reason = %q, want %q", res.Reason, "explicit_deny")
	}

	// ── AC3: no matching rule → deny (default-deny) ─────────────────────
	emptyGuard := body.NewGuard(registry, body.NewRuleEvaluator())
	res, err = emptyGuard.Authorize(body.AuthorityRequest{
		Doll:       "test-doll",
		Body:       body.LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err != nil {
		t.Fatalf("AC3: Authorize = %v, want nil error (denial is outcome)", err)
	}
	if !res.Denied {
		t.Errorf("AC3: Authorize = %+v, want denied (no rule = deny)", res)
	}

	// AC4: capability availability does not imply authority — already
	// proven above (available runtime.info/read + no matching rule = deny).

	// ── AC8: M2 resolution failure outcomes remain distinct ─────────────
	// Unsupported capability: a request for a nonexistent capability fails
	// with ErrUnsupportedCapability before authority is consulted.
	_, err = guard.Authorize(body.AuthorityRequest{
		Doll:       "test-doll",
		Body:       body.LocalBodyID,
		Capability: "no.such.capability",
		Operation:  "read",
	})
	if !errors.Is(err, body.ErrUnsupportedCapability) {
		t.Errorf("AC8: unsupported capability = %v, want ErrUnsupportedCapability", err)
	}

	// Unsupported operation: a known capability with an unknown operation
	// fails with ErrUnsupportedOperation before authority.
	_, err = guard.Authorize(body.AuthorityRequest{
		Doll:       "test-doll",
		Body:       body.LocalBodyID,
		Capability: "runtime.info",
		Operation:  "write",
	})
	if !errors.Is(err, body.ErrUnsupportedOperation) {
		t.Errorf("AC8: unsupported operation = %v, want ErrUnsupportedOperation", err)
	}

	// Unavailable: declare an unavailable capability on the real Local Body.
	blocked := body.Capability{
		ID:         "test:blocked",
		Operations: []string{"do-thing"},
		Available:  false,
	}
	if err := registry.RegisterCapability(body.LocalBodyID, blocked); err != nil {
		t.Fatalf("AC8: declare unavailable capability: %v", err)
	}
	_, err = guard.Authorize(body.AuthorityRequest{
		Doll:       "test-doll",
		Body:       body.LocalBodyID,
		Capability: "test:blocked",
		Operation:  "do-thing",
	})
	if !errors.Is(err, body.ErrUnavailable) {
		t.Errorf("AC8: unavailable = %v, want ErrUnavailable", err)
	}

	// ── AC7: denial cannot reach Body invocation ───────────────────
	// Already proven by AC6: an explicitly denied request returned
	// a GuardResult with Denied=true and execution remains unavailable.
	// Re-verify that execution is impossible:
	_, execErr = localBody.Execute(body.ExecutionRequest{Capability: "runtime.info"})
	if !errors.Is(execErr, body.ErrExecutionNotAvailable) {
		t.Errorf("AC7: Execute after denial = %v, want ErrExecutionNotAvailable", execErr)
	}
}

// TestM3NoBypass proves there is no alternate Core execution path that
// bypasses the authority evaluator. The Guard is the single mediated
// entry point, and Body.Execute remains uninvokable.
func TestM3NoBypass(t *testing.T) {
	registry := body.NewRegistry()
	guard := body.NewGuard(registry, body.NewRuleEvaluator()) // default deny on everything

	// The only Core-mediated path is Guard.Authorize.
	res, err := guard.Authorize(body.AuthorityRequest{
		Doll:       "test-doll",
		Body:       body.LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err != nil {
		t.Fatalf("AC9: Authorize = %v, want nil", err)
	}
	if !res.Denied {
		t.Fatalf("AC9: Authorize = %+v, want denied (no allow rule)", res)
	}

	// Body execution is not available through any route.
	localBody, _ := registry.Local()
	_, execErr := localBody.Execute(body.ExecutionRequest{Capability: "runtime.info"})
	if !errors.Is(execErr, body.ErrExecutionNotAvailable) {
		t.Errorf("AC9: Execute = %v, want ErrExecutionNotAvailable", execErr)
	}

	// Registry exposes no execution API.
	if _, ok := any(registry).(interface {
		Execute(bodyID body.BodyID, req body.ExecutionRequest) (*body.ExecutionResult, error)
	}); ok {
		t.Error("AC9: Registry must not expose Execute")
	}
}

// TestM3PreexistingExecutableNotInvoked proves Guard never calls Body.Execute.
func TestM3PreexistingExecutableNotInvoked(t *testing.T) {
	// This test uses the real Local Body; M2 proved Execute returns
	// ErrExecutionNotAvailable. This test proves M3's Guard never calls it.
	registry := body.NewRegistry()
	ev := body.NewRuleEvaluator(
		body.AuthorityRule{
			Doll:       "d",
			Body:       body.LocalBodyID,
			Capability: "runtime.info",
			Operation:  "read",
			Decision:   body.AuthorityAllow,
		},
	)
	guard := body.NewGuard(registry, ev)

	res, err := guard.Authorize(body.AuthorityRequest{
		Doll:       "d",
		Body:       body.LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})
	if err != nil {
		t.Fatalf("Authorize error = %v", err)
	}
	if !res.Eligible {
		t.Fatalf("Authorize = %+v, want eligible", res)
	}

	// Local Body's Execute still returns ErrExecutionNotAvailable.
	localBody, _ := registry.Local()
	_, execErr := localBody.Execute(body.ExecutionRequest{Capability: "runtime.info"})
	if !errors.Is(execErr, body.ErrExecutionNotAvailable) {
		t.Errorf("Execute after eligible = %v, want ErrExecutionNotAvailable", execErr)
	}
}
