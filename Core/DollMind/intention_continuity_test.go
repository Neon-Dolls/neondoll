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
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy := &permissionPolicy{decision: actionpolicy.DecisionRequireApproval}
	requests := permissionrequest.New(store.(permissionrequest.Store), policy)
	candidate := &resolverBody{id: "protected-body"}
	registry := body.NewRegistry()
	if err = registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{}), policy, "doll", WithPermissionRequests(requests, "owner", time.Hour))
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"act","matters":true,"reason":"due"}`},
		{ToolCalls: []inference.ToolCall{{ID: "protected-call", Name: "test.echo__run"}}},
		{Content: `{"summary":"approval is pending","proposed_action":"wait for the owner","observations":[],"should_reorient":false,"request_future_cognition":false}`},
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
}
