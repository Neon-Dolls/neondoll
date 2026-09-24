package inference

import (
	"context"
	"fmt"
)

// ── Phase 3 — Provider Adapter Contract ─────────────────────────────────

// ToolEnabledProvider wraps a Provider to surface tool definitions to the
// provider in a provider-neutral way. It delegates Infer() calls directly
// to the inner provider — the tool loop lives in the Scheduler (not here).
//
// For M6, this is a thin pass-through that formats tools as text for
// prompt-based tool use. Provider-specific adapters (e.g. OpenAI) override
// serialization through the inner provider's own request building.
//
// Future adapters can implement native tool serialization by wrapping the
// inner provider's request building — this struct provides the extension
// point without changing the public Provider interface.
type ToolEnabledProvider struct {
	inner Provider
}

// NewToolEnabledProvider creates a wrapper that exposes tool support.
func NewToolEnabledProvider(inner Provider) *ToolEnabledProvider {
	return &ToolEnabledProvider{inner: inner}
}

// Infer delegates to the inner Provider. Tools in the request are formatted
// as system-prompt text so prompt-based providers can reference them.
// Provider-specific adapters override this by serializing tools natively.
func (t *ToolEnabledProvider) Infer(ctx context.Context, req Request) (*Response, error) {
	if len(req.Tools) == 0 {
		return t.inner.Infer(ctx, req)
	}

	// Inject tool definitions as a system message so the model knows what it can call.
	reqWithTools := req
	toolText := formatToolsForPrompt(req.Tools)
	reqWithTools.Messages = append([]Message{
		{Role: "system", Content: toolText},
	}, reqWithTools.Messages...)

	// Format tool results from prior turns as system messages too.
	for _, tr := range req.ToolResults {
		reqWithTools.Messages = append(reqWithTools.Messages, Message{
			Role:    "system",
			Content: formatToolResultForPrompt(tr),
		})
	}

	return t.inner.Infer(ctx, reqWithTools)
}

// ID returns the inner provider's ID.
func (t *ToolEnabledProvider) ID() ProviderID {
	return t.inner.ID()
}

// formatToolsForPrompt creates a textual description of available tools.
func formatToolsForPrompt(tools []Tool) string {
	if len(tools) == 0 {
		return ""
	}
	result := "You have these tools available. When you want to use one, respond with exactly:\n" +
		"TOOL_CALL: <tool_name>\nARGUMENTS:\n<JSON arguments>\n\nAvailable tools:\n"
	for _, tool := range tools {
		result += "- " + tool.Name + ": " + tool.Description + "\n"
	}
	return result
}

// formatToolResultForPrompt creates a textual description of a tool result.
func formatToolResultForPrompt(tr ToolResult) string {
	status := "SUCCESS"
	if tr.Status == ToolResultFailure {
		status = "FAILURE"
	}
	result := fmt.Sprintf("Tool result for call %s: %s\n", tr.ToolCallID, status)
	if tr.Result != nil {
		result += fmt.Sprintf("Result: %v\n", tr.Result)
	}
	if tr.Error != nil {
		result += fmt.Sprintf("Error [%s]: %s\n", tr.Error.Code, tr.Error.Message)
	}
	return result
}
