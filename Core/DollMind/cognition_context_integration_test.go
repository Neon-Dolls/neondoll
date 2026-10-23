package dollmind

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type cognitionContextCall struct {
	runID    string
	purpose  inference.Purpose
	messages []inference.Message
}

type cognitionContextProvider struct {
	mu    sync.Mutex
	calls []cognitionContextCall
}

func (provider *cognitionContextProvider) ID() inference.ProviderID { return "context-test" }

func (provider *cognitionContextProvider) Infer(ctx context.Context, request inference.Request) (*inference.Response, error) {
	provider.mu.Lock()
	provider.calls = append(provider.calls, cognitionContextCall{
		runID:    CognitionRunID(ctx),
		purpose:  request.Purpose,
		messages: append([]inference.Message(nil), request.Messages...),
	})
	provider.mu.Unlock()
	if request.Purpose == inference.PurposeOrient {
		return &inference.Response{Content: `{"summary":"pulse orientation","matters":true,"reason":"a goal needs attention"}`}, nil
	}
	return &inference.Response{Content: `{"summary":"continue deliberately","observations":[],"request_future_cognition":false}`}, nil
}

func (provider *cognitionContextProvider) snapshot() []cognitionContextCall {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]cognitionContextCall(nil), provider.calls...)
}

func TestPulseExecutionUsesOneBoundedCognitionRunAcrossOrientAndPlan(t *testing.T) {
	state := &dollstate.DollState{
		Identity: dollstate.Identity{DollID: "doll-context", CanonicalName: "Spark"},
		Soul:     dollstate.Soul{Content: "curious and careful"},
	}
	for index := 0; index < 500; index++ {
		state.Goals.Items = append(state.Goals.Items, dollstate.GoalItem{
			Name:        "goal",
			Description: strings.Repeat("large optional context ", 20),
		})
	}
	provider := &cognitionContextProvider{}
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})
	wake := pulse.PulseWake{Pressure: 0.72, EffectivePressure: 0.61, RandomSample: 0.04}

	if err := scheduler.EnterPulseWake(context.Background(), wake); err != nil {
		t.Fatalf("EnterPulseWake: %v", err)
	}
	calls := provider.snapshot()
	if len(calls) != 2 {
		t.Fatalf("inference calls = %d, want Orient and Plan", len(calls))
	}
	if calls[0].runID == "" || calls[1].runID != calls[0].runID {
		t.Fatalf("run IDs = %q, %q; want one non-empty identity", calls[0].runID, calls[1].runID)
	}
	if calls[0].purpose != inference.PurposeOrient || calls[1].purpose != inference.PurposePlan {
		t.Fatalf("purposes = %q, %q", calls[0].purpose, calls[1].purpose)
	}
	orientContext := joinedInferenceContent(calls[0].messages)
	for _, required := range []string{"doll-context", "Spark", "Cognition Run objective", "Operational wake evidence", "0.7200"} {
		if !strings.Contains(orientContext, required) {
			t.Errorf("Orient context missing %q", required)
		}
	}
	if strings.Contains(orientContext, strings.Repeat("large optional context ", 20)) {
		t.Error("oversized optional Goals should not displace bounded required context")
	}
	if got := len([]rune(orientContext)); got > CognitiveContextTokenBudget*4 {
		t.Fatalf("bounded context has %d runes, budget approximation permits at most %d", got, CognitiveContextTokenBudget*4)
	}
	if planContext := joinedInferenceContent(calls[1].messages); !strings.Contains(planContext, "L1 Orientation") || !strings.Contains(planContext, "pulse orientation") {
		t.Fatalf("Plan context does not include run-local L1 working state: %s", planContext)
	}
}

func TestSeparatePulseCognitionRunsAreIsolated(t *testing.T) {
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "isolated", CanonicalName: "Spark"}}
	provider := &cognitionContextProvider{}
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state})

	for _, pressure := range []float64{0.31, 0.89} {
		if err := scheduler.EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: pressure, EffectivePressure: pressure}); err != nil {
			t.Fatalf("EnterPulseWake(%v): %v", pressure, err)
		}
	}
	calls := provider.snapshot()
	if len(calls) != 4 {
		t.Fatalf("inference calls = %d, want four", len(calls))
	}
	if calls[0].runID != calls[1].runID || calls[2].runID != calls[3].runID || calls[0].runID == calls[2].runID {
		t.Fatalf("run identities are not stable and isolated: %q %q %q %q", calls[0].runID, calls[1].runID, calls[2].runID, calls[3].runID)
	}
	if strings.Contains(joinedInferenceContent(calls[2].messages), "0.3100") {
		t.Error("second cognition run inherited first run's wake evidence")
	}
}

func joinedInferenceContent(messages []inference.Message) string {
	parts := make([]string, len(messages))
	for index, message := range messages {
		parts[index] = message.Content
	}
	return strings.Join(parts, "\n")
}
