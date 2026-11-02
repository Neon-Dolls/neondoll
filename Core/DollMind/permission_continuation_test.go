package dollmind

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type permissionPolicy struct {
	mu       sync.Mutex
	decision actionpolicy.Decision
}

func (policy *permissionPolicy) Evaluate(actionpolicy.Request) actionpolicy.Result {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	return actionpolicy.Result{Decision: policy.decision, Message: "Owner approval required"}
}

func (policy *permissionPolicy) set(decision actionpolicy.Decision) {
	policy.mu.Lock()
	policy.decision = decision
	policy.mu.Unlock()
}

type permissionNotifier struct {
	mu       sync.Mutex
	requests []permissionrequest.ID
	outcomes []permissionrequest.ContinuationOutcome
	failures int
}

type recoveryStore struct {
	permissionrequest.Store
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	failures  int
	started   chan struct{}
	startOnce sync.Once
	release   chan struct{}
}

func (store *recoveryStore) RecoverInterruptedPermissionContinuations(ctx context.Context, result json.RawMessage, at time.Time) (int64, error) {
	store.mu.Lock()
	store.calls++
	store.active++
	if store.active > store.maxActive {
		store.maxActive = store.active
	}
	fail := store.failures > 0
	if fail {
		store.failures--
	}
	started, release := store.started, store.release
	store.mu.Unlock()
	if started != nil {
		store.startOnce.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			store.finishRecoveryCall()
			return 0, ctx.Err()
		}
	}
	defer store.finishRecoveryCall()
	if fail {
		return 0, errors.New("transient recovery failure")
	}
	return store.Store.RecoverInterruptedPermissionContinuations(ctx, result, at)
}

func (store *recoveryStore) finishRecoveryCall() {
	store.mu.Lock()
	store.active--
	store.mu.Unlock()
}

func (store *recoveryStore) snapshot() (calls, maxActive int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.calls, store.maxActive
}

func newRecoveryExecutor(t *testing.T, store permissionrequest.Store) (*ToolExecutor, *permissionrequest.Manager) {
	t.Helper()
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store, policy)
	registry := body.NewRegistry()
	return NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{}), policy, "doll",
		WithPermissionRequests(requests, "owner", time.Hour)), requests
}

func (notifier *permissionNotifier) NotifyPermissionResolution(_ context.Context, _ string, request *permissionrequest.Request, outcome permissionrequest.ContinuationOutcome) error {
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if notifier.failures > 0 {
		notifier.failures--
		return errors.New("cognition unavailable")
	}
	notifier.requests = append(notifier.requests, request.ID)
	notifier.outcomes = append(notifier.outcomes, outcome)
	return nil
}

func TestPermissionOutcomeNotificationRetriesWithoutRepeatingExecution(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	candidate := &resolverBody{id: "once"}
	registry := body.NewRegistry()
	if err = registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{"once": {Decision: body.AuthorityAllow}}}), policy, "doll", WithPermissionRequests(requests, "owner", time.Hour))
	notifier := &permissionNotifier{failures: 1}
	executor.SetPermissionResolutionNotifier(notifier)
	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call"}, echoTool(""))
	id := permissionrequest.ID(result.Error.PermissionRequestID)
	resolved, err := requests.Approve(context.Background(), id, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = executor.HandlePermissionResolution(context.Background(), resolved); err == nil {
		t.Fatal("expected notification failure")
	}
	if candidate.executions != 1 {
		t.Fatalf("executions=%d", candidate.executions)
	}
	stored, err := requests.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ContinuationStatus != permissionrequest.ContinuationSucceeded || stored.NotificationStatus != permissionrequest.NotificationPending {
		t.Fatalf("stored=%#v", stored)
	}
	if err = executor.ReplayPermissionResolutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if candidate.executions != 1 {
		t.Fatalf("notification retry repeated execution: %d", candidate.executions)
	}
	stored, err = requests.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NotificationStatus != permissionrequest.NotificationDelivered || len(notifier.outcomes) != 1 {
		t.Fatalf("stored=%#v outcomes=%#v", stored, notifier.outcomes)
	}
	if err = executor.HandlePermissionResolution(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	if candidate.executions != 1 || len(notifier.outcomes) != 1 {
		t.Fatalf("replay executions=%d outcomes=%d", candidate.executions, len(notifier.outcomes))
	}
}

func TestPermissionRecoveryInitializationRetriesAfterFailure(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wrapped := &recoveryStore{Store: store.(permissionrequest.Store), failures: 1}
	executor, _ := newRecoveryExecutor(t, wrapped)
	if err = executor.ReplayPermissionResolutions(context.Background()); err == nil {
		t.Fatal("first recovery unexpectedly succeeded")
	}
	if err = executor.ReplayPermissionResolutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = executor.ReplayPermissionResolutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls, _ := wrapped.snapshot(); calls != 2 {
		t.Fatalf("recovery calls=%d, want failed attempt plus one success", calls)
	}
}

func TestPermissionRecoveryInitializationRetriesAfterCanceledContext(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wrapped := &recoveryStore{Store: store.(permissionrequest.Store)}
	executor, _ := newRecoveryExecutor(t, wrapped)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = executor.ReplayPermissionResolutions(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery error=%v", err)
	}
	if err = executor.ReplayPermissionResolutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls, _ := wrapped.snapshot(); calls != 2 {
		t.Fatalf("recovery calls=%d, want retry after cancellation", calls)
	}
}

func TestPermissionRecoveryInitializationSerializesConcurrentCallers(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wrapped := &recoveryStore{Store: store.(permissionrequest.Store), started: make(chan struct{}), release: make(chan struct{})}
	executor, _ := newRecoveryExecutor(t, wrapped)
	errorsSeen := make(chan error, 8)
	for range 8 {
		go func() { errorsSeen <- executor.ReplayPermissionResolutions(context.Background()) }()
	}
	select {
	case <-wrapped.started:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not start")
	}
	close(wrapped.release)
	for range 8 {
		if replayErr := <-errorsSeen; replayErr != nil {
			t.Fatal(replayErr)
		}
	}
	if calls, maxActive := wrapped.snapshot(); calls != 1 || maxActive != 1 {
		t.Fatalf("recovery calls=%d max concurrent=%d, want 1/1", calls, maxActive)
	}
}

func TestRestartMarksClaimedApprovedContinuationAmbiguousWithoutReexecution(t *testing.T) {
	database := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	candidate := &resolverBody{id: "must-not-replay"}
	registry := body.NewRegistry()
	if err = registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{"must-not-replay": {Decision: body.AuthorityAllow}}}), policy, "doll", WithPermissionRequests(requests, "owner", time.Hour))
	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call"}, echoTool(""))
	id := permissionrequest.ID(result.Error.PermissionRequestID)
	if _, err = requests.Approve(context.Background(), id, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = requests.ClaimContinuation(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := persistence.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restartedRequests := permissionrequest.New(reopened.(permissionrequest.Store), policy)
	restarted := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{"must-not-replay": {Decision: body.AuthorityAllow}}}), policy, "doll", WithPermissionRequests(restartedRequests, "owner", time.Hour))
	notifier := &permissionNotifier{}
	restarted.SetPermissionResolutionNotifier(notifier)
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsSeen <- restarted.ReplayPermissionResolutions(context.Background())
		}()
	}
	wg.Wait()
	close(errorsSeen)
	for replayErr := range errorsSeen {
		if replayErr != nil {
			t.Fatal(replayErr)
		}
	}
	if candidate.executions != 0 {
		t.Fatalf("ambiguous action replayed %d times", candidate.executions)
	}
	stored, err := restartedRequests.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var outcome permissionrequest.ContinuationOutcome
	if err = json.Unmarshal(stored.ContinuationResult, &outcome); err != nil {
		t.Fatal(err)
	}
	if stored.ContinuationStatus != permissionrequest.ContinuationFailed || outcome.ErrorCode != "execution_outcome_ambiguous" {
		t.Fatalf("recovered continuation=%#v outcome=%#v", stored, outcome)
	}
	if len(notifier.outcomes) != 1 {
		t.Fatalf("outcome notifications=%d, want 1", len(notifier.outcomes))
	}
}

func TestApprovedContinuationWaitsForTemporarilyUnavailableBody(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	registry := body.NewRegistry()
	authority := &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{"returning": {Decision: body.AuthorityAllow}}}
	executor := NewToolExecutor(registry, body.NewGuard(registry, authority), policy, "doll", WithPermissionRequests(requests, "owner", time.Hour))
	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call"}, echoTool(""))
	id := permissionrequest.ID(result.Error.PermissionRequestID)
	approved, err := requests.Approve(context.Background(), id, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = executor.HandlePermissionResolution(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	stored, err := requests.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ContinuationStatus != permissionrequest.ContinuationReady {
		t.Fatalf("unavailable continuation status=%q, want ready", stored.ContinuationStatus)
	}
	candidate := &resolverBody{id: "returning"}
	if err = registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	notifier := &permissionNotifier{}
	executor.SetPermissionResolutionNotifier(notifier)
	if err = executor.ReplayPermissionResolutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if candidate.executions != 1 {
		t.Fatalf("executions after Body return=%d, want 1", candidate.executions)
	}
	stored, err = requests.Get(context.Background(), id)
	if err != nil || stored.ContinuationStatus != permissionrequest.ContinuationSucceeded {
		t.Fatalf("completed continuation=%#v error=%v", stored, err)
	}
}

func TestRestartSafelyRecoversClaimedDeniedContinuation(t *testing.T) {
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	candidate := &resolverBody{id: "never-run"}
	registry := body.NewRegistry()
	if err = registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{}), policy, "doll",
		WithPermissionRequests(requests, "owner", time.Hour))
	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "denied-call"}, echoTool(""))
	id := permissionrequest.ID(result.Error.PermissionRequestID)
	if _, err = requests.Deny(context.Background(), id, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err = requests.ClaimContinuation(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	notifier := &permissionNotifier{}
	executor.SetPermissionResolutionNotifier(notifier)
	if err = executor.ReplayPermissionResolutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if candidate.executions != 0 {
		t.Fatalf("denied continuation executed %d times", candidate.executions)
	}
	stored, err := requests.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var outcome permissionrequest.ContinuationOutcome
	if err = json.Unmarshal(stored.ContinuationResult, &outcome); err != nil {
		t.Fatal(err)
	}
	if stored.ContinuationStatus != permissionrequest.ContinuationFailed || outcome.ErrorCode != "owner_denied" {
		t.Fatalf("denied recovery=%#v outcome=%#v", stored, outcome)
	}
}

func TestPulseAutonomousActionCreatesDurableApprovalAndResumesAfterRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	bodyFixture := &resolverBody{id: "approval-body"}
	registry := body.NewRegistry()
	if err = registry.Register(bodyFixture); err != nil {
		t.Fatal(err)
	}
	authority := &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{
		bodyFixture.id: {Decision: body.AuthorityAllow},
	}}
	executor := NewToolExecutor(registry, body.NewGuard(registry, authority), policy, "autonomous-doll",
		WithPermissionRequests(requests, "owner-1", time.Hour))
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"act autonomously","matters":true,"reason":"goal requires action"}`},
		{ToolCalls: []inference.ToolCall{{ID: "pulse-action", Name: "test.echo__run", Arguments: map[string]any{"value": "hello"}}}},
		{Content: makePlanJSON("approval requested")},
	}}
	model := testModelConfiguration("permission-plan", inference.DefaultModelContextBudget())
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "autonomous-doll", CanonicalName: "Autonomous"}}
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state},
		WithModelConfiguration(model),
		WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model),
		WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model),
		WithToolExecutor(executor))
	if err = scheduler.EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: 0.9, EffectivePressure: 0.8}); err != nil {
		t.Fatal(err)
	}
	if bodyFixture.executions != 0 {
		t.Fatalf("Body executed before approval %d times", bodyFixture.executions)
	}
	pending, err := requests.ListPending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v error=%v", pending, err)
	}
	request := pending[0]
	if request.Correlation.CognitiveRunID == "" || request.Correlation.ToolCallID != "pulse-action" || request.ContinuationStatus != permissionrequest.ContinuationWaiting {
		t.Fatalf("request=%#v", request)
	}
	if len(provider.requests) != 3 || len(messageToolResults(provider.requests[2].Messages)) != 1 {
		t.Fatalf("provider requests=%#v", provider.requests)
	}
	toolResult := messageToolResults(provider.requests[2].Messages)[0]
	if toolResult.Error == nil || toolResult.Error.PermissionRequestID != string(request.ID) {
		t.Fatalf("approval tool result=%#v", toolResult)
	}

	approved, err := requests.Approve(context.Background(), request.ID, "owner-1")
	if err != nil || approved.ContinuationStatus != permissionrequest.ContinuationReady {
		t.Fatalf("approved=%#v error=%v", approved, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := persistence.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restartedRequests := permissionrequest.New(reopened.(permissionrequest.Store), policy)
	restartedExecutor := NewToolExecutor(registry, body.NewGuard(registry, authority), policy, "autonomous-doll",
		WithPermissionRequests(restartedRequests, "owner-1", time.Hour))
	notifier := &permissionNotifier{}
	restartedExecutor.SetPermissionResolutionNotifier(notifier)
	if err = restartedExecutor.ResumeReadyPermissions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bodyFixture.executions != 1 {
		t.Fatalf("approved Body executions=%d", bodyFixture.executions)
	}
	if err = restartedExecutor.HandlePermissionResolution(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	if bodyFixture.executions != 1 {
		t.Fatalf("replayed decision executed Body %d times", bodyFixture.executions)
	}
	stored, err := restartedRequests.Get(context.Background(), request.ID)
	if err != nil || stored.ContinuationStatus != permissionrequest.ContinuationSucceeded {
		t.Fatalf("stored=%#v error=%v", stored, err)
	}
	if len(notifier.outcomes) != 1 || !notifier.outcomes[0].Success {
		t.Fatalf("notifications=%#v", notifier.outcomes)
	}
}

func TestPermissionContinuationRechecksPolicyAndDenialNeverDispatches(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision permissionrequest.Decision
		mutate   actionpolicy.Decision
		code     string
	}{
		{name: "owner denial", decision: permissionrequest.DecisionDeny, mutate: actionpolicy.DecisionRequireApproval, code: "owner_denied"},
		{name: "policy becomes prohibited", decision: permissionrequest.DecisionApprove, mutate: actionpolicy.DecisionDeny, code: "action_denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
			requests := permissionrequest.New(store.(permissionrequest.Store), policy)
			candidate := &resolverBody{id: "never-execute"}
			registry := body.NewRegistry()
			if err = registry.Register(candidate); err != nil {
				t.Fatal(err)
			}
			executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{}), policy, "doll",
				WithPermissionRequests(requests, "owner", time.Hour))
			notifier := &permissionNotifier{}
			executor.SetPermissionResolutionNotifier(notifier)
			result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call"}, echoTool(""))
			if result.Error == nil || result.Error.PermissionRequestID == "" {
				t.Fatalf("request result=%#v", result)
			}
			id := permissionrequest.ID(result.Error.PermissionRequestID)
			policy.set(test.mutate)
			var resolved *permissionrequest.Request
			if test.decision == permissionrequest.DecisionApprove {
				resolved, err = requests.Approve(context.Background(), id, "owner")
			} else {
				resolved, err = requests.Deny(context.Background(), id, "owner")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = executor.HandlePermissionResolution(context.Background(), resolved); err != nil {
				t.Fatal(err)
			}
			if candidate.executions != 0 {
				t.Fatalf("prohibited action executed %d times", candidate.executions)
			}
			stored, err := requests.Get(context.Background(), id)
			if err != nil || stored.ContinuationStatus != permissionrequest.ContinuationFailed {
				t.Fatalf("stored=%#v error=%v", stored, err)
			}
			var outcome permissionrequest.ContinuationOutcome
			if err = json.Unmarshal(stored.ContinuationResult, &outcome); err != nil || outcome.ErrorCode != test.code {
				t.Fatalf("outcome=%#v error=%v", outcome, err)
			}
			if len(notifier.outcomes) != 1 || notifier.outcomes[0].ErrorCode != test.code {
				t.Fatalf("notifications=%#v", notifier.outcomes)
			}
		})
	}
}
