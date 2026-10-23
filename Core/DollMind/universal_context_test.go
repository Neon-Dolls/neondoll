package dollmind

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type universalContextProvider struct {
	mu        sync.Mutex
	responses []string
	requests  []inference.Request
	runIDs    []string
}

func (provider *universalContextProvider) ID() inference.ProviderID { return "universal-context" }
func (provider *universalContextProvider) Infer(ctx context.Context, request inference.Request) (*inference.Response, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	provider.runIDs = append(provider.runIDs, CognitionRunID(ctx))
	index := len(provider.requests) - 1
	content := "done"
	if index < len(provider.responses) {
		content = provider.responses[index]
	}
	return &inference.Response{Content: content, ProviderID: provider.ID()}, nil
}

func TestDirectRunUsesBoundedIsolatedCognitionRuns(t *testing.T) {
	provider := &universalContextProvider{responses: []string{"first", "second"}}
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "direct-doll", CanonicalName: "Spark"}}
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	for _, input := range []string{"first direct thought", "second direct thought"} {
		if _, err := scheduler.Run(context.Background(), LevelReflect, input); err != nil {
			t.Fatalf("Run(%q): %v", input, err)
		}
	}
	if len(provider.requests) != 2 || provider.runIDs[0] == "" || provider.runIDs[1] == "" || provider.runIDs[0] == provider.runIDs[1] {
		t.Fatalf("direct run identities = %v, want two isolated non-empty IDs", provider.runIDs)
	}
	for index, input := range []string{"first direct thought", "second direct thought"} {
		request := provider.requests[index]
		if !strings.Contains(joinedInferenceContent(request.Messages), input) {
			t.Errorf("direct request %d missing input %q", index, input)
		}
		if tokens := estimatedInferenceTokens(request.Messages); tokens > CognitiveContextTokenBudget {
			t.Errorf("direct request %d tokens = %d, budget = %d", index, tokens, CognitiveContextTokenBudget)
		}
	}
}

func TestInternalIntentionWakeReusesOneBoundedRun(t *testing.T) {
	provider := &universalContextProvider{responses: []string{
		`{"summary":"intention matters","matters":true,"reason":"it is due"}`,
		makePlanJSON("handle due intention"),
	}}
	state := &dollstate.DollState{
		Identity: dollstate.Identity{DollID: "wake-doll", CanonicalName: "Spark"},
		Intentions: dollstate.Intentions{Items: []dollstate.IntentionItem{{
			ID: "intent-1", Subject: "Review the garden", Description: "Check progress", State: dollstate.IntentionStatePending,
		}}},
	}
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	_, err := scheduler.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "intent-1", Subject: "Review the garden", Description: "Check progress",
	})
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}
	if len(provider.requests) != 2 || provider.runIDs[0] == "" || provider.runIDs[0] != provider.runIDs[1] {
		t.Fatalf("intention wake run identities = %v, want one stable ID", provider.runIDs)
	}
	for index, request := range provider.requests {
		content := joinedInferenceContent(request.Messages)
		if !strings.Contains(content, "Review the garden") || !strings.Contains(content, "due internal intention") {
			t.Errorf("wake request %d missing objective/evidence", index)
		}
		if tokens := estimatedInferenceTokens(request.Messages); tokens > CognitiveContextTokenBudget {
			t.Errorf("wake request %d tokens = %d, budget = %d", index, tokens, CognitiveContextTokenBudget)
		}
	}
}

func TestDirectPlanToolLoopReusesRunAndRebuildsContext(t *testing.T) {
	provider := &toolContextProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "direct_A", Name: "runtime.info__read"}}},
		{ToolCalls: []inference.ToolCall{{ID: "direct_B", Name: "runtime.info__list"}}},
		{Content: makePlanJSON("direct tools complete")},
	}}
	scheduler := newPulseToolScheduler(provider, nil)

	_, _, err := scheduler.Plan(context.Background(), events.TypeMessage, "inspect runtime", &Orientation{
		Summary: "runtime inspection", Matters: true, Reason: "requested directly",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("tool iterations = %d, want 3", len(provider.requests))
	}
	for index, request := range provider.requests {
		if provider.runIDs[index] == "" || provider.runIDs[index] != provider.runIDs[0] {
			t.Errorf("tool iteration %d run ID = %q, want %q", index, provider.runIDs[index], provider.runIDs[0])
		}
		if tokens := estimatedInferenceTokens(request.Messages); tokens > CognitiveContextTokenBudget {
			t.Errorf("tool iteration %d tokens = %d, budget = %d", index, tokens, CognitiveContextTokenBudget)
		}
		if len(request.Tools) != 0 || len(request.ToolResults) != 0 {
			t.Errorf("tool iteration %d bypassed budget with legacy fields", index)
		}
	}
}
