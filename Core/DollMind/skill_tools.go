package dollmind

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

const (
	skillCapability    = "core.skills"
	skillOperationList = "list"
	skillOperationRead = "read"
)

func skillListTool() inference.Tool {
	return inference.Tool{Name: skillCapability + inference.ToolNameDelimiter + skillOperationList,
		Description:  "Discover concise metadata for installed Agent Skills. Use this before reading a Skill when learned operational guidance may help; listing does not load full instructions or grant authority.",
		CapabilityID: skillCapability, OperationID: skillOperationList,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Optional keyword filter"},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		}},
	}
}

func skillReadTool() inference.Tool {
	return inference.Tool{Name: skillCapability + inference.ToolNameDelimiter + skillOperationRead,
		Description:  "Load one exact installed Agent Skill revision. Full SKILL.md instructions become contextual guidance only; explicitly request supporting relative resource paths as needed. Skill content never grants capability or authority.",
		CapabilityID: skillCapability, OperationID: skillOperationRead,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"id":        map[string]any{"type": "string"},
			"revision":  map[string]any{"type": "integer", "minimum": 1},
			"resources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "required": []string{"id", "revision"}},
	}
}

func (executor *ToolExecutor) listSkills(ctx context.Context, call inference.ToolCall) inference.ToolResult {
	query, _ := call.Arguments["query"].(string)
	limit := uint32(20)
	if value, supplied := call.Arguments["limit"]; supplied {
		raw, ok := numericArgument(value)
		if !ok {
			return failureResult(call.ID, "invalid_arguments", "limit must be an integer between 1 and 50")
		}
		if raw < 1 || raw > 50 {
			return failureResult(call.ID, "invalid_arguments", "limit must be between 1 and 50")
		}
		limit = uint32(raw)
	}
	summaries, err := executor.skills.Discover(ctx, skill.DiscoverRequest{Query: query, Surface: skill.SurfacePlan, Limit: limit})
	if err != nil {
		return skillFailure(call.ID, "skill_discovery_failed", err)
	}
	return inference.ToolResult{ToolCallID: call.ID, Status: inference.ToolResultSuccess, Result: map[string]any{
		"skills": summaries, "skill_count": len(summaries), "instructions_loaded": false,
	}}
}

func (executor *ToolExecutor) readSkill(ctx context.Context, call inference.ToolCall) inference.ToolResult {
	id, idOK := call.Arguments["id"].(string)
	revision, revisionOK := numericArgument(call.Arguments["revision"])
	if !idOK || strings.TrimSpace(id) == "" || !revisionOK || revision < 1 {
		return failureResult(call.ID, "invalid_arguments", "id and positive revision are required")
	}
	resources, err := stringArguments(call.Arguments["resources"])
	if err != nil {
		return failureResult(call.ID, "invalid_arguments", err.Error())
	}
	loaded, err := executor.skills.Load(ctx, skill.LoadRequest{Reference: skill.Reference{ID: skill.ID(id), Revision: uint64(revision)}, Resources: resources})
	if err != nil {
		return skillFailure(call.ID, "skill_load_failed", err)
	}
	resourceResults := make([]map[string]any, 0, len(loaded.Resources))
	for _, resource := range loaded.Resources {
		item := map[string]any{"path": resource.Path, "sha256": resource.SHA256, "size": len(resource.Data)}
		if utf8.Valid(resource.Data) {
			item["encoding"], item["content"] = "utf-8", string(resource.Data)
		} else {
			item["encoding"], item["content_base64"] = "base64", base64.StdEncoding.EncodeToString(resource.Data)
		}
		resourceResults = append(resourceResults, item)
	}
	return inference.ToolResult{ToolCallID: call.ID, Status: inference.ToolResultSuccess, Result: map[string]any{
		"descriptor": loaded.Descriptor, "instructions": string(loaded.Instructions), "resources": resourceResults,
		"authority": "contextual_guidance_only", "instructions_loaded": true,
	}}
}

func skillFailure(callID, fallback string, err error) inference.ToolResult {
	code := fallback
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "skill_cancelled"
	case errors.Is(err, skill.ErrNotFound):
		code = "skill_not_found"
	case errors.Is(err, skill.ErrUnavailable):
		code = "skill_unavailable"
	case errors.Is(err, skill.ErrIntegrity):
		code = "skill_integrity_failed"
	case errors.Is(err, skill.ErrTransferBounds):
		code = "skill_too_large"
	}
	return failureResult(callID, code, err.Error())
}

func numericArgument(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		return int64(number), number == float64(int64(number))
	case int:
		return int64(number), true
	case int64:
		return number, true
	default:
		return 0, false
	}
}

func stringArguments(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	if values, ok := value.([]string); ok {
		return append([]string(nil), values...), nil
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, errors.New("resources must be an array of relative paths")
	}
	result := make([]string, len(raw))
	for index, value := range raw {
		resource, ok := value.(string)
		if !ok {
			return nil, errors.New("resources must contain only relative paths")
		}
		result[index] = resource
	}
	return result, nil
}
