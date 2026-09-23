// Package body — authority types and evaluator for Core 2 Milestone 3.
//
// M3 introduces the deterministic authority boundary between a requested
// Body operation and invocation. Availability and authority are separate:
//
//	reachable ≠ identified ≠ capable ≠ available ≠ authorized
//
// These types belong to Core semantics. They MUST NOT depend on an LLM
// provider, Doll Link transport, Interaction Session, or OS-specific
// permission model.
package body

// AuthorityRequest carries the full context required to evaluate whether
// a Body operation is authorized. It identifies the requesting Doll, the
// target Body and capability, the operation, and any arguments/context
// relevant to the decision.
type AuthorityRequest struct {
	// Doll identifies the Doll requesting the operation.
	Doll string

	// Body is the Body that would perform the operation.
	Body BodyID

	// Capability is the capability ID requested (e.g. "runtime.info").
	Capability string

	// Operation is the operation requested (e.g. "read").
	Operation string

	// Arguments carries the requested arguments/context where relevant.
	// M3 rule matching uses Doll+Body+Capability+Operation; Arguments
	// are carried for context and future policy layers.
	Arguments map[string]any
}

// AuthorityDecision is the explicit outcome of an authority evaluation.
type AuthorityDecision string

const (
	// AuthorityAllow permits the requested operation.
	AuthorityAllow AuthorityDecision = "allow"
	// AuthorityDeny refuses the requested operation.
	AuthorityDeny AuthorityDecision = "deny"
)

// String returns the string representation of the decision.
func (d AuthorityDecision) String() string { return string(d) }

// AuthorityResult is the explicit outcome of evaluating an AuthorityRequest.
// A zero-value result (empty Decision) is treated as deny by consumers,
// ensuring fail-closed default-deny behavior even from malfunctioning
// evaluators.
type AuthorityResult struct {
	// Decision is allow or deny. It is explicit, never inferred from the
	// absence of an error.
	Decision AuthorityDecision `json:"decision"`

	// Reason is a machine-usable code for a denial (e.g. "no_rule",
	// "explicit_deny"). Empty when the decision is allow.
	Reason string `json:"reason,omitempty"`

	// Message is an optional human-readable explanation of the decision.
	Message string `json:"message,omitempty"`
}

// IsAllow reports whether the decision is explicitly allow.
func (r AuthorityResult) IsAllow() bool { return r.Decision == AuthorityAllow }

// IsDeny reports whether the decision is deny. Fail-closed: a zero-value
// result (neither allow nor deny, or empty) counts as deny, so a
// malfunctioning evaluator can never accidentally authorize a request.
func (r AuthorityResult) IsDeny() bool { return r.Decision != AuthorityAllow }

// ---------------------------------------------------------------------------
// Phase 2 — Deterministic Core 2 Authority Evaluator
// ---------------------------------------------------------------------------

// AuthorityEvaluator decides whether an AuthorityRequest is authorized.
//
// Implementations MUST default to deny when no applicable allow exists.
// They MUST NOT infer permission from capability presence, available == true,
// Local Body identity, successful authentication, or network reachability.
type AuthorityEvaluator interface {
	// Evaluate returns the authority decision for the given request.
	Evaluate(req AuthorityRequest) AuthorityResult
}

// AuthorityRule is an explicit rule mapping a (Doll, Body, Capability,
// Operation) combination to a decision. All four fields must equal the
// request's fields for the rule to apply. A rule with empty fields matches
// requests that also have empty values for those fields.
type AuthorityRule struct {
	Doll       string
	Body       BodyID
	Capability string
	Operation  string
	Decision   AuthorityDecision
	Reason     string
	Message    string
}

// RuleEvaluator is the deterministic M3 authority evaluator. It matches
// an AuthorityRequest against explicit rules in order; the first matching
// rule wins. If no rule matches, the decision is deny with reason "no_rule".
//
// RuleEvaluator knows nothing about capability presence, availability,
// Body identity, authentication, or reachability. An available capability
// is never implicitly authorized — only an explicit allow rule grants
// permission.
type RuleEvaluator struct {
	rules []AuthorityRule
}

// NewRuleEvaluator creates a RuleEvaluator with the given rules, evaluated
// in order on each call to Evaluate.
func NewRuleEvaluator(rules ...AuthorityRule) *RuleEvaluator {
	return &RuleEvaluator{rules: rules}
}

// Evaluate matches the request against the configured rules. The first
// matching rule determines the result. If no rule matches, the decision
// is deny with reason "no_rule".
func (e *RuleEvaluator) Evaluate(req AuthorityRequest) AuthorityResult {
	for _, r := range e.rules {
		if r.Doll == req.Doll &&
			r.Body == req.Body &&
			r.Capability == req.Capability &&
			r.Operation == req.Operation {
			return AuthorityResult{
				Decision: r.Decision,
				Reason:   r.Reason,
				Message:  r.Message,
			}
		}
	}
	return AuthorityResult{
		Decision: AuthorityDeny,
		Reason:   "no_rule",
		Message:  "no authority rule matches this request",
	}
}
