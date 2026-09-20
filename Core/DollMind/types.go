package dollmind

import (
	"context"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// Level maps to the Doll Mind cognition hierarchy.
type Level int

const (
	LevelReflex  Level = 0
	LevelOrient  Level = 1
	LevelPlan    Level = 2
	LevelReflect Level = 3
	LevelDream   Level = 4
)

// DefaultRetryDuration is the default WakeTime bump applied when
// cognition decides a due intention does not warrant action.
const DefaultRetryDuration = 1 * time.Hour

func (l Level) String() string {
	switch l {
	case LevelReflex:
		return "reflex"
	case LevelOrient:
		return "orient"
	case LevelPlan:
		return "plan"
	case LevelReflect:
		return "reflect"
	case LevelDream:
		return "dream"
	default:
		return "unknown"
	}
}

// MindAPI is the interface between cognition and the inference provider.
type MindAPI interface {
	Inference() inference.Provider
	State() *dollstate.DollState
	// Save durably persists the current Doll State through the Core
	// persistence boundary. Called by Plan after materialising Intention.
	Save() error
}

// Result from a cognition cycle.
type Result struct {
	Level       Level
	Actions     []Action
	StateDirty  bool
	Orientation *Orientation // set when Level is LevelOrient
	Plan        *Plan        // set when Level is LevelPlan
}

// Action represents something the Doll should do.
type Action struct {
	Type    string
	Payload map[string]any
}

// Scheduler manages cognition cycles.
type Scheduler struct {
	provider     inference.Provider
	log          *logger.Logger
	mindAPI      MindAPI
	timeProvider func() time.Time
}

func New(provider inference.Provider, log *logger.Logger, mindAPI MindAPI) *Scheduler {
	return &Scheduler{
		provider:     provider,
		log:          log,
		mindAPI:      mindAPI,
		timeProvider: time.Now,
	}
}

// Enter is the cognition entry boundary.
//
// It runs L0 Reflex to determine whether inference is needed:
//
//   - PathSleep  → LevelReflex result (deterministic, handled at L0).
//   - PathOrient → L1 Orient; if the event matters, L2 Plan produces a
//                  structured plan. Returns LevelPlan when both L1 and L2
//                  complete, LevelOrient when L1 decides the event does not
//                  warrant planning.
//
// Enter may mutate Doll State through L2 Plan when the plan requests
// future cognition, materialising a pending Intention into state.
func (s *Scheduler) Enter(ctx context.Context, eventType events.Type, input string) (*Result, error) {
	path := L0Reflex(eventType)

	switch path {
	case PathSleep:
		s.log.Info("cognition sleep — L0 handled deterministically",
			map[string]any{"event_type": string(eventType)})
		return &Result{Level: LevelReflex}, nil
	case PathOrient:
		s.log.Info("cognition orient — L1 orientation requested",
			map[string]any{"event_type": string(eventType)})
		orient, err := s.Orient(ctx, eventType, input)
		if err != nil {
			return nil, fmt.Errorf("enter orient: %w", err)
		}
		if !orient.Matters {
			return &Result{Level: LevelOrient, Orientation: orient}, nil
		}
		// Event matters — proceed to L2 planning
		s.log.Info("cognition plan — L2 planning requested",
			map[string]any{"event_type": string(eventType), "orientation": orient.Summary})
		plan, dirty, err := s.Plan(ctx, eventType, input, orient)
		if err != nil {
			return nil, fmt.Errorf("enter plan: %w", err)
		}
		result := &Result{Level: LevelPlan, Orientation: orient, Plan: plan, StateDirty: dirty}
		return result, nil
	default:
		// Defensive: unknown mind paths fall back to sleep.
		return &Result{Level: LevelReflex}, nil
	}
}

// EnterWake is the cognition entry boundary for internal wake events.
//
// It accepts a structured IntentionWakePayload and:
//  1. Marks the intention as InProgress in durable state
//  2. Derives inference-facing text from payload fields
//  3. Enters cognition (L0 → L1 → optional L2)
//  4. Transitions intention state based on outcome:
//     - matters=false → re-schedule (Pending + bumped WakeTime)
//     - L2 completed → mark Completed
//     - cognition failure → re-schedule
//  5. Persists final state via MindAPI.Save()
func (s *Scheduler) EnterWake(ctx context.Context, payload events.IntentionWakePayload) (*Result, error) {
	if s.mindAPI == nil {
		return nil, fmt.Errorf("enter wake: mind API not set")
	}

	// 1. Mark intention as InProgress and persist.
	state := s.mindAPI.State()
	found := false
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == payload.IntentionID {
			state.Intentions.Items[i].State = dollstate.IntentionStateInProgress
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("enter wake: intention %q not found in state", payload.IntentionID)
	}
	if err := s.mindAPI.Save(); err != nil {
		return nil, fmt.Errorf("enter wake: persist in-progress state: %w", err)
	}

	// 2. Build inference-facing text from payload fields.
	text := payload.Subject
	if payload.Description != "" {
		text += "\n" + payload.Description
	}
	if payload.IntentionID != "" {
		text += "\nIntentionID: " + payload.IntentionID
	}

	// 3. Enter cognition.
	result, err := s.Enter(ctx, events.TypeInternalWake, text)
	if err != nil {
		s.rescheduleIntention(payload.IntentionID)
		return nil, fmt.Errorf("enter wake: cognition failed: %w", err)
	}

	// 4. Transition intention state based on cognition outcome.
	if result.Level == LevelOrient && result.Orientation != nil && !result.Orientation.Matters {
		s.rescheduleIntention(payload.IntentionID)
	} else if result.Level >= LevelPlan {
		s.completeIntention(payload.IntentionID)
	}

	// 5. Persist final state.
	if err := s.mindAPI.Save(); err != nil {
		return nil, fmt.Errorf("enter wake: persist final state: %w", err)
	}

	return result, nil
}

// rescheduleIntention sets an intention back to Pending and bumps its
// WakeTime by DefaultRetryDuration so the recognition loop will rediscover it.
func (s *Scheduler) rescheduleIntention(id string) {
	state := s.mindAPI.State()
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == id {
			state.Intentions.Items[i].State = dollstate.IntentionStatePending
			state.Intentions.Items[i].WakeTime = s.timeProvider().Add(DefaultRetryDuration).Format(time.RFC3339)
			return
		}
	}
}

// completeIntention marks an intention as Completed — it will not be
// rediscovered as due by any future recognition loop.
func (s *Scheduler) completeIntention(id string) {
	state := s.mindAPI.State()
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == id {
			state.Intentions.Items[i].State = dollstate.IntentionStateCompleted
			return
		}
	}
}

// Run executes a cognition cycle at the given level, always calling the provider.
func (s *Scheduler) Run(ctx context.Context, level Level, input string) (*Result, error) {
	s.log.Info("cognition run starting",
		map[string]any{"level": level.String(), "input_len": len(input)})

	resp, err := s.provider.Infer(ctx, inference.Request{
		Messages: []inference.Message{{Role: "user", Content: input}},
		Purpose:  inference.PurposeRespond,
	})
	if err != nil {
		return nil, err
	}

	result := &Result{
		Level: level,
		Actions: []Action{{
			Type:    "respond",
			Payload: map[string]any{"text": resp.Content},
		}},
	}

	s.log.Info("cognition run complete",
		map[string]any{"level": level.String(), "tokens": resp.TokensUsed})
	return result, nil
}