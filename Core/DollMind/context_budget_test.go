package dollmind

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

func TestCognitionUsesConfiguredContextAndOutputBudgets(t *testing.T) {
	provider := &universalContextProvider{responses: []string{
		`{"summary":"relevant","matters":true,"reason":"test"}`,
		`{"summary":"planned","should_reorient":false,"request_future_cognition":false}`,
	}}
	state := &dollstate.DollState{Version: dollstate.CurrentStateVersion}
	for index := 0; index < 30; index++ {
		state.Memories.Items = append(state.Memories.Items, dollstate.MemoryItem{Content: strings.Repeat("optional memory ", 20)})
	}
	budget := inference.ModelContextBudget{ContextTokens: 1024, OutputTokens: 128, RequiredOverheadTokens: 64}
	scheduler := New(provider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: state}, WithModelContextBudget(budget))

	if _, err := scheduler.Enter(context.Background(), events.TypeMessage, "please think"); err != nil {
		t.Fatal(err)
	}
	requests := provider.capturedRequests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want Orient and Plan", len(requests))
	}
	for index, request := range requests {
		if request.MaxTokens != 128 {
			t.Errorf("request %d MaxTokens = %d, want 128", index, request.MaxTokens)
		}
		if tokens := estimatedInferenceTokens(request.Messages); tokens > 832 {
			t.Errorf("request %d input tokens = %d, want at most 832", index, tokens)
		}
	}
}

func TestDirectCognitionUsesConfiguredOutputReserve(t *testing.T) {
	provider := &universalContextProvider{responses: []string{"response"}}
	budget := inference.ModelContextBudget{ContextTokens: 2048, OutputTokens: 300, RequiredOverheadTokens: 100}
	scheduler := New(provider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}}, WithModelContextBudget(budget))

	if _, err := scheduler.Run(context.Background(), LevelPlan, "direct input"); err != nil {
		t.Fatal(err)
	}
	requests := provider.capturedRequests()
	if len(requests) != 1 || requests[0].MaxTokens != 300 {
		t.Fatalf("requests = %+v, want one request with MaxTokens 300", requests)
	}
}

func TestCognitionRejectsInvalidConfiguredBudget(t *testing.T) {
	provider := &universalContextProvider{responses: []string{"unused"}}
	scheduler := New(provider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}}, WithModelContextBudget(inference.ModelContextBudget{
		ContextTokens: 512, OutputTokens: 512,
	}))

	_, err := scheduler.Run(context.Background(), LevelPlan, "input")
	if !errors.Is(err, inference.ErrInvalidContextBudget) {
		t.Fatalf("Run error = %v, want ErrInvalidContextBudget", err)
	}
	if requests := provider.capturedRequests(); len(requests) != 0 {
		t.Fatalf("provider requests = %d, want 0", len(requests))
	}
}

func TestToolLoopUsesConfiguredBudgetEveryIteration(t *testing.T) {
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"use tools","matters":true,"reason":"inspect"}`},
		{ToolCalls: []inference.ToolCall{{ID: "call_budget", Name: "runtime.info__read"}}},
		{Content: makePlanJSON("complete")},
	}}
	budget := inference.ModelContextBudget{ContextTokens: 1536, OutputTokens: 256, RequiredOverheadTokens: 128}
	scheduler := newPulseToolScheduler(provider, nil, WithModelContextBudget(budget))

	if err := scheduler.EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: 0.8, EffectivePressure: 0.7}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("requests = %d, want Orient plus two Plan iterations", len(provider.requests))
	}
	for index, request := range provider.requests {
		if request.MaxTokens != 256 {
			t.Errorf("request %d MaxTokens = %d, want 256", index, request.MaxTokens)
		}
		if tokens := estimatedInferenceTokens(request.Messages); tokens > 1152 {
			t.Errorf("request %d input tokens = %d, want at most 1152", index, tokens)
		}
	}
}

func (provider *universalContextProvider) capturedRequests() []inference.Request {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]inference.Request(nil), provider.requests...)
}
