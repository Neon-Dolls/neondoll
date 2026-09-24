package dollmind

import (
	"context"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

// ── Phase 4 — Core Tool Executor ────────────────────────────────────────

// ToolExecutor maps normalized ToolCalls back to exposed Body operations.
// It uses the EXISTING Guard.Execute path — there is no second execution path.
//
// Flow: ToolCall → lookup in run's exposed-tool table → validate arguments →
// construct M4 ExecutionRequest → Guard.Execute → ExecutionResult → ToolResult.
type ToolExecutor struct {
	guard     *body.Guard
	localBody body.Body
}

// NewToolExecutor creates a new ToolExecutor bound to the given Guard and
// the Local Body used for tool projection.
func NewToolExecutor(guard *body.Guard, localBody body.Body) *ToolExecutor {
	return &ToolExecutor{guard: guard, localBody: localBody}
}

// Tools projects the local body's capabilities into Inference Tool definitions
// using the deterministic naming convention from Phase 2.
func (e *ToolExecutor) Tools() []inference.Tool {
	return inference.ProjectToolsFromCapabilities(e.localBody, e.localBody.ID())
}

// ExecuteCall executes a single ToolCall against the Body via Guard.Execute.
// It returns a correlated ToolResult for every call. Errors never crash the
// loop — they produce truthful failure ToolResults.
func (e *ToolExecutor) ExecuteCall(ctx context.Context, call inference.ToolCall, toolDef *inference.Tool) inference.ToolResult {
	if toolDef == nil {
		return failureResult(call.ID, "unknown_tool", "tool definition not found")
	}
	if toolDef.TargetBodyID == "" {
		return failureResult(call.ID, "invalid_configuration", "tool has no target body")
	}

	execReq := body.ExecutionRequest{
		ExecutionID: fmt.Sprintf("tool_%s", call.ID),
		Doll:        "neondoll",
		Body:        body.BodyID(toolDef.TargetBodyID),
		Capability:  toolDef.CapabilityID,
		Operation:   toolDef.OperationID,
		Arguments:   call.Arguments,
	}

	result := e.guard.Execute(execReq)

	switch result.Status {
	case body.StatusSuccess:
		return successResult(call.ID, result)
	case body.StatusDenied:
		return executionFailureResult(call.ID, result, "denied", fmt.Sprintf("authority denied: %s", result.Error))
	case body.StatusUnsupported:
		return executionFailureResult(call.ID, result, "unsupported", fmt.Sprintf("capability/operation not supported: %s", result.Error))
	case body.StatusInvalid:
		return executionFailureResult(call.ID, result, "invalid_arguments", fmt.Sprintf("invalid request: %s", result.Error))
	case body.StatusUnavailable:
		return executionFailureResult(call.ID, result, "unavailable", fmt.Sprintf("capability unavailable: %s", result.Error))
	case body.StatusFailed:
		return executionFailureResult(call.ID, result, "execution_failed", fmt.Sprintf("body execution failed: %s", result.Error))
	default:
		return failureResult(call.ID, "unknown_status", fmt.Sprintf("unexpected execution status: %s", result.Status))
	}
}

// ExecuteSequentially executes multiple ToolCalls sequentially, returning a
// ToolResult for each. The calls are executed one at a time, not concurrently.
func (e *ToolExecutor) ExecuteSequentially(ctx context.Context, calls []inference.ToolCall, toolMap map[string]*inference.Tool) []inference.ToolResult {
	results := make([]inference.ToolResult, 0, len(calls))
	for _, call := range calls {
		toolDef, ok := toolMap[call.Name]
		if !ok {
			results = append(results, failureResult(call.ID, "unknown_tool",
				fmt.Sprintf("tool %q not found in exposed tools", call.Name)))
			continue
		}
		result := e.ExecuteCall(ctx, call, toolDef)
		results = append(results, result)
	}
	return results
}

// successResult builds a ToolResult from a successful ExecutionResult.
func successResult(callID string, execResult body.ExecutionResult) inference.ToolResult {
	tr := inference.ToolResult{
		ToolCallID:  callID,
		ExecutionID: execResult.ExecutionID,
		Status:      inference.ToolResultSuccess,
	}
	if execResult.Output != "" {
		tr.Result = map[string]any{
			"output": execResult.Output,
		}
	}
	return tr
}

// failureResult builds a ToolResult with failure status and NO execution
// correlation. Use for pre-execution validation failures where no Guard.Execute
// attempt occurred (unknown tool, missing target body).
func failureResult(callID string, code string, message string) inference.ToolResult {
	return inference.ToolResult{
		ToolCallID: callID,
		Status:     inference.ToolResultFailure,
		Error: &inference.ToolError{
			Code:    code,
			Message: message,
		},
	}
}

// executionFailureResult builds a ToolResult with failure status from a
// completed Guard.Execute attempt, preserving the execution correlation ID.
// Every actual execution attempt — whether denied, unsupported, or failed —
// remains observable through ExecutionID so multiple attempts for one ToolCall
// can be correlated independently.
func executionFailureResult(callID string, execResult body.ExecutionResult, code string, message string) inference.ToolResult {
	return inference.ToolResult{
		ToolCallID:  callID,
		ExecutionID: execResult.ExecutionID,
		Status:      inference.ToolResultFailure,
		Error: &inference.ToolError{
			Code:    code,
			Message: message,
		},
	}
}
