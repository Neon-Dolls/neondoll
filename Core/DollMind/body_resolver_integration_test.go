package dollmind

import (
	"context"
	"errors"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

type resolverBody struct {
	id                 body.BodyID
	resolveCalls       int
	unavailableOnGuard bool
	executionError     error
	executions         int
}

func (candidate *resolverBody) ID() body.BodyID     { return candidate.id }
func (candidate *resolverBody) Kind() body.BodyKind { return body.BodyKindRemote }
func (candidate *resolverBody) Name() string        { return string(candidate.id) }
func (candidate *resolverBody) Describe() []body.Capability {
	return []body.Capability{{ID: "test.echo", Operations: []string{"run"}, Available: true}}
}
func (candidate *resolverBody) RegisterCapability(body.Capability) error { return nil }
func (candidate *resolverBody) ResolveCapability(capability, operation string) error {
	candidate.resolveCalls++
	if capability != "test.echo" {
		return body.ErrUnsupportedCapability
	}
	if operation != "run" {
		return body.ErrUnsupportedOperation
	}
	if candidate.unavailableOnGuard && candidate.resolveCalls > 1 {
		return body.ErrUnavailable
	}
	return nil
}
func (candidate *resolverBody) Execute(request body.ExecutionRequest) (*body.ExecutionResult, error) {
	candidate.executions++
	if candidate.executionError != nil {
		return nil, candidate.executionError
	}
	return &body.ExecutionResult{Status: body.StatusSuccess, Output: string(candidate.id)}, nil
}

type recordingAuthority struct {
	results map[body.BodyID]body.AuthorityResult
	calls   []body.AuthorityRequest
}

func (authority *recordingAuthority) Evaluate(request body.AuthorityRequest) body.AuthorityResult {
	authority.calls = append(authority.calls, request)
	if result, ok := authority.results[request.Body]; ok {
		return result
	}
	return body.AuthorityResult{Decision: body.AuthorityDeny, Reason: "no_rule"}
}

func TestToolExecutorActionProhibitionStopsBeforeResolution(t *testing.T) {
	registry := body.NewRegistry()
	candidate := &resolverBody{id: "body-never-resolved"}
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	bodyAuthority := &recordingAuthority{}
	policy, err := actionpolicy.New([]actionpolicy.Rule{{
		Capability: "test.echo", Operation: "run", Decision: actionpolicy.DecisionDeny, Reason: "owner_prohibited",
	}})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, body.NewGuard(registry, bodyAuthority), policy, "doll")

	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call-denied"}, echoTool(""))
	if result.Status != inference.ToolResultFailure || result.Error == nil || result.Error.Code != "action_denied" {
		t.Fatalf("result = %+v", result)
	}
	if candidate.resolveCalls != 0 || candidate.executions != 0 || len(bodyAuthority.calls) != 0 {
		t.Fatalf("resolution=%d executions=%d Body authority calls=%d", candidate.resolveCalls, candidate.executions, len(bodyAuthority.calls))
	}
}

func TestToolExecutorApprovalRequirementStopsBeforeResolution(t *testing.T) {
	registry := body.NewRegistry()
	candidate := &resolverBody{id: "body-awaiting-approval"}
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	bodyAuthority := &recordingAuthority{}
	policy, err := actionpolicy.New([]actionpolicy.Rule{{
		Capability: "test.echo", Operation: "run", Decision: actionpolicy.DecisionRequireApproval, Reason: "owner_approval",
	}})
	if err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, body.NewGuard(registry, bodyAuthority), policy, "doll")

	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call-approval"}, echoTool(""))
	if result.Status != inference.ToolResultFailure || result.Error == nil || result.Error.Code != "approval_required" {
		t.Fatalf("result = %+v", result)
	}
	if candidate.resolveCalls != 0 || candidate.executions != 0 || len(bodyAuthority.calls) != 0 {
		t.Fatalf("resolution=%d executions=%d Body authority calls=%d", candidate.resolveCalls, candidate.executions, len(bodyAuthority.calls))
	}
}

func TestToolExecutorBodyDenialAndUnavailableFallBackIndependently(t *testing.T) {
	first := &resolverBody{id: "body-denied"}
	second := &resolverBody{id: "body-unavailable", unavailableOnGuard: true}
	third := &resolverBody{id: "body-success"}
	authority := &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{
		first.id:  {Decision: body.AuthorityDeny, Reason: "body_policy", Scope: body.AuthorityScopeBody},
		second.id: {Decision: body.AuthorityAllow},
		third.id:  {Decision: body.AuthorityAllow},
	}}
	executor := resolverExecutor(t, []*resolverBody{first, second, third}, authority,
		map[body.BodyID]int{first.id: 30, second.id: 20, third.id: 10})

	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call-fallback"}, echoTool(""))
	if result.Status != inference.ToolResultSuccess || result.Result["output"] != string(third.id) {
		t.Fatalf("result = %+v", result)
	}
	assertToolAttempts(t, result, []string{string(first.id), string(second.id), string(third.id)}, []string{"denied", "unavailable", "success"})
	if got := authorityBodies(authority.calls); !equalStrings(got, []string{string(first.id), string(third.id)}) {
		// Availability is resolved before Guard evaluates authority.
		t.Fatalf("independent Guard authority calls = %v", got)
	}
}

func TestToolExecutorFailureFallsBackAndPreservesRejections(t *testing.T) {
	failed := &resolverBody{id: "body-failed", executionError: errors.New("device failure")}
	success := &resolverBody{id: "body-success"}
	authority := &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{
		failed.id: {Decision: body.AuthorityAllow}, success.id: {Decision: body.AuthorityAllow},
	}}
	executor := resolverExecutor(t, []*resolverBody{failed, success}, authority,
		map[body.BodyID]int{failed.id: 20, success.id: 10, body.LocalBodyID: 1000})

	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call-failure"}, echoTool(""))
	if result.Status != inference.ToolResultSuccess {
		t.Fatalf("result = %+v", result)
	}
	assertToolAttempts(t, result, []string{string(failed.id), string(success.id)}, []string{"failed", "success"})
	if len(result.CandidateRejections) == 0 || result.CandidateRejections[0].BodyID != string(body.LocalBodyID) {
		t.Fatalf("candidate rejections = %+v", result.CandidateRejections)
	}
}

func TestToolExecutorExplicitTargetNeverRedirects(t *testing.T) {
	target := &resolverBody{id: "body-target", executionError: errors.New("target failed")}
	alternative := &resolverBody{id: "body-alternative"}
	authority := &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{
		target.id: {Decision: body.AuthorityAllow}, alternative.id: {Decision: body.AuthorityAllow},
	}}
	executor := resolverExecutor(t, []*resolverBody{target, alternative}, authority,
		map[body.BodyID]int{alternative.id: 100, target.id: 1})

	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "call-target"}, echoTool(string(target.id)))
	if result.Status != inference.ToolResultFailure || result.Error == nil || result.Error.Code != "execution_failed" {
		t.Fatalf("result = %+v", result)
	}
	assertToolAttempts(t, result, []string{string(target.id)}, []string{"failed"})
	if alternative.executions != 0 {
		t.Fatalf("alternative executions = %d", alternative.executions)
	}
}

func TestCognitiveExecutionUsesResolvingProductionToolPath(t *testing.T) {
	denied := &resolverBody{id: "body-denied"}
	success := &resolverBody{id: "body-success"}
	authority := &recordingAuthority{results: map[body.BodyID]body.AuthorityResult{
		denied.id: {Decision: body.AuthorityDeny, Scope: body.AuthorityScopeBody}, success.id: {Decision: body.AuthorityAllow},
	}}
	executor := resolverExecutor(t, []*resolverBody{denied, success}, authority,
		map[body.BodyID]int{denied.id: 20, success.id: 10})
	provider := &resolverProvider{}
	model := config.ModelConfiguration{Name: "test", Provider: "openai", Model: "test-model", Endpoint: inference.DefaultBaseURL,
		Budget: inference.ModelContextBudget{ContextTokens: 1000, OutputTokens: 100, RequiredOverheadTokens: 100}}

	_, err := cognitiveexecution.Execute(context.Background(), cognitiveexecution.Request{
		Provider: provider, ModelConfiguration: model, ToolExecutor: executor,
		BuildContext: func(_ context.Context, state cognitiveexecution.ContextState) ([]inference.Message, error) {
			messages := []inference.Message{{Role: "system", Content: "bounded", Tools: state.Tools}}
			for _, exchange := range state.Exchanges {
				content, err := cognitiveexecution.FormatToolExchange(exchange)
				if err != nil {
					return nil, err
				}
				call, result := exchange.Call, exchange.Result
				messages = append(messages, inference.Message{Role: "tool", Content: content, ToolCall: &call, ToolResult: &result})
			}
			return messages, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d", len(provider.requests))
	}
	result := provider.requests[1].Messages[1].ToolResult
	if result == nil {
		t.Fatal("second inference request has no correlated tool result")
	}
	assertToolAttempts(t, *result, []string{string(denied.id), string(success.id)}, []string{"denied", "success"})
}

type resolverProvider struct{ requests []inference.Request }

func (provider *resolverProvider) ID() inference.ProviderID { return "resolver-provider" }
func (provider *resolverProvider) Infer(_ context.Context, request inference.Request) (*inference.Response, error) {
	provider.requests = append(provider.requests, request)
	if len(provider.requests) == 1 {
		return &inference.Response{ToolCalls: []inference.ToolCall{{ID: "call-cognitive", Name: "test.echo__run"}}}, nil
	}
	return &inference.Response{Content: "done"}, nil
}

func resolverExecutor(t *testing.T, candidates []*resolverBody, authority body.AuthorityEvaluator, preferences map[body.BodyID]int) *ToolExecutor {
	t.Helper()
	registry := body.NewRegistry()
	for _, candidate := range candidates {
		if err := registry.Register(candidate); err != nil {
			t.Fatal(err)
		}
	}
	return NewToolExecutor(registry, body.NewGuard(registry, authority), actionpolicy.Default(), "doll", WithBodyPreferences(preferences))
}

func echoTool(target string) *inference.Tool {
	return &inference.Tool{Name: "test.echo__run", CapabilityID: "test.echo", OperationID: "run", TargetBodyID: target}
}

func assertToolAttempts(t *testing.T, result inference.ToolResult, bodyIDs, statuses []string) {
	t.Helper()
	if len(result.Attempts) != len(bodyIDs) {
		t.Fatalf("attempts = %+v", result.Attempts)
	}
	for index, attempt := range result.Attempts {
		if attempt.BodyID != bodyIDs[index] || attempt.Status != statuses[index] || attempt.ExecutionID == "" {
			t.Fatalf("attempt %d = %+v, want Body=%s status=%s", index, attempt, bodyIDs[index], statuses[index])
		}
		if index > 0 && attempt.ExecutionID == result.Attempts[index-1].ExecutionID {
			t.Fatalf("attempt IDs are not distinct: %+v", result.Attempts)
		}
	}
}

func authorityBodies(requests []body.AuthorityRequest) []string {
	result := make([]string, len(requests))
	for index, request := range requests {
		result[index] = string(request.Body)
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
