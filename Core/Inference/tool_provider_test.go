package inference

import (
	"context"
	"fmt"
	"testing"
)

// ── Adapter-Level ToolCall Parser Tests ──────────────────────────────────

func TestParseToolCallsFromText_EmptyContent(t *testing.T) {
	calls := parseToolCallsFromText("")
	if len(calls) != 0 {
		t.Errorf("expected 0 calls from empty content, got %d", len(calls))
	}
}

func TestParseToolCallsFromText_NoToolCall(t *testing.T) {
	content := "Hello, here is my response to you."
	calls := parseToolCallsFromText(content)
	if len(calls) != 0 {
		t.Errorf("expected 0 calls from plain text, got %d", len(calls))
	}
}

func TestParseToolCallsFromText_SingleCall(t *testing.T) {
	content := `TOOL_CALL: runtime.info__read
ARGUMENTS:
{}
`
	calls := parseToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "runtime.info__read" {
		t.Errorf("Name = %q, want %q", calls[0].Name, "runtime.info__read")
	}
	if calls[0].Arguments == nil {
		t.Error("expected non-nil Arguments")
	}
	if calls[0].ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestParseToolCallsFromText_TwoCalls(t *testing.T) {
	content := `TOOL_CALL: runtime.info__read
ARGUMENTS:
{}

TOOL_CALL: runtime.info__list
ARGUMENTS:
{}
`
	calls := parseToolCallsFromText(content)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Name != "runtime.info__read" {
		t.Errorf("call[0].Name = %q, want %q", calls[0].Name, "runtime.info__read")
	}
	if calls[1].Name != "runtime.info__list" {
		t.Errorf("call[1].Name = %q, want %q", calls[1].Name, "runtime.info__list")
	}
	// IDs must be unique
	if calls[0].ID == calls[1].ID {
		t.Errorf("call IDs must be unique, got %q for both", calls[0].ID)
	}
}

func TestParseToolCallsFromText_WithArguments(t *testing.T) {
	content := `TOOL_CALL: custom.capability__write
ARGUMENTS:
{"key": "value", "count": 42}
`
	calls := parseToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "custom.capability__write" {
		t.Errorf("Name = %q, want %q", calls[0].Name, "custom.capability__write")
	}
	if calls[0].Arguments["key"] != "value" {
		t.Errorf("Arguments[key] = %v, want %v", calls[0].Arguments["key"], "value")
	}
	if calls[0].Arguments["count"] != float64(42) {
		t.Errorf("Arguments[count] = %v, want %v", calls[0].Arguments["count"], 42)
	}
}

func TestParseToolCallsFromText_InvalidJSON(t *testing.T) {
	content := `TOOL_CALL: runtime.info__read
ARGUMENTS:
not-valid-json
`
	calls := parseToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "runtime.info__read" {
		t.Errorf("Name = %q, want %q", calls[0].Name, "runtime.info__read")
	}
	// Invalid JSON produces nil arguments (graceful degradation).
	if calls[0].Arguments != nil {
		t.Errorf("expected nil Arguments for invalid JSON, got %v", calls[0].Arguments)
	}
}

func TestParseToolCallsFromText_MissingArguments(t *testing.T) {
	content := `TOOL_CALL: runtime.info__read`
	calls := parseToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "runtime.info__read" {
		t.Errorf("Name = %q, want %q", calls[0].Name, "runtime.info__read")
	}
}

func TestParseToolCallsFromText_InlineArguments(t *testing.T) {
	content := `TOOL_CALL: runtime.info__read
ARGUMENTS: {"inline": true}
`
	calls := parseToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Arguments["inline"] != true {
		t.Errorf("Arguments[inline] = %v, want true", calls[0].Arguments["inline"])
	}
}

// ── Full Adapter Round-Trip Test ─────────────────────────────────────────

// toolCallResponder is a Provider that returns tool calls on the first N
// invocations and final content on the last.
type toolCallResponder struct {
	stages     []string // each non-empty string: "tool:<name>" or final content text
	callIndex  int
	ProviderID ProviderID
}

func (r *toolCallResponder) Infer(_ context.Context, req Request) (*Response, error) {
	if r.callIndex >= len(r.stages) {
		return &Response{
			Content:    "fallback",
			ProviderID: r.ProviderID,
		}, nil
	}
	stage := r.stages[r.callIndex]
	r.callIndex++

	if len(stage) > 5 && stage[:5] == "tool:" {
		name := stage[5:]
		return &Response{
			Content:    "TOOL_CALL: " + name + "\nARGUMENTS:\n{}\n",
			ProviderID: r.ProviderID,
			TokensUsed: len(req.Messages),
		}, nil
	}

	return &Response{
		Content:    stage,
		ProviderID: r.ProviderID,
		TokensUsed: len(req.Messages),
	}, nil
}

func (r *toolCallResponder) ID() ProviderID { return r.ProviderID }

func TestToolEnabledProvider_AdapterRoundTrip(t *testing.T) {
	// Full adapter-level flow:
	//   1. Request with tools → provider gets tool text → provider responds
	//      with TOOL_CALL: runtime.info__read → parser extracts ToolCall
	//   2. Feed ToolResult back → provider gets result text → provider
	//      responds with final content → no tool calls, return content

	responder := &toolCallResponder{
		stages: []string{
			"tool:runtime.info__read",                 // Stage 0: tool call
			"temperature is 22°C and humidity is 60%", // Stage 1: final content
		},
		ProviderID: "test-adapter",
	}

	tp := NewToolEnabledProvider(responder)

	tools := []Tool{
		{
			Name:         "runtime.info__read",
			Description:  "runtime.info / read",
			CapabilityID: "runtime.info",
			OperationID:  "read",
		},
	}

	ctx := context.Background()

	// ── First call: should produce a ToolCall ──
	resp1, err := tp.Infer(ctx, Request{
		Messages: []Message{{Role: "user", Content: "what is the temperature"}},
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("Infer call 1: %v", err)
	}
	if len(resp1.ToolCalls) == 0 {
		t.Fatal("Infer call 1: expected ToolCalls, got none")
	}
	if resp1.Content != "" {
		t.Errorf("Infer call 1: expected empty Content when ToolCalls are present, got %q", resp1.Content)
	}
	if resp1.ToolCalls[0].Name != "runtime.info__read" {
		t.Errorf("ToolCall[0].Name = %q, want %q", resp1.ToolCalls[0].Name, "runtime.info__read")
	}
	if resp1.ToolCalls[0].ID == "" {
		t.Error("ToolCall[0].ID is empty")
	}

	// ── Second call: feed result back, should get final content ──
	resp2, err := tp.Infer(ctx, Request{
		Messages: []Message{{Role: "user", Content: "what is the temperature"}},
		Tools:    tools,
		ToolResults: []ToolResult{
			{
				ToolCallID:  resp1.ToolCalls[0].ID,
				ExecutionID: "exec-001",
				Status:      ToolResultSuccess,
				Result:      map[string]any{"output": `{"os":"linux","architecture":"amd64","go_version":"go1.22"}`},
			},
		},
	})
	if err != nil {
		t.Fatalf("Infer call 2: %v", err)
	}
	if len(resp2.ToolCalls) > 0 {
		t.Errorf("Infer call 2: expected no ToolCalls, got %d", len(resp2.ToolCalls))
	}
	if resp2.Content != "temperature is 22°C and humidity is 60%" {
		t.Errorf("Infer call 2: Content = %q, want %q", resp2.Content, "temperature is 22°C and humidity is 60%")
	}
}

func TestToolEnabledProvider_NoToolsPassthrough(t *testing.T) {
	// When no tools are provided, ToolEnabledProvider passes through to
	// the inner provider without injecting any tool-related text.
	inner := NewMockProvider("test", "this is a regular response")
	tp := NewToolEnabledProvider(inner)

	resp, err := tp.Infer(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if resp.Content != "this is a regular response" {
		t.Errorf("Content = %q, want %q", resp.Content, "this is a regular response")
	}
	if len(resp.ToolCalls) > 0 {
		t.Errorf("expected no ToolCalls, got %d", len(resp.ToolCalls))
	}
}

func TestToolEnabledProvider_ProviderError(t *testing.T) {
	// Errors from the inner provider propagate through unchanged.
	inner := &errorProvider{}
	tp := NewToolEnabledProvider(inner)

	_, err := tp.Infer(context.Background(), Request{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Tools:    []Tool{{Name: "test__op"}},
	})
	if err == nil {
		t.Fatal("expected error from inner provider")
	}
	if err.Error() != "inner provider error" {
		t.Errorf("error = %q, want %q", err.Error(), "inner provider error")
	}
}

type errorProvider struct{}

func (e *errorProvider) Infer(_ context.Context, _ Request) (*Response, error) {
	return nil, fmt.Errorf("inner provider error")
}

func (e *errorProvider) ID() ProviderID { return "error" }

func TestToolEnabledProvider_TwoSequentialToolCalls(t *testing.T) {
	// Provider returns two sequential tool calls, then final content.
	responder := &toolCallResponder{
		stages: []string{
			"tool:runtime.info__read",                     // Stage 0
			"tool:runtime.info__list",                     // Stage 1
			"result: 2 operations completed successfully", // Stage 2: final
		},
		ProviderID: "test-seq",
	}

	tp := NewToolEnabledProvider(responder)

	tools := []Tool{
		{Name: "runtime.info__read", Description: "read", CapabilityID: "runtime.info", OperationID: "read"},
		{Name: "runtime.info__list", Description: "list", CapabilityID: "runtime.info", OperationID: "list"},
	}

	ctx := context.Background()
	req := Request{
		Messages: []Message{{Role: "user", Content: "run both tools"}},
		Tools:    tools,
	}

	// Call 1: tool call for read
	resp, err := tp.Infer(ctx, req)
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "runtime.info__read" {
		t.Fatalf("call 1: expected 1 tool call to runtime.info__read, got %v", resp.ToolCalls)
	}

	// Feed result back for read
	req.ToolResults = append(req.ToolResults, ToolResult{
		ToolCallID: resp.ToolCalls[0].ID, Status: ToolResultSuccess,
		Result: map[string]any{"output": "ok"},
	})

	// Call 2: tool call for list
	resp, err = tp.Infer(ctx, req)
	if err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "runtime.info__list" {
		t.Fatalf("call 2: expected 1 tool call to runtime.info__list, got %v", resp.ToolCalls)
	}

	// Feed result back for list
	req.ToolResults = append(req.ToolResults, ToolResult{
		ToolCallID: resp.ToolCalls[0].ID, Status: ToolResultSuccess,
		Result: map[string]any{"output": "listed"},
	})

	// Call 3: final content
	resp, err = tp.Infer(ctx, req)
	if err != nil {
		t.Fatalf("call 3: %v", err)
	}
	if len(resp.ToolCalls) > 0 {
		t.Fatalf("call 3: expected no tool calls, got %d", len(resp.ToolCalls))
	}
	if resp.Content != "result: 2 operations completed successfully" {
		t.Errorf("call 3: Content = %q, want %q", resp.Content, "result: 2 operations completed successfully")
	}
}
