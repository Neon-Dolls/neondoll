// Package dollmind provides the core cognition pipeline for NeonDoll.
//
// Phase 3 — Spark Thinks: L2 Plan uses the Inference Provider and the L1
// Orientation to produce structured semantic planning output describing what
// Spark thinks should happen next.

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

// Plan is the structured semantic result of L2 cognition — what Spark thinks
// should happen next when L1 orientation determined the event matters.
//
// The Plan is purely semantic: it describes proposed courses of action, observed
// state about goals and drives, and whether re-evaluation is warranted. It does
// NOT contain executable commands, action types, or instructions that imply
// Core execution or Goal mutation. The caller interprets the Plan's semantics.
//
// All fields except Summary are optional. JSON serialisation is used for
// model output parsing.
type Plan struct {
	Summary        string   `json:"summary"`          // what Spark thinks should happen next
	ProposedAction string   `json:"proposed_action"`  // semantic description of course of action
	Observations   []string `json:"observations"`     // noticed things about goals, drives, context
	ShouldReorient bool     `json:"should_reorient"`  // whether to re-evaluate later
}

// planJSON is the strictly parsed JSON shape expected from model output.
type planJSON struct {
	Summary        string   `json:"summary"`
	ProposedAction string   `json:"proposed_action"`
	Observations   []string `json:"observations"`
	ShouldReorient bool     `json:"should_reorient"`
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
  "summary": "what you think should happen next",
  "proposed_action": "semantic description of the course of action you recommend",
  "observations": ["relevant observation about goals, drives, or context", "another observation, if any"],
  "should_reorient": false
}

All fields are optional except "summary". "observations" may be empty. "should_reorient" indicates whether you want to re-evaluate this situation later. Describe what should happen semantically rather than issuing commands.`)

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

	// Nil-safety for observations
	if parsed.Observations == nil {
		parsed.Observations = []string{}
	}

	return &Plan{
		Summary:        parsed.Summary,
		ProposedAction: parsed.ProposedAction,
		Observations:   parsed.Observations,
		ShouldReorient: parsed.ShouldReorient,
	}, nil
}

// Plan executes L2 planning: it constructs a full context from the Doll's
// canonical state, current event, and L1 orientation, then sends an inference
// request with purpose "plan" and parses the structured plan output.
//
// Plan does NOT mutate state. The returned Plan is purely semantic — the
// caller interprets what Spark thinks should happen next.
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
			"event_type":       eventType,
			"summary":          plan.Summary,
			"should_reorient":  plan.ShouldReorient,
		})

	return plan, nil
}