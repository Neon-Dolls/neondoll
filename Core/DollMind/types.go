package dollmind

import (
	"context"
	"fmt"

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
}

// Result from a cognition cycle.
type Result struct {
	Level       Level
	Actions     []Action
	StateDirty  bool
	Orientation *Orientation // set when Level is LevelOrient
	Plan        *Plan        // set when Level is LevelPlan

	// CreatedIntention is set when L2 Plan materialised a pending Intention
	// into Doll State during the cognition cycle.
	CreatedIntention *dollstate.IntentionItem
}

// Action represents something the Doll should do.
type Action struct {
	Type    string
	Payload map[string]any
}

// Scheduler manages cognition cycles.
type Scheduler struct {
	provider inference.Provider
	log      *logger.Logger
	mindAPI  MindAPI
}

func New(provider inference.Provider, log *logger.Logger, mindAPI MindAPI) *Scheduler {
	return &Scheduler{provider: provider, log: log, mindAPI: mindAPI}
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
		if dirty && len(s.mindAPI.State().Intentions.Items) > 0 {
			items := s.mindAPI.State().Intentions.Items
			result.CreatedIntention = &items[len(items)-1]
		}
		return result, nil
	default:
		// Defensive: unknown mind paths fall back to sleep.
		return &Result{Level: LevelReflex}, nil
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