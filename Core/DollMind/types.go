package dollmind

import (
	"context"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Logger"
	"github.com/Neon-Dolls/neondoll/DollState"
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
	Level      Level
	Actions    []Action
	StateDirty bool
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

// Run executes a cognition cycle at the given level, always calling the provider.
func (s *Scheduler) Run(ctx context.Context, level Level, input string) (*Result, error) {
	s.log.Info("cognition run starting",
		map[string]any{"level": level.String(), "input_len": len(input)})

	resp, err := s.provider.Infer(ctx, inference.Request{
		Model:    "default",
		Messages: []inference.Message{{Role: "user", Content: input}},
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