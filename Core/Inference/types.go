package inference

import "context"

// ProviderID identifies an inference provider.
type ProviderID string

// Purpose signals why a request is being made — the semantic orientation of
// the inference call. The smallest possible contract between cognition and
// infrastructure: the provider knows what kind of response context is needed,
// but model selection and routing remain infrastructure concerns.
type Purpose string

const (
	PurposeOrient  Purpose = "orient"  // L1: interpret situation, decide if it matters
	PurposePlan    Purpose = "plan"    // L2: produce an actionable plan from orientation
	PurposeRespond Purpose = "respond" // L2+: generate a response to the user/event
)

// ── Phase 1 — Provider-Neutral Inference Tool Types ──────────────────────

// Tool defines a capability-exposed operation that the provider can invoke.
// Tools are provider-neutral — no OpenAI, Anthropic, or other vendor types.
// The name is deterministic and stable across calls.
type Tool struct {
	// Name is the deterministic, stable identifier (e.g. "runtime.info__read").
	// The tool identity represents the semantic capability/operation pairing,
	// not the target Body — Body routing is explicit internal metadata.
	Name string `json:"name"`

	// Description explains what the tool does, for the provider's consumption.
	Description string `json:"description"`

	// InputSchema describes the expected arguments as a JSON Schema-like map.
	// The structure is provider-neutral; adapters convert to native format.
	InputSchema map[string]any `json:"input_schema,omitempty"`

	// CapabilityID and OperationID retain the routing metadata needed to
	// recover the Body + capability + operation when a ToolCall returns.
	// These are internal routing fields, not serialized to the provider.
	CapabilityID string `json:"-"`
	OperationID  string `json:"-"`
	TargetBodyID string `json:"-"`
}

// ToolCall represents a provider's request to invoke a tool.
// It is provider-neutral — normalized from whatever native format the
// provider uses (e.g. OpenAI function calls, Anthropic tool_use blocks).
type ToolCall struct {
	// ID is the provider-assigned identifier for this call (e.g. "call_abc123").
	ID string `json:"id"`

	// Name is the tool name the provider chose to invoke.
	Name string `json:"name"`

	// Arguments is the structured arguments the provider supplied.
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolResultStatus indicates whether a tool execution succeeded or failed.
type ToolResultStatus string

const (
	ToolResultSuccess ToolResultStatus = "success"
	ToolResultFailure ToolResultStatus = "failure"
)

// ToolResult carries the outcome of executing a ToolCall.
// Every ToolCall produces exactly one correlated ToolResult.
type ToolResult struct {
	// ToolCallID correlates this result to the original ToolCall.
	ToolCallID string `json:"tool_call_id"`

	// ExecutionID traces this result to the concrete execution attempt
	// that produced it. Multiple results may share a ToolCallID when a
	// future/eligible alternative Body produces independently authorized
	// execution attempts for one Tool Call.
	ExecutionID string `json:"execution_id,omitempty"`

	// Status indicates success or failure.
	Status ToolResultStatus `json:"status"`

	// Result is the structured output on success (map may hold any shape).
	Result map[string]any `json:"result,omitempty"`

	// Error is the structured error information on failure.
	Error *ToolError `json:"error,omitempty"`
}

// ToolError carries structured error information for a failed tool execution.
type ToolError struct {
	// Code is a machine-readable error code (e.g. "unknown_tool", "invalid_arguments",
	// "denied", "execution_failed").
	Code string `json:"code"`

	// Message is a human-readable description of the error.
	Message string `json:"message"`
}

// ── Request and Response ─────────────────────────────────────────────────

// Request is a generic inference request.
type Request struct {
	Model       string
	Messages    []Message
	Temperature float64
	MaxTokens   int
	Purpose     Purpose

	// Tools is an optional list of Tool definitions. When nil or empty,
	// the provider operates in no-tool mode (existing behavior preserved).
	// When non-empty, the provider is expected to expose these tools and
	// may respond with ToolCalls instead of final content.
	Tools []Tool

	// ToolResults carries results from prior tool executions back to the
	// provider for continuation turns in the same Cognition Run.
	// Set on subsequent loop iterations; nil/empty on the first call.
	ToolResults []ToolResult
}

// Message in a conversation.
type Message struct {
	Role    string
	Content string
}

// Response from inference.
// A response is EITHER final content (Content is non-empty) OR one or more
// ToolCalls (ToolCalls is non-empty). It is never both.
type Response struct {
	// Content is the final text response when no tool calls were made.
	// Empty when ToolCalls is populated.
	Content string

	// ToolCalls is populated when the provider requests tool execution.
	// Nil/empty when Content is the final answer.
	ToolCalls []ToolCall

	ProviderID ProviderID
	TokensUsed int
}

// Provider is the interface for inference backends.
type Provider interface {
	Infer(ctx context.Context, req Request) (*Response, error)
	ID() ProviderID
}

// MockProvider for testing.
type MockProvider struct {
	id       ProviderID
	response string
}

func NewMockProvider(id ProviderID, response string) *MockProvider {
	return &MockProvider{id: id, response: response}
}

func (m *MockProvider) Infer(_ context.Context, req Request) (*Response, error) {
	return &Response{
		Content:    m.response,
		ProviderID: m.id,
		TokensUsed: len(req.Messages),
	}, nil
}

func (m *MockProvider) ID() ProviderID { return m.id }
