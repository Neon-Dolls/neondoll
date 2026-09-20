// Package dollmind provides the core cognition pipeline for NeonDoll.
//
// Phase 2 — Spark Orients: L1 Orient uses the Inference Provider to produce
// a structured Orientation from the Doll's canonical state.

package dollmind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
)

// Orientation is the structured result of L1 cognition — the Doll's
// interpretation of a situation, whether it matters enough to warrant
// further planning, and the reasoning behind that decision.
//
// Orientation is clean structured data with no embedded model identity.
type Orientation struct {
	Summary string `json:"summary"`
	Matters bool   `json:"matters"`
	Reason  string `json:"reason"`
}

// orientationJSON is the strictly parsed JSON shape expected from model output.
type orientationJSON struct {
	Summary string `json:"summary"`
	Matters bool   `json:"matters"`
	Reason  string `json:"reason"`
}

// buildOrientPrompt constructs the full orientation context from the
// current DollState and the incoming event.
//
// It includes only canonical state from Core 1: identity, soul, owner,
// drives, goals, and recent memory. This is a simple string concatenation —
// no vector retrieval, no generalized context engine.
func buildOrientPrompt(state *dollstate.DollState, eventType events.Type, input string) string {
	var parts []string

	// Identity
	if name := state.Identity.CanonicalName; name != "" {
		parts = append(parts, "You are "+name+".")
	}

	// Soul
	if soul := state.Soul.Content; soul != "" {
		parts = append(parts, "Your nature: "+soul)
	}

	// Owner
	if owner := state.Owner.Name; owner != "" {
		parts = append(parts, "Your owner is "+owner+".")
	}

	// Drives
	if len(state.Drives.Items) > 0 {
		var lines []string
		for _, d := range state.Drives.Items {
			s := d.Name
			if d.Description != "" {
				s += ": " + d.Description
			}
			lines = append(lines, "- "+s)
		}
		parts = append(parts, "Your drives:\n"+strings.Join(lines, "\n"))
	}

	// Goals — active and completed both matter for orientation
	if len(state.Goals.Items) > 0 {
		var lines []string
		for _, g := range state.Goals.Items {
			s := g.Name + " [" + g.State + "]"
			if g.Description != "" {
				s += " — " + g.Description
			}
			lines = append(lines, "- "+s)
		}
		parts = append(parts, "Your goals:\n"+strings.Join(lines, "\n"))
	}

	// Recent memory items (last 5 for context)
	if len(state.Memories.Items) > 0 {
		var lines []string
		start := 0
		if n := len(state.Memories.Items); n > 5 {
			start = n - 5
		}
		for _, m := range state.Memories.Items[start:] {
			lines = append(lines, fmt.Sprintf("[%s] %s", m.Kind, m.Content))
		}
		parts = append(parts, "Recent memory:\n"+strings.Join(lines, "\n"))
	}

	// Event
	parts = append(parts, "Event type: "+string(eventType)+"\nEvent: "+input)

	// Task instruction — purpose "orientation"
	parts = append(parts, `Your task is ORIENTATION: interpret what is happening, decide whether it matters (requires further planning), and explain why. Return ONLY valid JSON with no markdown formatting or code blocks around it. Use this exact format: {"summary":"...","matters":true,"reason":"..."}`)

	return strings.Join(parts, "\n\n")
}

// parseOrientation extracts and validates a structured Orientation from raw
// model output. It strips common wrapping (markdown code fences, leading/
// trailing whitespace) before JSON parsing.
//
// Returns an error if the output is unparsable or required fields are empty.
func parseOrientation(raw string) (*Orientation, error) {
	cleaned := strings.TrimSpace(raw)

	// Strip markdown code fences if present
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var parsed orientationJSON
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return nil, fmt.Errorf("invalid orientation JSON: %w", err)
	}

	// Validate required non-empty fields
	if parsed.Summary == "" {
		return nil, fmt.Errorf("orientation missing required field: summary")
	}
	if parsed.Reason == "" {
		return nil, fmt.Errorf("orientation missing required field: reason")
	}

	return &Orientation{
		Summary: parsed.Summary,
		Matters: parsed.Matters,
		Reason:  parsed.Reason,
	}, nil
}

// Orient executes L1 orientation: it constructs a full context from the
// Doll's canonical state and current event, sends the inference request
// with purpose "orientation", and parses/validates the structured response.
//
// Orient does NOT mutate state. The caller uses the returned Orientation
// to decide whether L2 planning is warranted.
func (s *Scheduler) Orient(ctx context.Context, eventType events.Type, input string) (*Orientation, error) {
	if s.mindAPI == nil {
		return nil, fmt.Errorf("mind API not set: cannot access state")
	}

	state := s.mindAPI.State()
	prompt := buildOrientPrompt(state, eventType, input)

	resp, err := s.provider.Infer(ctx, inference.Request{
		Messages: []inference.Message{
			{Role: "system", Content: prompt},
		},
		Temperature: 0.3,
		MaxTokens:   512,
		Purpose:     inference.PurposeOrient,
	})
	if err != nil {
		return nil, fmt.Errorf("orient inference: %w", err)
	}

	orient, err := parseOrientation(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("orient parse: %w", err)
	}

	s.log.Info("orientation complete",
		map[string]any{
			"event_type": eventType,
			"matters":    orient.Matters,
			"summary":    orient.Summary,
		})

	return orient, nil
}