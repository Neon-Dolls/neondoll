package bodyresolver_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/BodyResolver"
)

type executionBody struct {
	id               body.BodyID
	mu               sync.Mutex
	resolveCalls     int
	unavailableAfter int
	executionError   error
	waitForCancel    bool
	started          chan struct{}
	executions       int
}

func (b *executionBody) ID() body.BodyID     { return b.id }
func (b *executionBody) Kind() body.BodyKind { return body.BodyKindRemote }
func (b *executionBody) Name() string        { return string(b.id) }
func (b *executionBody) Describe() []body.Capability {
	return []body.Capability{{ID: "test.echo", Operations: []string{"run"}, Available: true}}
}
func (b *executionBody) RegisterCapability(body.Capability) error { return nil }
func (b *executionBody) ResolveCapability(capability, operation string) error {
	if capability != "test.echo" {
		return body.ErrUnsupportedCapability
	}
	if operation != "run" {
		return body.ErrUnsupportedOperation
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resolveCalls++
	if b.unavailableAfter > 0 && b.resolveCalls >= b.unavailableAfter {
		return body.ErrUnavailable
	}
	return nil
}
func (b *executionBody) Execute(request body.ExecutionRequest) (*body.ExecutionResult, error) {
	return b.ExecuteContext(context.Background(), request)
}
func (b *executionBody) ExecuteContext(ctx context.Context, request body.ExecutionRequest) (*body.ExecutionResult, error) {
	b.mu.Lock()
	b.executions++
	b.mu.Unlock()
	if b.waitForCancel {
		if b.started != nil {
			close(b.started)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if b.executionError != nil {
		return nil, b.executionError
	}
	return &body.ExecutionResult{Status: body.StatusSuccess, Output: string(b.id)}, nil
}

func TestCoordinatorFirstBodySucceeds(t *testing.T) {
	first := &executionBody{id: "body-first"}
	second := &executionBody{id: "body-second"}
	coordinator, resolved := coordinatorFixture(t, []*executionBody{first, second},
		allowRule(first.id), allowRule(second.id))

	outcome, err := coordinator.Execute(context.Background(), resolved, executionRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertAttempts(t, outcome, []body.BodyID{first.id}, []body.ExecutionStatus{body.StatusSuccess})
	if outcome.Result == nil || outcome.Result.Output != string(first.id) || second.executions != 0 {
		t.Fatalf("outcome=%+v second executions=%d", outcome, second.executions)
	}
}

func TestCoordinatorUnavailableBodyFallsBackAndCorrelatesAttempts(t *testing.T) {
	first := &executionBody{id: "body-first", unavailableAfter: 2}
	second := &executionBody{id: "body-second"}
	coordinator, resolved := coordinatorFixture(t, []*executionBody{first, second},
		allowRule(first.id), allowRule(second.id))

	outcome, err := coordinator.Execute(context.Background(), resolved, executionRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertAttempts(t, outcome, []body.BodyID{first.id, second.id}, []body.ExecutionStatus{body.StatusUnavailable, body.StatusSuccess})
	if outcome.Attempts[0].ExecutionID == outcome.Attempts[1].ExecutionID ||
		outcome.Attempts[0].Result.ExecutionID != outcome.Attempts[0].ExecutionID ||
		outcome.Attempts[1].Result.ExecutionID != outcome.Attempts[1].ExecutionID {
		t.Fatalf("attempt correlation = %+v", outcome.Attempts)
	}
}

func TestCoordinatorBodyScopedDenialFallsBack(t *testing.T) {
	first := &executionBody{id: "body-first"}
	second := &executionBody{id: "body-second"}
	coordinator, resolved := coordinatorFixture(t, []*executionBody{first, second},
		denyRule(first.id, body.AuthorityScopeBody), allowRule(second.id))

	outcome, err := coordinator.Execute(context.Background(), resolved, executionRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertAttempts(t, outcome, []body.BodyID{first.id, second.id}, []body.ExecutionStatus{body.StatusDenied, body.StatusSuccess})
	if first.executions != 0 || second.executions != 1 {
		t.Fatalf("executions first=%d second=%d", first.executions, second.executions)
	}
}

func TestCoordinatorGlobalProhibitionStopsFallback(t *testing.T) {
	first := &executionBody{id: "body-first"}
	second := &executionBody{id: "body-second"}
	coordinator, resolved := coordinatorFixture(t, []*executionBody{first, second},
		denyRule(first.id, body.AuthorityScopeGlobal), allowRule(second.id))

	outcome, err := coordinator.Execute(context.Background(), resolved, executionRequest())
	if !errors.Is(err, bodyresolver.ErrGlobalProhibition) {
		t.Fatalf("error = %v", err)
	}
	assertAttempts(t, outcome, []body.BodyID{first.id}, []body.ExecutionStatus{body.StatusDenied})
	if outcome.Result == nil || outcome.Result.DenialScope != body.AuthorityScopeGlobal || second.executions != 0 {
		t.Fatalf("outcome=%+v second executions=%d", outcome, second.executions)
	}
}

func TestCoordinatorExecutionFailureFallsBack(t *testing.T) {
	first := &executionBody{id: "body-first", executionError: errors.New("device failed")}
	second := &executionBody{id: "body-second"}
	coordinator, resolved := coordinatorFixture(t, []*executionBody{first, second},
		allowRule(first.id), allowRule(second.id))

	outcome, err := coordinator.Execute(context.Background(), resolved, executionRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertAttempts(t, outcome, []body.BodyID{first.id, second.id}, []body.ExecutionStatus{body.StatusFailed, body.StatusSuccess})
}

func TestCoordinatorExhaustionReturnsCompleteHistory(t *testing.T) {
	first := &executionBody{id: "body-first", executionError: errors.New("first failed")}
	second := &executionBody{id: "body-second", executionError: errors.New("second failed")}
	coordinator, resolved := coordinatorFixture(t, []*executionBody{first, second},
		allowRule(first.id), allowRule(second.id))

	outcome, err := coordinator.Execute(context.Background(), resolved, executionRequest())
	if !errors.Is(err, bodyresolver.ErrCandidatesExhausted) {
		t.Fatalf("error = %v", err)
	}
	assertAttempts(t, outcome, []body.BodyID{first.id, second.id}, []body.ExecutionStatus{body.StatusFailed, body.StatusFailed})
	if outcome.Result == nil || outcome.Result.ExecutionID != outcome.Attempts[1].ExecutionID {
		t.Fatalf("terminal result = %+v", outcome.Result)
	}
}

func TestCoordinatorCancellationStopsExecution(t *testing.T) {
	first := &executionBody{id: "body-first", waitForCancel: true, started: make(chan struct{})}
	second := &executionBody{id: "body-second"}
	coordinator, resolved := coordinatorFixture(t, []*executionBody{first, second},
		allowRule(first.id), allowRule(second.id))
	ctx, cancel := context.WithCancel(context.Background())
	type completed struct {
		outcome bodyresolver.ExecutionOutcome
		err     error
	}
	done := make(chan completed, 1)
	go func() {
		outcome, err := coordinator.Execute(ctx, resolved, executionRequest())
		done <- completed{outcome: outcome, err: err}
	}()
	<-first.started
	cancel()
	completedExecution := <-done
	outcome, err := completedExecution.outcome, completedExecution.err
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	assertAttempts(t, outcome, []body.BodyID{first.id}, []body.ExecutionStatus{body.StatusFailed})
	if first.executions != 1 || second.executions != 0 {
		t.Fatalf("outcome=%+v executions=%d/%d", outcome, first.executions, second.executions)
	}
}

func coordinatorFixture(t *testing.T, bodies []*executionBody, rules ...body.AuthorityRule) (*bodyresolver.Coordinator, bodyresolver.Result) {
	t.Helper()
	registry := body.NewRegistry()
	candidates := make([]bodyresolver.Candidate, 0, len(bodies))
	for index, candidateBody := range bodies {
		if err := registry.Register(candidateBody); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, bodyresolver.Candidate{Body: candidateBody, Preference: len(bodies) - index})
	}
	resolved, err := bodyresolver.Resolve(bodyresolver.ActionRequirement{Capability: "test.echo", Operation: "run"},
		bodyresolver.TaskConstraints{}, bodyresolver.OwnerPolicy{}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := bodyresolver.NewCoordinator(body.NewGuard(registry, body.NewRuleEvaluator(rules...)))
	if err != nil {
		t.Fatal(err)
	}
	return coordinator, resolved
}

func executionRequest() bodyresolver.ExecutionRequest {
	return bodyresolver.ExecutionRequest{ExecutionID: "action-123", Doll: "doll-test", Arguments: map[string]any{"value": "hello"}}
}

func allowRule(bodyID body.BodyID) body.AuthorityRule {
	return body.AuthorityRule{Doll: "doll-test", Body: bodyID, Capability: "test.echo", Operation: "run", Decision: body.AuthorityAllow}
}

func denyRule(bodyID body.BodyID, scope body.AuthorityScope) body.AuthorityRule {
	return body.AuthorityRule{Doll: "doll-test", Body: bodyID, Capability: "test.echo", Operation: "run", Decision: body.AuthorityDeny, Reason: "policy_denied", Scope: scope}
}

func assertAttempts(t *testing.T, outcome bodyresolver.ExecutionOutcome, ids []body.BodyID, statuses []body.ExecutionStatus) {
	t.Helper()
	gotIDs := make([]body.BodyID, len(outcome.Attempts))
	gotStatuses := make([]body.ExecutionStatus, len(outcome.Attempts))
	for index, attempt := range outcome.Attempts {
		gotIDs[index] = attempt.BodyID
		gotStatuses[index] = attempt.Result.Status
	}
	if !reflect.DeepEqual(gotIDs, ids) || !reflect.DeepEqual(gotStatuses, statuses) {
		t.Fatalf("attempts ids=%v statuses=%v", gotIDs, gotStatuses)
	}
}
