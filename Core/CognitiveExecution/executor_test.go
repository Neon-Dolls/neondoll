package cognitiveexecution_test

import (
	"context"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

type scriptedProvider struct {
	responses []*inference.Response
	requests  []inference.Request
}

func (provider *scriptedProvider) ID() inference.ProviderID { return "scripted" }
func (provider *scriptedProvider) Infer(_ context.Context, request inference.Request) (*inference.Response, error) {
	provider.requests = append(provider.requests, request)
	return provider.responses[len(provider.requests)-1], nil
}

type recordingTools struct {
	calls []inference.ToolCall
}

func (tools *recordingTools) Tools() []inference.Tool {
	return []inference.Tool{{Name: "runtime.info__read"}, {Name: "runtime.info__list"}}
}

func (tools *recordingTools) ExecuteCall(_ context.Context, call inference.ToolCall, _ *inference.Tool) inference.ToolResult {
	tools.calls = append(tools.calls, call)
	return inference.ToolResult{ToolCallID: call.ID, ExecutionID: "exec_" + call.ID, Status: inference.ToolResultSuccess}
}

func TestExecuteRebuildsContextAndPinsModelAcrossToolIterations(t *testing.T) {
	provider := &scriptedProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "call-one", Name: "runtime.info__read"}}},
		{ToolCalls: []inference.ToolCall{{ID: "call-two", Name: "runtime.info__list"}}},
		{Content: "complete"},
	}}
	tools := &recordingTools{}
	model := config.ModelConfiguration{
		Name: "plan", Provider: "openai", Model: "plan-model", Endpoint: "http://plan.invalid",
		Budget: inference.ModelContextBudget{ContextTokens: 1600, OutputTokens: 200, RequiredOverheadTokens: 100},
	}
	var builds []cognitiveexecution.ContextState
	result, err := cognitiveexecution.Execute(context.Background(), cognitiveexecution.Request{
		Provider: provider, ModelConfiguration: model, Purpose: inference.PurposePlan,
		Temperature: 0.3, ToolExecutor: tools,
		BuildContext: func(_ context.Context, state cognitiveexecution.ContextState) ([]inference.Message, error) {
			builds = append(builds, state)
			return []inference.Message{{Role: "system", Content: "bounded context"}}, nil
		},
	})
	if err != nil || result.Response.Content != "complete" {
		t.Fatalf("Execute result=%+v err=%v", result, err)
	}
	if len(builds) != 3 || len(provider.requests) != 3 || len(tools.calls) != 2 {
		t.Fatalf("builds=%d requests=%d tools=%d", len(builds), len(provider.requests), len(tools.calls))
	}
	for index, request := range provider.requests {
		if request.Model != "plan-model" || request.MaxTokens != 200 || builds[index].InputTokenBudget != 1300 {
			t.Errorf("iteration %d request=%+v state=%+v", index, request, builds[index])
		}
	}
	if len(builds[0].Exchanges) != 0 || len(builds[1].Exchanges) != 1 || len(builds[2].Exchanges) != 2 {
		t.Fatalf("exchange progression = %d/%d/%d", len(builds[0].Exchanges), len(builds[1].Exchanges), len(builds[2].Exchanges))
	}
	if !builds[1].Exchanges[0].Required || builds[2].Exchanges[0].Required || !builds[2].Exchanges[1].Required {
		t.Fatalf("required exchange lifecycle = %+v", builds)
	}
	if len(result.Exchanges) != 2 || result.Exchanges[0].Required || result.Exchanges[1].Required {
		t.Fatalf("terminal exchange lifecycle = %+v", result.Exchanges)
	}
	if tools.calls[0].ID != "call-one" || tools.calls[1].ID != "call-two" {
		t.Fatalf("tool correlation = %+v", tools.calls)
	}
}
