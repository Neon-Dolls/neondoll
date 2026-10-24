package dollmind

import (
	"context"
	"io"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

func TestCognitionRoutesEachTaskWithPairedModelBudget(t *testing.T) {
	directProvider := &universalContextProvider{responses: []string{"direct response"}}
	orientProvider := &universalContextProvider{responses: []string{`{"summary":"route","matters":true,"reason":"test"}`}}
	planProvider := &universalContextProvider{responses: []string{makePlanJSON("planned")}}
	directModel := testModelConfiguration("direct-model", inference.ModelContextBudget{ContextTokens: 2048, OutputTokens: 300, RequiredOverheadTokens: 100})
	orientModel := testModelConfiguration("orient-model", inference.ModelContextBudget{ContextTokens: 1024, OutputTokens: 128, RequiredOverheadTokens: 64})
	planModel := testModelConfiguration("plan-model", inference.ModelContextBudget{ContextTokens: 1536, OutputTokens: 256, RequiredOverheadTokens: 128})
	scheduler := New(directProvider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}},
		WithModelConfiguration(directModel),
		WithTaskModelConfiguration(config.InferenceTaskOrient, orientProvider, orientModel),
		WithTaskModelConfiguration(config.InferenceTaskPlan, planProvider, planModel),
	)

	if err := scheduler.EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: 0.8, EffectivePressure: 0.7}); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Run(context.Background(), LevelReflect, "direct thought"); err != nil {
		t.Fatal(err)
	}
	assertTaskRequest(t, orientProvider.capturedRequests(), "orient-model", 128, 832)
	assertTaskRequest(t, planProvider.capturedRequests(), "plan-model", 256, 1152)
	assertTaskRequest(t, directProvider.capturedRequests(), "direct-model", 300, 1648)
}

func assertTaskRequest(t *testing.T, requests []inference.Request, model string, outputBudget, inputBudget int) {
	t.Helper()
	if len(requests) != 1 {
		t.Fatalf("%s requests = %d, want 1", model, len(requests))
	}
	request := requests[0]
	if request.Model != model || request.MaxTokens != outputBudget {
		t.Fatalf("%s request = %+v", model, request)
	}
	if tokens := estimatedInferenceTokens(request.Messages); tokens > inputBudget {
		t.Fatalf("%s input tokens = %d, want at most %d", model, tokens, inputBudget)
	}
}

func TestToolLoopRetainsPlanTaskConfiguration(t *testing.T) {
	planProvider := &toolContextProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "call_task", Name: "runtime.info__read"}}},
		{Content: makePlanJSON("complete")},
	}}
	planModel := testModelConfiguration("task-plan-model", inference.ModelContextBudget{
		ContextTokens: 1536, OutputTokens: 240, RequiredOverheadTokens: 96,
	})
	scheduler := newPulseToolScheduler(planProvider, nil,
		WithTaskModelConfiguration(config.InferenceTaskPlan, planProvider, planModel))

	_, _, err := scheduler.Plan(context.Background(), events.TypeMessage, "use a tool", &Orientation{Summary: "tool", Matters: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(planProvider.requests) != 2 {
		t.Fatalf("Plan requests = %d, want 2", len(planProvider.requests))
	}
	for index, request := range planProvider.requests {
		if request.Model != "task-plan-model" || request.MaxTokens != 240 {
			t.Errorf("iteration %d request = %+v", index, request)
		}
		if tokens := estimatedInferenceTokens(request.Messages); tokens > 1200 {
			t.Errorf("iteration %d input tokens = %d, want at most 1200", index, tokens)
		}
	}
}
