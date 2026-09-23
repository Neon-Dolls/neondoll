package body

import (
	"testing"
)

// ── Phase 1: Authority Request and Decision ────────────────────────────

// TestAuthorityRequest_PreservesIdentities proves the authority request
// carries every identity and context needed to evaluate permission:
// Doll, Body, capability, operation, and arguments.
func TestAuthorityRequest_PreservesIdentities(t *testing.T) {
	req := AuthorityRequest{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
		Arguments: map[string]any{
			"format": "json",
		},
	}

	if req.Doll != "neko-chan" {
		t.Errorf("req.Doll = %q, want %q", req.Doll, "neko-chan")
	}
	if req.Body != LocalBodyID {
		t.Errorf("req.Body = %q, want %q", req.Body, LocalBodyID)
	}
	if req.Capability != "runtime.info" {
		t.Errorf("req.Capability = %q, want %q", req.Capability, "runtime.info")
	}
	if req.Operation != "read" {
		t.Errorf("req.Operation = %q, want %q", req.Operation, "read")
	}
	if len(req.Arguments) != 1 || req.Arguments["format"] != "json" {
		t.Errorf("req.Arguments = %v, want {format: json}", req.Arguments)
	}
}

// TestAuthorityDecision_Explicit proves decisions are explicit values,
// not inferred from the absence of an error.
func TestAuthorityDecision_Explicit(t *testing.T) {
	if AuthorityAllow.String() != "allow" {
		t.Errorf("AuthorityAllow.String() = %q, want %q", AuthorityAllow.String(), "allow")
	}
	if AuthorityDeny.String() != "deny" {
		t.Errorf("AuthorityDeny.String() = %q, want %q", AuthorityDeny.String(), "deny")
	}

	allowResult := AuthorityResult{Decision: AuthorityAllow}
	denyResult := AuthorityResult{Decision: AuthorityDeny}

	if !allowResult.IsAllow() || allowResult.IsDeny() {
		t.Errorf("allowResult: IsAllow()=%v IsDeny()=%v, want true/false", allowResult.IsAllow(), allowResult.IsDeny())
	}
	if denyResult.IsAllow() || !denyResult.IsDeny() {
		t.Errorf("denyResult: IsAllow()=%v IsDeny()=%v, want false/true", denyResult.IsAllow(), denyResult.IsDeny())
	}
}

// TestAuthorityResult_ZeroValueTreatsAsDeny proves the fail-closed default:
// a zero-value result (empty decision) counts as deny, never allow.
func TestAuthorityResult_ZeroValueTreatsAsDeny(t *testing.T) {
	var z AuthorityResult
	if z.IsAllow() {
		t.Error("zero-value AuthorityResult is allow, want deny (fail-closed)")
	}
	if !z.IsDeny() {
		t.Error("zero-value AuthorityResult is not deny, want deny (fail-closed)")
	}
}

// ── Phase 2: Deterministic Evaluator ─────────────────────────────────────

// TestRuleEvaluator_ExplicitAllow proves an explicit allow rule → allow.
func TestRuleEvaluator_ExplicitAllow(t *testing.T) {
	ev := NewRuleEvaluator(AuthorityRule{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
		Decision:   AuthorityAllow,
		Reason:     "explicit_allow",
	})

	res := ev.Evaluate(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})

	if !res.IsAllow() {
		t.Fatalf("Evaluate() = %+v, want allow", res)
	}
	if res.Reason != "explicit_allow" {
		t.Errorf("Evaluate().Reason = %q, want %q", res.Reason, "explicit_allow")
	}
}

// TestRuleEvaluator_ExplicitDeny proves an explicit deny rule → deny.
func TestRuleEvaluator_ExplicitDeny(t *testing.T) {
	ev := NewRuleEvaluator(AuthorityRule{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
		Decision:   AuthorityDeny,
		Reason:     "explicit_deny",
	})

	res := ev.Evaluate(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})

	if !res.IsDeny() {
		t.Fatalf("Evaluate() = %+v, want deny", res)
	}
	if res.Reason != "explicit_deny" {
		t.Errorf("Evaluate().Reason = %q, want %q", res.Reason, "explicit_deny")
	}
}

// TestRuleEvaluator_NoMatchingRuleDenies proves default/no-match is deny.
func TestRuleEvaluator_NoMatchingRuleDenies(t *testing.T) {
	ev := NewRuleEvaluator(AuthorityRule{
		Doll:       "other-doll",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
		Decision:   AuthorityAllow,
	})

	res := ev.Evaluate(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})

	if !res.IsDeny() {
		t.Fatalf("Evaluate() = %+v, want deny when no rule matches", res)
	}
	if res.Reason != "no_rule" {
		t.Errorf("Evaluate().Reason = %q, want %q", res.Reason, "no_rule")
	}
}

// TestRuleEvaluator_EmptyEvaluatorDenies proves an evaluator with no rules
// denies everything — nothing is implicitly allowed.
func TestRuleEvaluator_EmptyEvaluatorDenies(t *testing.T) {
	ev := NewRuleEvaluator()
	res := ev.Evaluate(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})
	if !res.IsDeny() {
		t.Fatalf("empty evaluator Evaluate() = %+v, want deny", res)
	}
}

// TestRuleEvaluator_AvailableCapabilityNotAuthorized proves the evaluator
// itself knows nothing about availability: a capability that is present and
// available is still denied without an explicit allow rule. The evaluator
// receives no capability availability information at all.
func TestRuleEvaluator_AvailableCapabilityNotAuthorized(t *testing.T) {
	// The registry holds runtime.info/read as available (M2 reality), but
	// the evaluator is empty — availability must not imply authority.
	registry := NewRegistry()
	if err := registry.ResolveCapability(LocalBodyID, "runtime.info", "read"); err != nil {
		t.Fatalf("setup: runtime.info/read should be supported+available: %v", err)
	}

	ev := NewRuleEvaluator()
	res := ev.Evaluate(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})
	if !res.IsDeny() {
		t.Fatalf("available capability with no allow rule = %+v, want deny", res)
	}
}

// TestRuleEvaluator_IdentityChangePreventsMatch proves that changing any of
// Doll, Body, capability, or operation prevents an unrelated allow rule
// from matching.
func TestRuleEvaluator_IdentityChangePreventsMatch(t *testing.T) {
	base := AuthorityRule{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
		Decision:   AuthorityAllow,
	}

	cases := []struct {
		name string
		req  AuthorityRequest
	}{
		{
			name: "different Doll",
			req:  AuthorityRequest{Doll: "other-doll", Body: LocalBodyID, Capability: "runtime.info", Operation: "read"},
		},
		{
			name: "different Body",
			req:  AuthorityRequest{Doll: "neko-chan", Body: "remote::elsewhere", Capability: "runtime.info", Operation: "read"},
		},
		{
			name: "different capability",
			req:  AuthorityRequest{Doll: "neko-chan", Body: LocalBodyID, Capability: "file.access", Operation: "read"},
		},
		{
			name: "different operation",
			req:  AuthorityRequest{Doll: "neko-chan", Body: LocalBodyID, Capability: "runtime.info", Operation: "write"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := NewRuleEvaluator(base)
			res := ev.Evaluate(tc.req)
			if !res.IsDeny() {
				t.Errorf("Evaluate(%+v) = %+v, want deny (rule must not match)", tc.req, res)
			}
		})
	}
}

// TestRuleEvaluator_FirstMatchingRuleWins proves deterministic in-order
// matching: the first matching rule decides.
func TestRuleEvaluator_FirstMatchingRuleWins(t *testing.T) {
	ev := NewRuleEvaluator(
		AuthorityRule{
			Doll:       "neko-chan",
			Body:       LocalBodyID,
			Capability: "runtime.info",
			Operation:  "read",
			Decision:   AuthorityDeny,
			Reason:     "first_rule",
		},
		AuthorityRule{
			Doll:       "neko-chan",
			Body:       LocalBodyID,
			Capability: "runtime.info",
			Operation:  "read",
			Decision:   AuthorityAllow,
			Reason:     "second_rule",
		},
	)

	res := ev.Evaluate(AuthorityRequest{
		Doll:       "neko-chan",
		Body:       LocalBodyID,
		Capability: "runtime.info",
		Operation:  "read",
	})
	if !res.IsDeny() || res.Reason != "first_rule" {
		t.Errorf("Evaluate() = %+v, want deny with reason %q", res, "first_rule")
	}
}
