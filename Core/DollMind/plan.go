// Package dollmind provides the core cognition pipeline for NeonDoll.
//
// Phase 3 — Spark Thinks: L2 Plan uses the Inference Provider and the L1
// Orientation to produce an actionable plan when the doll determines an
// event matters.

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

// Plan is the structured result of L2 cognition — a course of action
// the Doll decided to take when L1 orientation determined the event matters.
//
// Phase 3 minimum: summary of intent + list of actions to execute.
// Future phases add goal modifications and scheduled intentions.
type Plan struct {
	Summary string       `json:"summary"`  // what this plan is about
	Actions []PlanAction `json:"actions"`  // immediate actions to take
}

// PlanAction is a single actionable item within a Plan.
//
// Type describes what kind of action. Known types for Phase 3:
//   - "respond"     — reply to the user (payload: {"text": "..."})
//   - "create_goal" — establish a new goal (payload: {"name":"...","description":"..."})
//   - "complete_goal" — mark a goal finished (payload: {"name":"..."})
//
// Payload is a flexible map so action types can evolve without schema changes.
type PlanAction struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// planJSON is the strictly parsed JSON shape expected from model output.
type planJSON struct {
	Summary string          `json:"summary"`
	Actions []planActionJSON `json:"actions"`
}

type planActionJSON struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// buildPlanPrompt constructs the full planning context from the Doll's
// canonical state, the current event, and the L1 orientation that determined
// the event matters.
//
// It includes the same state context as orientation plus the orientation
// result itself, so the model can reason about what to do.
func buildPlanPrompt(state *dollstate.DollState, eventType events.Type, input string, orientation *Orientation) string {
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

	// Goals
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

	// Recent memory items (last 5)
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

	// L1 orientation result
	parts = append(parts, fmt.Sprintf(
		"Orientation: %s\nReason: %s",
		orientation.Summary, orientation.Reason,
	))

	// Task instruction — purpose "plan"
	parts = append(parts, `Your task is PLANNING: decide what to do about this situation. Return ONLY valid JSON with no markdown formatting or code blocks. Use this format:
{
  "summary": "what you plan to do",
  "actions": [
    {"type": "respond", "payload": {"text": "your response message"}},
    {"type": "create_goal", "payload": {"name": "...", "description": "..."}},
    {"type": "complete_goal", "payload": {"name": "..."}}
  ]
}
You may include zero or more actions. A "respond" action is the most common. Include create_goal or complete_goal only when you intend to modify your goal set.`)

	return strings.Join(parts, "\n\n")
}

// parsePlan extracts and validates a structured Plan from raw model output.
// It strips common wrapping (markdown code fences, leading/trailing whitespace)
// before JSON parsing.
func parsePlan(raw string) (*Plan, error) {
	cleaned := strings.TrimSpace(raw)

	// Strip markdown code fences if present
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var parsed planJSON
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return nil, fmt.Errorf("invalid plan JSON: %w", err)
	}

	// Validate required field
	if parsed.Summary == "" {
		return nil, fmt.Errorf("plan missing required field: summary")
	}

	// Actions is optional — an empty list means "act on orientation only,
	// no immediate action needed" — but ensure it's not nil for clean serialization.
	if parsed.Actions == nil {
		parsed.Actions = []planActionJSON{}
	}

	actions := make([]PlanAction, len(parsed.Actions))
	for i, a := range parsed.Actions {
		if a.Type == "" {
			return nil, fmt.Errorf("plan action %d missing required field: type", i)
		}
		if a.Payload == nil {
			a.Payload = map[string]any{}
		}
		actions[i] = PlanAction{
			Type:    a.Type,
			Payload: a.Payload,
		}
	}

	return &Plan{
		Summary: parsed.Summary,
		Actions: actions,
	}, nil
}

// Plan executes L2 planning: it constructs a full context from the Doll's
// canonical state, current event, and L1 orientation, then sends an inference
// request with purpose "plan" and parses the structured plan output.
//
// Plan does NOT mutate state. The caller uses the returned Plan to decide
// how to execute actions, update goals, or schedule further cognition.
func (s *Scheduler) Plan(ctx context.Context, eventType events.Type, input string, orientation *Orientation) (*Plan, error) {
	if s.mindAPI == nil {
		return nil, fmt.Errorf("mind API not set: cannot access state")
	}
	if orientation == nil {
		return nil, fmt.Errorf("plan requires a non-nil orientation")
	}

	state := s.mindAPI.State()
	prompt := buildPlanPrompt(state, eventType, input, orientation)

	resp, err := s.provider.Infer(ctx, inference.Request{
		Messages: []inference.Message{
			{Role: "system", Content: prompt},
		},
		Temperature: 0.3,
		MaxTokens:   1024,
		Purpose:     inference.PurposePlan,
	})
	if err != nil {
		return nil, fmt.Errorf("plan inference: %w", err)
	}

	plan, err := parsePlan(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("plan parse: %w", err)
	}

	s.log.Info("plan complete",
		map[string]any{
			"event_type":     eventType,
			"summary":        plan.Summary,
			"action_count":   len(plan.Actions),
		})

	return plan, nil
}