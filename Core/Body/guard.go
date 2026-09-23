package body

import "fmt"

// Guard is the single Core-mediated authority boundary between a requested
// Body operation and invocation. M4 will place real invocation immediately
// after this boundary.
//
// For M3 the path is:
//
//	request
//	   ↓
//	Body lookup
//	   ↓
//	capability + operation resolution
//	   ↓
//	authority evaluation
//	   ┌──────┴──────┐
//	 deny           allow
//	  ↓               ↓
//	explicit denied   eligible for invocation
//	                  (M4 will invoke)
//
// The boundary MUST validate capability support/availability BEFORE asking
// authority to approve invocation, and it MUST never call Body.Execute in
// M3 — an allowed request returns an explicit authorized/eligible result,
// and a denied request stops at the authority boundary.
//
// Do not callers a convenience path that invokes a Body while skipping
// authority: Guard is the normal Core execution path and owns the authority
// check.
type Guard struct {
	registry  *Registry
	evaluator AuthorityEvaluator
}

// NewGuard creates the Core-mediated authority boundary over the given
// Body Registry with the given authority evaluator.
func NewGuard(registry *Registry, evaluator AuthorityEvaluator) *Guard {
	return &Guard{registry: registry, evaluator: evaluator}
}

// GuardResult is the outcome of a guarded request.
type GuardResult struct {
	// Eligible is true when authority approved the request and it is
	// eligible for invocation (M4). In M3 eligible requests are NOT
	// invoked.
	Eligible bool

	// Denied is true when authority explicitly refused the request.
	// Denied requests stop at the boundary and never reach invocation.
	Denied bool

	// Reason is a machine-usable denial code (e.g. "no_rule",
	// "explicit_deny"). Empty when eligible.
	Reason string

	// Message is an optional human-readable explanation.
	Message string
}

// Authorize runs the guarded Core path for one request:
//
//	request → Body lookup → capability + operation resolution
//	       → authority evaluation → denied | eligible
//
// It returns:
//   - an error if the Body cannot be looked up or the capability/operation
//     fails resolution (ErrUnsupportedCapability, ErrUnsupportedOperation,
//     ErrUnavailable — M2 semantics preserved). These are resolution
//     failures, NOT authority decisions; the evaluator is not consulted.
//   - a GuardResult with Denied=true if authority refused the request;
//   - a GuardResult with Eligible=true if authority approved it. The
//     capability is NOT executed in M3.
func (g *Guard) Authorize(req AuthorityRequest) (GuardResult, error) {
	// 1. Body lookup.
	b, ok := g.registry.Get(req.Body)
	if !ok {
		return GuardResult{}, fmt.Errorf("body %q not found", req.Body)
	}

	// 2. Capability + operation resolution. Support and availability are
	// validated BEFORE authority — an unsupported or unavailable capability
	// can never become an authority-approved execution.
	if err := b.ResolveCapability(req.Capability, req.Operation); err != nil {
		return GuardResult{}, err
	}

	// 3. Authority evaluation. Default deny: only an explicit allow may
	// reach eligible.
	result := g.evaluator.Evaluate(req)
	if result.IsDeny() {
		return GuardResult{
			Denied:  true,
			Reason:  result.Reason,
			Message: result.Message,
		}, nil
	}

	return GuardResult{
		Eligible: true,
		Reason:   result.Reason,
		Message:  result.Message,
	}, nil
}
