package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
//
// After the inner provider responds, this method parses the text content
// for tool call syntax and extracts normalized ToolCall objects.
// The provider text format is:
//
//	TOOL_CALL: <tool_name>
//	ARGUMENTS:
//	<JSON arguments>
//
// When tool call syntax is detected, Response.ToolCalls is populated and
// Response.Content is left empty (tool call turn). Otherwise the response
// is treated as final content.
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

	resp, err := t.inner.Infer(ctx, reqWithTools)
	if err != nil {
		return nil, err
	}

	// Parse the inner provider's text content for tool call syntax.
	if resp.Content != "" {
		toolCalls := parseToolCallsFromText(resp.Content)
		if len(toolCalls) > 0 {
			// Tool call turn — move content into tool calls, leave content empty.
			return &Response{
				ToolCalls:  toolCalls,
				ProviderID: resp.ProviderID,
				TokensUsed: resp.TokensUsed,
			}, nil
		}
	}

	return resp, nil
}

// ID returns the inner provider's ID.
func (t *ToolEnabledProvider) ID() ProviderID {
	return t.inner.ID()
}

// parseToolCallsFromText scans provider text output for tool call directives
// and extracts normalized ToolCall objects.
//
// Expected format per tool call:
//
//	TOOL_CALL: <tool_name>
//	ARGUMENTS:
//	<JSON object>
//
// Multiple tool calls can appear in sequence. Any text before, between, or
// after calls is ignored during parsing.
func parseToolCallsFromText(content string) []ToolCall {
	lines := strings.Split(content, "\n")
	var calls []ToolCall
	callIndex := 0

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		// Skip empty lines and non-TOOL_CALL lines.
		if !strings.HasPrefix(trimmed, "TOOL_CALL:") {
			continue
		}

		// Extract tool name after "TOOL_CALL:" prefix.
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "TOOL_CALL:"))
		if name == "" {
			continue
		}

		// Expect ARGUMENTS: on the next non-empty line.
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) {
			// No ARGUMENTS line — tool call without arguments.
			callIndex++
			calls = append(calls, ToolCall{
				ID:        fmt.Sprintf("tp_call_%d", callIndex),
				Name:      name,
				Arguments: nil,
			})
			i = j
			continue
		}

		argsLine := strings.TrimSpace(lines[j])
		if !strings.HasPrefix(argsLine, "ARGUMENTS:") {
			// Missing ARGUMENTS marker — treat as no arguments.
			callIndex++
			calls = append(calls, ToolCall{
				ID:        fmt.Sprintf("tp_call_%d", callIndex),
				Name:      name,
				Arguments: nil,
			})
			i = j - 1
			continue
		}

		// Collect argument lines after "ARGUMENTS:" header.
		// The header may be "ARGUMENTS:" (no inline value) or "ARGUMENTS: <json>".
		argsText := strings.TrimSpace(strings.TrimPrefix(argsLine, "ARGUMENTS:"))

		if argsText == "" {
			// Multi-line JSON: collect following lines until empty line or
			// another TOOL_CALL: directive.
			jsonLines := []string{}
			for k := j + 1; k < len(lines); k++ {
				nextLine := lines[k]
				trimmedNext := strings.TrimSpace(nextLine)
				if trimmedNext == "" || strings.HasPrefix(trimmedNext, "TOOL_CALL:") {
					break
				}
				jsonLines = append(jsonLines, nextLine)
			}
			argsText = strings.TrimSpace(strings.Join(jsonLines, "\n"))
		}

		var args map[string]any
		if argsText != "" {
			if err := json.Unmarshal([]byte(argsText), &args); err != nil {
				// Invalid JSON — still produce a ToolCall with nil arguments;
				// the executor will fail with invalid_arguments.
				args = nil
			}
		}

		callIndex++
		calls = append(calls, ToolCall{
			ID:        fmt.Sprintf("tp_call_%d", callIndex),
			Name:      name,
			Arguments: args,
		})

		// Skip to after the ARGUMENTS block.
		i = j + 1
		for i < len(lines) {
			trimmedNext := strings.TrimSpace(lines[i])
			if strings.HasPrefix(trimmedNext, "TOOL_CALL:") {
				i--
				break
			}
			i++
		}
	}

	return calls
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
