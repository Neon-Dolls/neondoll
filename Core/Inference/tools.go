package inference

import (
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/Body"
)

// ── Phase 2 — Capability → Tool Projection ──────────────────────────────

// ToolNameDelimiter separates components in a deterministic tool name.
const ToolNameDelimiter = "__"

// ProjectToolsFromCapabilities produces Tool definitions from a Body's
// capabilities. It filters to operations that are eligible for the cognition
// run (available and non-empty) and produces deterministic tool names.
//
// Three boundaries are distinct:
//  1. capability available — the Body declares the capability with Available=true
//  2. tool exposed — the tool definition is included in the request to the provider
//  3. execution authorized — the Guard must still evaluate authority separately
//
// Tool exposure does NOT imply authority. The caller must pass the resulting
// Tool definitions through Guard.Execute before any operation is performed.
//
// For Core 2, when an explicit valid Body target is required, use the provided
// targetBodyID. Otherwise Local Body is used. The architecture preserves the
// possibility of alternative Body targets in future milestones.
func ProjectToolsFromCapabilities(targetBody body.Body, targetBodyID body.BodyID) []Tool {
	caps := targetBody.Describe()
	if len(caps) == 0 {
		return nil
	}

	var tools []Tool
	for _, cap := range caps {
		if !cap.Available {
			continue
		}
		for _, op := range cap.Operations {
			if op == "" {
				continue
			}
			// Build description from capability metadata if available.
			desc := cap.ID + " / " + op
			if d, ok := cap.Metadata["description"]; ok && d != "" {
				desc = d
			}

			tools = append(tools, Tool{
				Name:         toolName(targetBodyID, cap.ID, op),
				Description:  desc,
				InputSchema:  nil, // Capability has no InputSchema field; this is reserved for future use.
				CapabilityID: cap.ID,
				OperationID:  op,
				TargetBodyID: string(targetBodyID),
			})
		}
	}
	return tools
}

// toolName creates a deterministic tool name from capability and operation
// identifiers. The pattern is: "<capability>__<operation>"
// The tool identity represents the semantic capability/operation pairing,
// not the target Body — Body routing is tracked as explicit internal
// metadata (Tool.TargetBodyID).
func toolName(bodyID body.BodyID, capID string, opID string) string {
	return fmt.Sprintf("%s%s%s", capID, ToolNameDelimiter, opID)
}

// ToolMappingTable recovers the internal routing metadata from a tool name.
// It returns the capability ID, operation ID, and a boolean indicating success.
// Body ID is returned empty in Core 2; it will be populated in future milestones
// when alternative Body targets are supported.
func ToolMappingTable(toolName string) (bodyID string, capID string, opID string, ok bool) {
	// Expected format: "<capID>__<opID>"
	if len(toolName) == 0 {
		return "", "", "", false
	}

	// Find the delimiter separating capability from operation.
	delim := ToolNameDelimiter
	idx := findDelimiter(toolName, delim)
	if idx < 0 {
		return "", "", "", false
	}

	capID = toolName[:idx]
	opID = toolName[idx+len(delim):]
	if capID == "" || opID == "" {
		return "", "", "", false
	}
	return "", capID, opID, true
}

// findDelimiter finds the first occurrence of the delimiter in s.
func findDelimiter(s, delim string) int {
	for i := 0; i <= len(s)-len(delim); i++ {
		if s[i:i+len(delim)] == delim {
			return i
		}
	}
	return -1
}
