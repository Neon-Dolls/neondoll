package dollmind

import (
	"context"
	"encoding/json"
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
}

func (notifier *permissionNotifier) NotifyPermissionResolution(_ context.Context, request *permissionrequest.Request, outcome permissionrequest.ContinuationOutcome) error {
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	notifier.requests = append(notifier.requests, request.ID)
	notifier.outcomes = append(notifier.outcomes, outcome)
	return nil
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
