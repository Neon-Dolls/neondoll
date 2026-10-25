package bodyresolver

import (
	"context"
	"errors"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/Body"
)

var (
	ErrNoExecutionCandidates = errors.New("no eligible execution candidates")
	ErrCandidatesExhausted   = errors.New("body execution candidates exhausted")
	ErrGlobalProhibition     = errors.New("global authority prohibition")
	ErrTerminalExecution     = errors.New("terminal body execution failure")
)

// ExecutionRequest supplies execution-wide identity and arguments. The
// capability and operation come from the resolver Result, preventing the
// coordinator from independently selecting a different action.
type ExecutionRequest struct {
	ExecutionID string
	Doll        string
	Arguments   map[string]any
}

// Attempt is one independently guarded, correlated Body execution.
type Attempt struct {
	BodyID      body.BodyID
	ExecutionID string
	Result      body.ExecutionResult
}

// ExecutionOutcome contains the terminal result, when an attempt occurred,
// and the complete ordered attempt history.
type ExecutionOutcome struct {
	Result   *body.ExecutionResult
	Attempts []Attempt
}

// Coordinator consumes resolver ordering and invokes every attempted Body
// through the existing Guard. It performs no authorization of its own.
type Coordinator struct {
	guard *body.Guard
}

func NewCoordinator(guard *body.Guard) (*Coordinator, error) {
	if guard == nil {
		return nil, fmt.Errorf("body resolver coordinator: Guard is required")
	}
	return &Coordinator{guard: guard}, nil
}

// Execute attempts eligible candidates in their resolver-provided order.
// Only Body-scoped denial, unavailability, and execution failure permit the
// next candidate. Global denial, cancellation, invalid/stale resolution, and
// every other terminal status stop immediately.
func (coordinator *Coordinator) Execute(ctx context.Context, resolved Result, request ExecutionRequest) (ExecutionOutcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.ExecutionID == "" || request.Doll == "" || resolved.Requirement.Capability == "" || resolved.Requirement.Operation == "" {
		return ExecutionOutcome{}, ErrInvalidRequest
	}
	if len(resolved.Eligible) == 0 {
		return ExecutionOutcome{}, ErrNoExecutionCandidates
	}

	outcome := ExecutionOutcome{Attempts: make([]Attempt, 0, len(resolved.Eligible))}
	for index, candidate := range resolved.Eligible {
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		attemptID := fmt.Sprintf("%s/attempt/%d", request.ExecutionID, index+1)
		result := coordinator.guard.ExecuteContext(ctx, body.ExecutionRequest{
			ExecutionID: attemptID,
			Doll:        request.Doll,
			Body:        candidate.Body.ID(),
			Capability:  resolved.Requirement.Capability,
			Operation:   resolved.Requirement.Operation,
			Arguments:   request.Arguments,
		})
		outcome.Attempts = append(outcome.Attempts, Attempt{BodyID: candidate.Body.ID(), ExecutionID: attemptID, Result: result})
		outcome.Result = &outcome.Attempts[len(outcome.Attempts)-1].Result

		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		switch result.Status {
		case body.StatusSuccess:
			return outcome, nil
		case body.StatusDenied:
			switch result.DenialScope {
			case body.AuthorityScopeGlobal:
				return outcome, ErrGlobalProhibition
			case "", body.AuthorityScopeBody:
				continue
			default:
				return outcome, ErrTerminalExecution
			}
		case body.StatusUnavailable, body.StatusFailed:
			continue
		default:
			return outcome, ErrTerminalExecution
		}
	}
	return outcome, ErrCandidatesExhausted
}
