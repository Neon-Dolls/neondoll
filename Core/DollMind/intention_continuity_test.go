package dollmind

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	actionpolicy "github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	permissionrequest "github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

func TestDueIntentionActsAndKeepsIdentityWhenWorkRemains(t *testing.T) {
	now := time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC)
	nextWake := now.Add(time.Hour).Format(time.RFC3339)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{DollID: "continuity-doll", CanonicalName: "Continuity"},
		Intentions: dollstate.Intentions{Items: []dollstate.IntentionItem{{
			ID: "intention-1", Subject: "inspect the environment", Description: "act and reassess",
			WakeTime: now.Add(-time.Minute).Format(time.RFC3339), State: dollstate.IntentionStatePending,
		}}},
	}
	api := &orientMockAPI{state: state}
	candidate := &resolverBody{id: "sensor-body"}
	registry := body.NewRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	policy := &permissionPolicy{decision: actionpolicy.DecisionAllow}
	executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{
		candidate.id: {Decision: body.AuthorityAllow},
	}}), policy, "continuity-doll")
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"the intention deserves action","matters":true,"reason":"observation is needed"}`},
		{ToolCalls: []inference.ToolCall{{ID: "observe-1", Name: "test.echo__run", Arguments: map[string]any{"value": "inspect"}}}},
		{Content: `{"summary":"observation succeeded but work remains","proposed_action":"reassess later","observations":["partial progress"],"retain_experience":["environment was inspected"],"should_reorient":true,"request_future_cognition":true,"future_subject":"reassess the environment","future_reason":"use the observation to decide the next action","future_wake_time":"` + nextWake + `"}`},
	}}
	model := testModelConfiguration("intention-model", inference.DefaultModelContextBudget())
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), api,
		WithTimeProvider(func() time.Time { return now }),
		WithModelConfiguration(model),
		WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model),
		WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model),
		WithToolExecutor(executor))

	result, err := scheduler.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "intention-1", Subject: "inspect the environment", Description: "act and reassess",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Level != LevelPlan || candidate.executions != 1 {
		t.Fatalf("result=%#v executions=%d", result, candidate.executions)
	}
	if len(state.Intentions.Items) != 1 {
		t.Fatalf("intentions=%#v", state.Intentions.Items)
	}
	got := state.Intentions.Items[0]
	if got.ID != "intention-1" || got.State != dollstate.IntentionStatePending || got.WakeTime != nextWake || got.Subject != "reassess the environment" {
		t.Fatalf("rescheduled intention=%#v", got)
	}
	if len(state.Memories.Items) != 1 || state.Memories.Items[0].Content != "environment was inspected" {
		t.Fatalf("memories=%#v", state.Memories.Items)
	}
	now = now.Add(2 * time.Hour)
	due, err := scheduler.DueIntentions()
	if err != nil || len(due) != 1 || due[0].ID != "intention-1" {
		t.Fatalf("due=%#v err=%v", due, err)
	}
}

func TestDueIntentionPermissionRequestKeepsIntentionCorrelation(t *testing.T) {
	now := time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC)
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "doll"}, Intentions: dollstate.Intentions{Items: []dollstate.IntentionItem{{
		ID: "intent-permission", Subject: "perform protected work", WakeTime: now.Add(-time.Minute).Format(time.RFC3339), State: dollstate.IntentionStatePending,
	}}}}
	api := &orientMockAPI{state: state}
	database := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(database)
	if err != nil {
		t.Fatal(err)
	}
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	candidate := &resolverBody{id: "protected-body"}
	registry := body.NewRegistry()
	if err = registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	authority := &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{candidate.id: {Decision: body.AuthorityAllow}}}
	executor := NewToolExecutor(registry, body.NewGuard(registry, authority), policy, "doll", WithPermissionRequests(requests, "owner", time.Hour))
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"act","matters":true,"reason":"due"}`},
		{ToolCalls: []inference.ToolCall{{ID: "protected-call", Name: "test.echo__run"}}},
		{Content: `{"summary":"approval is pending","proposed_action":"wait for the owner","observations":[],"should_reorient":false,"request_future_cognition":false}`},
		{Content: `{"summary":"reconsider the approved result","matters":true,"reason":"the action outcome is now available"}`},
		{Content: `{"summary":"the intention is fulfilled","proposed_action":"stop","observations":["approved work completed"],"should_reorient":false,"request_future_cognition":false}`},
	}}
	model := testModelConfiguration("permission", inference.DefaultModelContextBudget())
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), api, WithTimeProvider(func() time.Time { return now }), WithModelConfiguration(model),
		WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model), WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model), WithToolExecutor(executor))
	if _, err = scheduler.EnterWake(context.Background(), events.IntentionWakePayload{IntentionID: "intent-permission", Subject: "perform protected work"}); err != nil {
		t.Fatal(err)
	}
	pending, err := requests.ListPending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if pending[0].Correlation.IntentionID != "intent-permission" {
		t.Fatalf("correlation=%#v", pending[0].Correlation)
	}
	if state.Intentions.Items[0].State != dollstate.IntentionStatePending {
		t.Fatalf("intention=%#v", state.Intentions.Items[0])
	}
	due, err := scheduler.DueIntentions()
	if err != nil || len(due) != 0 {
		t.Fatalf("permission-blocked due=%#v err=%v", due, err)
	}
	if candidate.executions != 0 {
		t.Fatalf("protected action repeated while pending: %d", candidate.executions)
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
	blocked, err := restartedRequests.HasUnresolvedIntention(context.Background(), "intent-permission", "")
	if err != nil || !blocked {
		t.Fatalf("restart blocked=%v err=%v", blocked, err)
	}
	restartedExecutor := NewToolExecutor(registry, body.NewGuard(registry, authority), policy, "doll", WithPermissionRequests(restartedRequests, "owner", time.Hour))
	restartedScheduler := New(provider, logger.New(logger.ErrorLevel, nil), api, WithTimeProvider(func() time.Time { return now }), WithModelConfiguration(model),
		WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model), WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model), WithToolExecutor(restartedExecutor))
	restartedExecutor.SetPermissionResolutionNotifier(restartedScheduler)
	approved, err := restartedRequests.Approve(context.Background(), pending[0].ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = restartedExecutor.HandlePermissionResolution(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	if candidate.executions != 1 {
		t.Fatalf("approved executions=%d", candidate.executions)
	}
	if state.Intentions.Items[0].State != dollstate.IntentionStateCompleted {
		t.Fatalf("resolved intention=%#v", state.Intentions.Items[0])
	}
	if err = restartedExecutor.HandlePermissionResolution(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	if candidate.executions != 1 {
		t.Fatalf("replayed approval executions=%d", candidate.executions)
	}
}

func TestDeniedPermissionReturnsToOriginalIntentionWithoutExecution(t *testing.T) {
	now := time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC)
	nextWake := now.Add(2 * time.Hour).Format(time.RFC3339)
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "doll"}, Intentions: dollstate.Intentions{Items: []dollstate.IntentionItem{{
		ID: "denied-intention", Subject: "protected work", WakeTime: now.Add(-time.Minute).Format(time.RFC3339), State: dollstate.IntentionStatePending,
	}}}}
	api := &orientMockAPI{state: state}
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	request, err := requests.Create(context.Background(), permissionrequest.CreateRequest{
		DollID: "doll", OwnerID: "owner", Origin: permissionrequest.OriginPolicyRequired, Action: "test.echo__run",
		Capability: "test.echo", Operation: "run", ExpiresAt: now.Add(time.Hour), Correlation: permissionrequest.Correlation{IntentionID: "denied-intention", ToolCallID: "denied-call"},
	})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := requests.Deny(context.Background(), request.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	candidate := &resolverBody{id: "never-run"}
	registry := body.NewRegistry()
	if err = registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{}), policy, "doll", WithPermissionRequests(requests, "owner", time.Hour))
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"the owner denied this approach","matters":true,"reason":"choose what remains"}`},
		{Content: `{"summary":"defer and reconsider another approach","proposed_action":"do not retry the denied action","observations":[],"should_reorient":true,"request_future_cognition":true,"future_subject":"find another approach","future_reason":"the protected action was denied","future_wake_time":"` + nextWake + `"}`},
	}}
	model := testModelConfiguration("denial", inference.DefaultModelContextBudget())
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), api, WithTimeProvider(func() time.Time { return now }), WithModelConfiguration(model),
		WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model), WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model), WithToolExecutor(executor))
	executor.SetPermissionResolutionNotifier(scheduler)
	if err = executor.HandlePermissionResolution(context.Background(), denied); err != nil {
		t.Fatal(err)
	}
	if candidate.executions != 0 {
		t.Fatalf("denied action executions=%d", candidate.executions)
	}
	got := state.Intentions.Items[0]
	if got.ID != "denied-intention" || got.State != dollstate.IntentionStatePending || got.WakeTime != nextWake {
		t.Fatalf("intention=%#v", got)
	}
}
