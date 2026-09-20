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
	"time"

	"github.com/google/uuid"

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

	// Future cognition — set when Spark decides that future cognitive
	// attention is warranted. The Core materialises these into a pending
	// IntentionItem in Doll State after parsing.
	RequestFutureCognition bool   `json:"request_future_cognition"`
	FutureSubject          string `json:"future_subject,omitempty"`
	FutureReason           string `json:"future_reason,omitempty"`
	FutureWakeTime         string `json:"future_wake_time,omitempty"`
}

// planJSON is the strictly parsed JSON shape expected from model output.
type planJSON struct {
	Summary        string   `json:"summary"`
	ProposedAction string   `json:"proposed_action"`
	Observations   []string `json:"observations"`
	ShouldReorient bool     `json:"should_reorient"`

	RequestFutureCognition bool   `json:"request_future_cognition"`
	FutureSubject          string `json:"future_subject,omitempty"`
	FutureReason           string `json:"future_reason,omitempty"`
	FutureWakeTime         string `json:"future_wake_time,omitempty"`
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
	if eventType == events.TypeInternalWake {
		parts = append(parts, "An intention from within your own mind has become due.\n\n"+input)
	} else {
		parts = append(parts, "Event type: "+string(eventType)+"\nEvent: "+input)
	}

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
  "should_reorient": false,
  "request_future_cognition": false,
  "future_subject": "",
  "future_reason": "",
  "future_wake_time": ""
}

All fields are optional except "summary". "observations" may be empty. "should_reorient" indicates whether you want to re-evaluate this situation later. "request_future_cognition" indicates whether future cognitive attention is warranted — set to true only when the Doll should specifically reconsider something at a future time. When true, "future_subject" describes what to reconsider, "future_reason" explains why, and "future_wake_time" is the RFC 3339 UTC timestamp when this cognition should occur. Describe what should happen semantically rather than issuing commands.`)

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

		RequestFutureCognition: parsed.RequestFutureCognition,
		FutureSubject:          parsed.FutureSubject,
		FutureReason:           parsed.FutureReason,
		FutureWakeTime:         parsed.FutureWakeTime,
	}, nil
}

// Plan executes L2 planning: it constructs a full context from the Doll's
// canonical state, current event, and L1 orientation, then sends an inference
// request with purpose "plan" and parses the structured plan output.
//
// Plan may mutate Doll State: when the parsed Plan requests future cognition,
// Core validates the wake time and materialises a canonical pending
// IntentionItem into the Doll's state. The boolean return indicates whether
// state was mutated.
func (s *Scheduler) Plan(ctx context.Context, eventType events.Type, input string, orientation *Orientation) (*Plan, bool, error) {
	if s.mindAPI == nil {
		return nil, false, fmt.Errorf("mind API not set: cannot access state")
	}
	if orientation == nil {
		return nil, false, fmt.Errorf("plan requires a non-nil orientation")
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
		return nil, false, fmt.Errorf("plan inference: %w", err)
	}

	plan, err := parsePlan(resp.Content)
	if err != nil {
		return nil, false, fmt.Errorf("plan parse: %w", err)
	}

	s.log.Info("plan complete",
		map[string]any{
			"event_type":       eventType,
			"summary":          plan.Summary,
			"should_reorient":  plan.ShouldReorient,
			"request_future":   plan.RequestFutureCognition,
		})

	dirty, err := s.materialiseIntention(plan)
	if err != nil {
		return nil, false, fmt.Errorf("plan intention: %w", err)
	}
	return plan, dirty, nil
}

// materialiseIntention converts a Plan's future cognition request into a
// canonical pending IntentionItem stored in Doll State and durably persisted
// through the Core persistence boundary.
//
// It returns true if state was mutated, and an error if the intention request
// is rejected or persistence fails. Validation rules:
//   - RequestFutureCognition must be true
//   - FutureSubject must be non-empty
//   - FutureWakeTime must be a valid RFC 3339 timestamp in the future
//
// If validation fails, the intention is not created and an error is returned
// so the caller can decide how to handle the rejected request. If persistence
// fails, the in-memory state still reflects the mutation but the error is
// propagated so the caller can retry or recover.
func (s *Scheduler) materialiseIntention(plan *Plan) (bool, error) {
	if !plan.RequestFutureCognition {
		return false, nil
	}

	// Validate subject
	if plan.FutureSubject == "" {
		return false, fmt.Errorf("intention rejected: future_subject is empty")
	}

	// Validate wake time
	wakeTime, err := time.Parse(time.RFC3339, plan.FutureWakeTime)
	if err != nil {
		return false, fmt.Errorf("intention rejected: invalid future_wake_time %q: %w", plan.FutureWakeTime, err)
	}
	if !wakeTime.After(s.timeProvider()) {
		return false, fmt.Errorf("intention rejected: future_wake_time %q is not in the future", plan.FutureWakeTime)
	}

	// Create and store the canonical IntentionItem
	intention := dollstate.IntentionItem{
		ID:          uuid.New().String(),
		Subject:     plan.FutureSubject,
		Description: plan.FutureReason,
		WakeTime:    plan.FutureWakeTime,
		State:       dollstate.IntentionStatePending,
	}

	state := s.mindAPI.State()
	state.Intentions.Items = append(state.Intentions.Items, intention)

	s.log.Info("intention materialised",
		map[string]any{
			"id":        intention.ID,
			"subject":   intention.Subject,
			"wake_time": intention.WakeTime,
		})

	// Durably persist through the existing Core persistence boundary.
	if err := s.mindAPI.Save(); err != nil {
		return false, fmt.Errorf("persist intention: %w", err)
	}

	return true, nil
}