package dollmind

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	body "github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type toolContextProvider struct {
	mu        sync.Mutex
	responses []*inference.Response
	requests  []inference.Request
	runIDs    []string
}

func (provider *toolContextProvider) ID() inference.ProviderID { return "tool-context-test" }

func (provider *toolContextProvider) Infer(ctx context.Context, request inference.Request) (*inference.Response, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	provider.runIDs = append(provider.runIDs, CognitionRunID(ctx))
	index := len(provider.requests) - 1
	if index >= len(provider.responses) {
		return &inference.Response{Content: makePlanJSON("fallback")}, nil
	}
	return provider.responses[index], nil
}

func TestPulseToolLoopRebuildsBoundedContextEachIteration(t *testing.T) {
	provider := &toolContextProvider{responses: []*inference.Response{
		{Content: `{"summary":"use tools","matters":true,"reason":"inspect runtime"}`},
		{ToolCalls: []inference.ToolCall{{ID: "call_A", Name: "runtime.info__read"}}},
		{ToolCalls: []inference.ToolCall{{ID: "call_B", Name: "runtime.info__list"}}},
		{Content: makePlanJSON("tools complete")},
	}}
	scheduler := newPulseToolScheduler(provider, nil)

	if err := scheduler.EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: 0.8, EffectivePressure: 0.7}); err != nil {
		t.Fatalf("EnterPulseWake: %v", err)
	}
	if len(provider.requests) != 4 {
		t.Fatalf("inference requests = %d, want Orient plus three Plan iterations", len(provider.requests))
	}
	for index, runID := range provider.runIDs {
		if runID == "" || runID != provider.runIDs[0] {
			t.Fatalf("request %d run ID = %q, want stable %q", index, runID, provider.runIDs[0])
		}
	}
	for index, request := range provider.requests[1:] {
		if tokens := estimatedInferenceTokens(request.Messages); tokens > defaultCognitiveInputBudget {
			t.Errorf("Plan iteration %d estimate = %d, budget = %d", index, tokens, defaultCognitiveInputBudget)
		}
		if len(request.Tools) != 0 || len(request.ToolResults) != 0 {
			t.Errorf("Plan iteration %d carried legacy unbudgeted tool fields", index)
		}
	}
	if got := messageToolResults(provider.requests[2].Messages); len(got) != 1 || got[0].ToolCallID != "call_A" {
		t.Fatalf("second Plan iteration message results = %+v, want only call_A", got)
	}
	if got := messageToolResults(provider.requests[3].Messages); len(got) != 2 || got[0].ToolCallID != "call_A" || got[1].ToolCallID != "call_B" {
		t.Fatalf("third Plan iteration message results = %+v, want correlated call_A and call_B history", got)
	}
	lastContext := joinedInferenceContent(provider.requests[3].Messages)
	for _, evidence := range []string{"call_A", "call_B", "Tool result for call call_B"} {
		if !strings.Contains(lastContext, evidence) {
			t.Errorf("rebuilt context missing correlated evidence %q", evidence)
		}
	}
}

func TestOrientUsesUnifiedMultiStepExecution(t *testing.T) {
	provider := &toolContextProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "orient_A", Name: "runtime.info__read"}}},
		{ToolCalls: []inference.ToolCall{{ID: "orient_B", Name: "runtime.info__list"}}},
		{Content: `{"summary":"oriented with tools","matters":false,"reason":"complete"}`},
	}}
	model := testModelConfiguration("orient-tools", inference.ModelContextBudget{
		ContextTokens: 1536, OutputTokens: 240, RequiredOverheadTokens: 96,
	})
	scheduler := newPulseToolScheduler(provider, nil,
		WithTaskModelConfiguration(config.InferenceTaskOrient, provider, model))

	orientation, err := scheduler.Orient(context.Background(), events.TypeMessage, "inspect before orienting")
	if err != nil || orientation.Summary != "oriented with tools" {
		t.Fatalf("Orient result=%+v err=%v", orientation, err)
	}
	assertUnifiedToolIterations(t, provider, "orient-tools", 240, 1200, "orient_A", "orient_B")
}

func TestDirectCognitionUsesUnifiedMultiStepExecution(t *testing.T) {
	provider := &toolContextProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "direct_A", Name: "runtime.info__read"}}},
		{ToolCalls: []inference.ToolCall{{ID: "direct_B", Name: "runtime.info__list"}}},
		{Content: "direct tools complete"},
	}}
	model := testModelConfiguration("direct-tools", inference.ModelContextBudget{
		ContextTokens: 2048, OutputTokens: 300, RequiredOverheadTokens: 100,
	})
	scheduler := newPulseToolScheduler(provider, nil,
		WithTaskModelConfiguration(config.InferenceTaskDirectCognition, provider, model))

	result, err := scheduler.Run(context.Background(), LevelReflect, "inspect directly")
	if err != nil || result.Actions[0].Payload["text"] != "direct tools complete" {
		t.Fatalf("Run result=%+v err=%v", result, err)
	}
	assertUnifiedToolIterations(t, provider, "direct-tools", 300, 1648, "direct_A", "direct_B")
}

func TestPermissionResolutionStartsFreshBoundedCognition(t *testing.T) {
	provider := &toolContextProvider{responses: []*inference.Response{{Content: "reconsidered"}}}
	scheduler := newPulseToolScheduler(provider, nil)
	request := &permissionrequest.Request{
		ID: "permission-1", Status: permissionrequest.StatusDenied,
		Capability: "test.echo", Operation: "run",
	}
	outcome := permissionrequest.ContinuationOutcome{Success: false, ErrorCode: "owner_denied", Error: "Owner denied the requested action"}
	if err := scheduler.NotifyPermissionResolution(context.Background(), request, outcome); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 || provider.runIDs[0] == "" {
		t.Fatalf("requests=%#v run IDs=%v", provider.requests, provider.runIDs)
	}
	content := joinedInferenceContent(provider.requests[0].Messages)
	for _, expected := range []string{"permission-1", "owner_denied", "test.echo"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("permission resolution context missing %q: %s", expected, content)
		}
	}
}

func assertUnifiedToolIterations(t *testing.T, provider *toolContextProvider, model string, outputBudget, inputBudget int, firstCall, secondCall string) {
	t.Helper()
	if len(provider.requests) != 3 {
		t.Fatalf("%s inference requests = %d, want 3", model, len(provider.requests))
	}
	for index, request := range provider.requests {
		if request.Model != model || request.MaxTokens != outputBudget {
			t.Errorf("%s iteration %d request = %+v", model, index, request)
		}
		if tokens := estimatedInferenceTokens(request.Messages); tokens > inputBudget {
			t.Errorf("%s iteration %d tokens = %d, budget = %d", model, index, tokens, inputBudget)
		}
		if provider.runIDs[index] == "" || provider.runIDs[index] != provider.runIDs[0] {
			t.Errorf("%s iteration %d run ID = %q, want %q", model, index, provider.runIDs[index], provider.runIDs[0])
		}
	}
	if got := messageToolResults(provider.requests[1].Messages); len(got) != 1 || got[0].ToolCallID != firstCall {
		t.Fatalf("%s second iteration results = %+v", model, got)
	}
	if got := messageToolResults(provider.requests[2].Messages); len(got) != 2 || got[0].ToolCallID != firstCall || got[1].ToolCallID != secondCall {
		t.Fatalf("%s third iteration results = %+v", model, got)
	}
}

func TestDirectPlanToolLoopRejectsOversizedRequiredResult(t *testing.T) {
	largeBody := &largeOutputBody{output: strings.Repeat("oversized-result-", defaultCognitiveInputBudget*3)}
	provider := &toolContextProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "large_call", Name: "large.output__read"}}},
		{Content: makePlanJSON("must not be reached")},
	}}
	scheduler := newPulseToolScheduler(provider, largeBody)

	_, _, err := scheduler.Plan(context.Background(), events.TypeMessage, "read the large output", &Orientation{
		Summary: "large output requested", Matters: true, Reason: "direct test",
	})
	if err == nil || !strings.Contains(err.Error(), contextbuilder.ErrRequiredContextExceedsBudget.Error()) {
		t.Fatalf("Plan error = %v, want required-context budget failure", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider received %d requests; oversized result must fail before the next invocation", len(provider.requests))
	}
}

func newPulseToolScheduler(provider inference.Provider, executionBody body.Body, opts ...Option) *Scheduler {
	registry := body.NewRegistry()
	if executionBody == nil {
		executionBody, _ = registry.Local()
	} else if err := registry.Register(executionBody); err != nil {
		panic(err)
	}
	guard := body.NewGuard(registry, allowAllEvaluator{})
	executor := NewToolExecutor(registry, guard, actionpolicy.Default(), "neondoll")
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "tool-doll", CanonicalName: "Spark"}}
	return New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state}, append([]Option{WithToolExecutor(executor)}, opts...)...)
}

func estimatedInferenceTokens(messages []inference.Message) int {
	estimator := contextbuilder.ApproximateEstimator{}
	total := 0
	for _, message := range messages {
		total += estimator.EstimateTokens(contextbuilder.Message{Role: contextbuilder.Role(message.Role), Content: message.Content})
	}
	return total
}

func messageToolResults(messages []inference.Message) []inference.ToolResult {
	var results []inference.ToolResult
	for _, message := range messages {
		if message.ToolResult != nil {
			results = append(results, *message.ToolResult)
		}
	}
	return results
}

type largeOutputBody struct{ output string }

func (bodyFixture *largeOutputBody) ID() body.BodyID     { return "large-output" }
func (bodyFixture *largeOutputBody) Kind() body.BodyKind { return body.BodyKindRemote }
func (bodyFixture *largeOutputBody) Name() string        { return "large-output" }
func (bodyFixture *largeOutputBody) Describe() []body.Capability {
	return []body.Capability{{ID: "large.output", Operations: []string{"read"}, Available: true}}
}
func (bodyFixture *largeOutputBody) ResolveCapability(capability, operation string) error {
	if capability != "large.output" {
		return body.ErrUnsupportedCapability
	}
	if operation != "read" {
		return body.ErrUnsupportedOperation
	}
	return nil
}
func (bodyFixture *largeOutputBody) RegisterCapability(body.Capability) error {
	return errors.New("not supported by fixture")
}
func (bodyFixture *largeOutputBody) Execute(request body.ExecutionRequest) (*body.ExecutionResult, error) {
	return &body.ExecutionResult{ExecutionID: request.ExecutionID, Status: body.StatusSuccess, Output: bodyFixture.output}, nil
}
