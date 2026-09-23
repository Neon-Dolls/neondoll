package body

import (
	"errors"
	"fmt"
)

// Guard is the single Core-mediated boundary between a requested Body
// operation and invocation. Authorize (M3) is the authority-only preflight
// query that never invokes; Execute (M4) is the one production execution
// path that validates, resolves, authorizes, and invokes atomically so
// there is no time-of-check/time-of-use bypass surface.
//
// The production execution path is:
//
//	execution request
//	      ↓
//	validate (structural fields)
//	      ↓
//	Body lookup
//	      ↓
//	capability + operation resolution
//	      ↓
//	authority evaluation
//	   ┌──┴──┐
//	 deny   allow
//	  ↓       ↓
//	denied   invoke Body operation
//	result       ↓
//	         execution result
//
// Authority and invocation are one path: the same ExecutionRequest passes
// through validation, resolution, authority evaluation (where its Arguments
// are presented to the AuthorityEvaluator), and — on allow — to Body.Execute.
// Denied/unsupported/unavailable/invalid requests never reach Body.Execute.
//
// Do not give callers a convenience path that invokes a Body while skipping
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

// GuardResult is the outcome of an M3 authority-only request.
// M4 callers use the terminal ExecutionResult from Execute.
type GuardResult struct {
	// Eligible is true when authority approved the request and it is
	// eligible for invocation. In M3 eligible requests are NOT
	// invoked; in M4 use Guard.Execute for real execution.
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

// Authorize runs the M3 authority-only preflight path for one request:
//
//	request → Body lookup → capability + operation resolution
//	       → authority evaluation → denied | eligible
//
// Authorize never invokes Body.Execute. For real execution use
// Guard.Execute, which validates, resolves, authorizes, and invokes
// in one indivisible path.
//
// It returns:
//   - an error if the Body cannot be looked up or the capability/operation
//     fails resolution (ErrUnsupportedCapability, ErrUnsupportedOperation,
//     ErrUnavailable — M2 semantics preserved). The evaluator is not
//     consulted for resolution failures.
//   - a GuardResult with Denied=true if authority refused the request;
//   - a GuardResult with Eligible=true if authority approved it. The
//     capability is NOT executed — use Guard.Execute for real invocation.
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

// Execute runs the single Core-mediated production execution path for one
// request. It validates structural fields, looks up the target Body,
// resolves capability support and availability, evaluates authority
// (default-deny), and — only on explicit allow — invokes the Body
// operation:
//
//	execution request → validate → Body lookup → capability +
//	operation resolution → authority evaluation → invoke Body
//	→ correlated terminal result
//
// The same ExecutionRequest — including its Arguments — is presented to
// authority evaluation and, on allow, passed unchanged to Body.Execute.
// There is no callable intermediate "authorize then invoke" step that a
// caller could split into separate calls, because that would create a
// time-of-check/time-of-use bypass surface.
//
// Every terminal outcome carries the request's execution ID:
//
//	invalid      — structurally invalid request (fails field validation)
//	unsupported  — unknown capability or operation (M2 resolution)
//	unavailable  — known capability/operation currently not available
//	denied       — authority refused (default-deny)
//	failed       — authorized invocation failed during execution
//	success      — authorized execution completed
//
// Denied, unsupported, invalid, and unavailable requests never reach
// Body.Execute. Authorized invocation that fails is never confused with
// denial, unsupported, or unavailable.
func (g *Guard) Execute(req ExecutionRequest) ExecutionResult {
	// 1. Structural validation. Invalid never reaches authority or
	// invocation; the execution ID is always correlated into the result.
	if req.ExecutionID == "" || req.Doll == "" || req.Body == "" ||
		req.Capability == "" || req.Operation == "" {
		return terminal(req, StatusInvalid, "invalid_request", "execution request is missing required fields (execution_id, doll, body, capability, operation)")
	}

	// 2. Body lookup.
	b, ok := g.registry.Get(req.Body)
	if !ok {
		return terminal(req, StatusInvalid, "body_not_found", fmt.Sprintf("body %q not found", req.Body))
	}

	// 3. Capability + operation resolution. Support and availability are
	// validated BEFORE authority — an unsupported or unavailable capability
	// can never become an authority-approved execution.
	if err := b.ResolveCapability(req.Capability, req.Operation); err != nil {
		switch {
		case errors.Is(err, ErrUnsupportedCapability), errors.Is(err, ErrUnsupportedOperation):
			code := "resolution_failed"
			if errors.Is(err, ErrUnsupportedCapability) {
				code = "unsupported_capability"
			} else if errors.Is(err, ErrUnsupportedOperation) {
				code = "unsupported_operation"
			}
			return terminal(req, StatusUnsupported, code, err.Error())
		case errors.Is(err, ErrUnavailable):
			return terminal(req, StatusUnavailable, "unavailable", err.Error())
		default:
			return terminal(req, StatusFailed, "resolution_failed", err.Error())
		}
	}

	// 4. Authority evaluation. The same Arguments are presented to
	// authority as will be executed.
	authReq := AuthorityRequest{
		Doll:       req.Doll,
		Body:       req.Body,
		Capability: req.Capability,
		Operation:  req.Operation,
		Arguments:  req.Arguments,
	}
	authRes := g.evaluator.Evaluate(authReq)
	if authRes.IsDeny() {
		return terminal(req, StatusDenied, authRes.Reason, authRes.Message)
	}

	// 5. Invoke the Body operation with the same request that was
	// authorized. The Arguments are the same map — no mutation.
	bodyRes, err := b.Execute(req)
	if err != nil {
		return terminal(req, StatusFailed, "invocation_failed", err.Error())
	}
	if bodyRes == nil {
		return terminal(req, StatusFailed, "invocation_failed", "body returned nil result")
	}

	// 6. Correlate the Body's terminal result with the request's
	// execution ID. The execution ID is always from the original
	// request, never from what the Body returns.
	bodyRes.ExecutionID = req.ExecutionID
	return *bodyRes
}

// terminal builds a correlated terminal ExecutionResult carrying the
// request's execution ID, the given status, machine-usable reason code,
// and optional human-readable message.
func terminal(req ExecutionRequest, status ExecutionStatus, code, message string) ExecutionResult {
	return ExecutionResult{
		ExecutionID: req.ExecutionID,
		Status:      status,
		ErrorCode:   code,
		Error:       message,
	}
}
