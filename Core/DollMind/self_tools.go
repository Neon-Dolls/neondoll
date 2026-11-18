package dollmind

import (
	"context"
	"errors"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	selfstate "github.com/Neon-Dolls/neondoll/Core/Self"
)

const (
	selfCapability      = "core.self"
	selfOperationRevise = "revise"
)

func selfRevisionTool() inference.Tool {
	return inference.Tool{
		Name:         selfCapability + inference.ToolNameDelimiter + selfOperationRevise,
		Description:  "Replace your complete developing Self Markdown when your understanding of who you are has meaningfully changed. This revises Self only: it does not alter Soul, Memory, relationships, or authority. Use the current revision_id for stale-update protection; use an empty ID only when no developed Self exists. A Self change may be remembered separately with core.memory__remember when that meaning is worth retaining.",
		CapabilityID: selfCapability, OperationID: selfOperationRevise,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"expected_revision_id": map[string]any{"type": "string"},
			"content":              map[string]any{"type": "string", "description": "Complete replacement Markdown for the current Self"},
		}, "required": []string{"expected_revision_id", "content"}},
	}
}

func (executor *ToolExecutor) reviseSelf(ctx context.Context, call inference.ToolCall) inference.ToolResult {
	if executor.self == nil {
		return failureResult(call.ID, "self_unavailable", "developing Self service is unavailable")
	}
	expected, expectedOK := call.Arguments["expected_revision_id"].(string)
	content, contentOK := call.Arguments["content"].(string)
	if !expectedOK || !contentOK || strings.TrimSpace(content) == "" {
		return failureResult(call.ID, "invalid_arguments", "expected_revision_id and non-empty content are required")
	}
	revision, err := executor.self.Revise(ctx, selfstate.RevisionRequest{ExpectedRevisionID: expected, Content: content})
	if err != nil {
		switch {
		case errors.Is(err, selfstate.ErrStaleRevision):
			return failureResult(call.ID, "self_stale_revision", err.Error())
		case errors.Is(err, selfstate.ErrContentTooLarge):
			return failureResult(call.ID, "self_too_large", err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return failureResult(call.ID, "self_cancelled", err.Error())
		default:
			return failureResult(call.ID, "self_revision_failed", err.Error())
		}
	}
	return inference.ToolResult{ToolCallID: call.ID, Status: inference.ToolResultSuccess, Result: map[string]any{
		"revision_id": revision.RevisionID, "revision": revision.Revision,
		"created_at": revision.CreatedAt, "updated_at": revision.UpdatedAt,
		"active": true, "complete_replacement": true,
		"memory_created": false, "memory_note": "Remember the meaning of this change separately only if it is worth retaining.",
	}}
}
