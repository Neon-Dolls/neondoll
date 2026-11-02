package dollmind

import (
	"context"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

func TestLifecycleTickAutomaticallyDispatchesDueIntentionOnce(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{DollID: "lifecycle-doll"},
		Intentions: dollstate.Intentions{Items: []dollstate.IntentionItem{{
			ID: "due-once", Subject: "finish work", WakeTime: now.Format(time.RFC3339), State: dollstate.IntentionStatePending,
		}}},
	}
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"due work matters","matters":true,"reason":"scheduled"}`},
		{Content: makePlanJSON("finished")},
	}}
	model := testModelConfiguration("lifecycle", inference.DefaultModelContextBudget())
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state},
		WithTimeProvider(func() time.Time { return now }), WithModelConfiguration(model),
		WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model),
		WithTaskModelConfiguration(config.InferenceTaskPlan, provider, model))
	lifecycle, err := NewLifecycle(scheduler)
	if err != nil {
		t.Fatal(err)
	}
	ticks, acknowledgements := make(chan time.Time, 2), make(chan struct{}, 2)
	runner := pulse.NewTestRunner(config.PulseConfig{Enabled: false}, pulse.NewFakeClock(now), nil,
		logger.New(logger.ErrorLevel, nil), lifecycle, ticks, acknowledgements)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer runner.Stop()
	for range 2 {
		ticks <- now
		select {
		case <-acknowledgements:
		case <-time.After(5 * time.Second):
			t.Fatal("lifecycle tick timed out")
		}
	}
	if state.Intentions.Items[0].State != dollstate.IntentionStateCompleted {
		t.Fatalf("intention state=%q", state.Intentions.Items[0].State)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("inference requests=%d, want one Orient/Plan execution", len(provider.requests))
	}
}
