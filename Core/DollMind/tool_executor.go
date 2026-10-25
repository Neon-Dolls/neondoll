package dollmind

import (
	"context"
	"errors"
	"sort"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/BodyResolver"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

// ToolExecutor is the single cognitive tool execution path. It performs the
// action-wide authority check, resolves trusted Registry Bodies, and delegates
// every concrete attempt to BodyResolver.Coordinator and therefore Body.Guard.
type ToolExecutor struct {
	registry        *body.Registry
	actionAuthority body.AuthorityEvaluator
	coordinator     *bodyresolver.Coordinator
	dollID          string
	preferences     map[body.BodyID]int
	ownerPolicy     bodyresolver.OwnerPolicy
}

type ToolExecutorOption func(*ToolExecutor)

func WithBodyPreferences(preferences map[body.BodyID]int) ToolExecutorOption {
	return func(executor *ToolExecutor) {
		executor.preferences = make(map[body.BodyID]int, len(preferences))
		for bodyID, preference := range preferences {
			executor.preferences[bodyID] = preference
		}
	}
}

func WithBodyOwnerPolicy(policy bodyresolver.OwnerPolicy) ToolExecutorOption {
	return func(executor *ToolExecutor) { executor.ownerPolicy = policy }
}

// NewToolExecutor composes existing Core authority, Registry, resolver, and
// Guard boundaries. actionAuthority evaluates the Body-independent action;
// Guard independently authorizes every selected Body execution.
func NewToolExecutor(registry *body.Registry, guard *body.Guard, actionAuthority body.AuthorityEvaluator, dollID string, options ...ToolExecutorOption) *ToolExecutor {
	coordinator, _ := bodyresolver.NewCoordinator(guard)
	executor := &ToolExecutor{registry: registry, actionAuthority: actionAuthority, coordinator: coordinator, dollID: dollID}
	for _, option := range options {
		option(executor)
	}
	return executor
}

// Tools projects each currently available semantic capability/operation once.
// Body selection stays internal; provider-visible names never encode identity.
func (executor *ToolExecutor) Tools() []inference.Tool {
	if executor.registry == nil {
		return nil
	}
	bodies := executor.registry.All()
	sort.Slice(bodies, func(i, j int) bool { return bodies[i].ID() < bodies[j].ID() })
	byName := make(map[string]inference.Tool)
	for _, candidate := range bodies {
		for _, tool := range inference.ProjectToolsFromCapabilities(candidate, candidate.ID()) {
			tool.TargetBodyID = ""
			if _, exists := byName[tool.Name]; !exists {
				byName[tool.Name] = tool
			}
		}
	}
	tools := make([]inference.Tool, 0, len(byName))
	for _, tool := range byName {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// ExecuteCall authorizes an action before discovery, resolves candidates from
// trusted Registry state, then executes resolver ordering through Guard.
func (executor *ToolExecutor) ExecuteCall(ctx context.Context, call inference.ToolCall, tool *inference.Tool) inference.ToolResult {
	if tool == nil {
		return failureResult(call.ID, "unknown_tool", "tool definition not found")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return failureResult(call.ID, "cancelled", err.Error())
	}
	if executor.registry == nil || executor.actionAuthority == nil || executor.coordinator == nil || executor.dollID == "" {
		return failureResult(call.ID, "invalid_configuration", "tool execution boundaries are unavailable")
	}

	action := body.AuthorityRequest{Doll: executor.dollID, Capability: tool.CapabilityID, Operation: tool.OperationID, Arguments: call.Arguments}
	if decision := executor.actionAuthority.Evaluate(action); decision.IsDeny() {
		return failureResult(call.ID, "action_denied", decision.Message)
	}

	constraints := bodyresolver.TaskConstraints{}
	if tool.TargetBodyID != "" {
		constraints.AllowedBodyIDs = []body.BodyID{body.BodyID(tool.TargetBodyID)}
	}
	resolved, resolveErr := bodyresolver.Resolve(bodyresolver.ActionRequirement{
		Capability: tool.CapabilityID,
		Operation:  tool.OperationID,
	}, constraints, executor.ownerPolicy, executor.registryCandidates())
	rejections := toolCandidateRejections(resolved.Rejected)
	if resolveErr != nil {
		result := failureResult(call.ID, "no_eligible_body", resolveErr.Error())
		result.CandidateRejections = rejections
		return result
	}

	outcome, executionErr := executor.coordinator.Execute(ctx, resolved, bodyresolver.ExecutionRequest{
		ExecutionID: "tool_" + call.ID,
		Doll:        executor.dollID,
		Arguments:   call.Arguments,
	})
	return toolOutcome(call.ID, outcome, rejections, executionErr)
}

func (executor *ToolExecutor) registryCandidates() []bodyresolver.Candidate {
	bodies := executor.registry.All()
	candidates := make([]bodyresolver.Candidate, 0, len(bodies))
	for _, candidate := range bodies {
		candidates = append(candidates, bodyresolver.Candidate{Body: candidate, Preference: executor.preferences[candidate.ID()]})
	}
	return candidates
}

func toolOutcome(callID string, outcome bodyresolver.ExecutionOutcome, rejections []inference.ToolCandidateRejection, err error) inference.ToolResult {
	attempts := make([]inference.ToolExecutionAttempt, len(outcome.Attempts))
	for index, attempt := range outcome.Attempts {
		attempts[index] = inference.ToolExecutionAttempt{
			BodyID: string(attempt.BodyID), ExecutionID: attempt.ExecutionID,
			Status: string(attempt.Result.Status), ErrorCode: attempt.Result.ErrorCode,
		}
	}
	if err == nil && outcome.Result != nil && outcome.Result.Status == body.StatusSuccess {
		result := successResult(callID, *outcome.Result)
		result.Attempts, result.CandidateRejections = attempts, rejections
		return result
	}
	code := "execution_failed"
	message := "Body execution failed"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code, message = "cancelled", err.Error()
	case errors.Is(err, bodyresolver.ErrGlobalProhibition):
		code, message = "action_denied", "global authority prohibition"
	case errors.Is(err, bodyresolver.ErrCandidatesExhausted):
		code, message = exhaustedToolError(outcome)
	case err != nil:
		message = err.Error()
	}
	result := failureResult(callID, code, message)
	if outcome.Result != nil {
		result.ExecutionID = outcome.Result.ExecutionID
	}
	result.Attempts, result.CandidateRejections = attempts, rejections
	return result
}

func exhaustedToolError(outcome bodyresolver.ExecutionOutcome) (string, string) {
	if outcome.Result == nil {
		return "candidates_exhausted", bodyresolver.ErrCandidatesExhausted.Error()
	}
	switch outcome.Result.Status {
	case body.StatusDenied:
		return "denied", "authority denied: " + outcome.Result.Error
	case body.StatusUnavailable:
		return "unavailable", "capability unavailable: " + outcome.Result.Error
	case body.StatusFailed:
		return "execution_failed", "Body execution failed: " + outcome.Result.Error
	default:
		return "candidates_exhausted", bodyresolver.ErrCandidatesExhausted.Error()
	}
}

func toolCandidateRejections(rejected []bodyresolver.RejectedCandidate) []inference.ToolCandidateRejection {
	result := make([]inference.ToolCandidateRejection, len(rejected))
	for index, candidate := range rejected {
		reasons := make([]string, len(candidate.Reasons))
		for reasonIndex, reason := range candidate.Reasons {
			reasons[reasonIndex] = string(reason.Code) + ": " + reason.Detail
		}
		result[index] = inference.ToolCandidateRejection{BodyID: string(candidate.BodyID), Reasons: reasons}
	}
	return result
}

func (executor *ToolExecutor) ExecuteSequentially(ctx context.Context, calls []inference.ToolCall, toolMap map[string]*inference.Tool) []inference.ToolResult {
	results := make([]inference.ToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, executor.ExecuteCall(ctx, call, toolMap[call.Name]))
	}
	return results
}

func successResult(callID string, result body.ExecutionResult) inference.ToolResult {
	toolResult := inference.ToolResult{ToolCallID: callID, ExecutionID: result.ExecutionID, Status: inference.ToolResultSuccess}
	if result.Output != "" {
		toolResult.Result = map[string]any{"output": result.Output}
	}
	return toolResult
}

func failureResult(callID, code, message string) inference.ToolResult {
	return inference.ToolResult{ToolCallID: callID, Status: inference.ToolResultFailure, Error: &inference.ToolError{Code: code, Message: message}}
}
