package dollmind

import (
	"context"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ──────────────────────────────────────────────
// L0Reflex — unit tests
// ──────────────────────────────────────────────

func TestL0Reflex_Deterministic(t *testing.T) {
	// Same input must always produce the same output.
	r1 := L0Reflex(events.TypeMessage)
	r2 := L0Reflex(events.TypeMessage)
	if r1 != r2 {
		t.Fatal("L0Reflex is not deterministic")
	}
}

func TestL0Reflex_MessageRequiresCognition(t *testing.T) {
	path := L0Reflex(events.TypeMessage)
	if path != PathOrient {
		t.Errorf("TypeMessage → %v, want PathOrient", path)
	}
}

func TestL0Reflex_CommandRequiresCognition(t *testing.T) {
	path := L0Reflex(events.TypeCommand)
	if path != PathOrient {
		t.Errorf("TypeCommand → %v, want PathOrient", path)
	}
}

func TestL0Reflex_PresenceHandledWithoutInference(t *testing.T) {
	path := L0Reflex(events.TypePresence)
	if path != PathSleep {
		t.Errorf("TypePresence → %v, want PathSleep", path)
	}
}

func TestL0Reflex_SystemHandledWithoutInference(t *testing.T) {
	path := L0Reflex(events.TypeSystem)
	if path != PathSleep {
		t.Errorf("TypeSystem → %v, want PathSleep", path)
	}
}

func TestL0Reflex_UnknownHandledWithoutInference(t *testing.T) {
	path := L0Reflex(events.TypeUnknown)
	if path != PathSleep {
		t.Errorf("TypeUnknown → %v, want PathSleep", path)
	}
}

func TestL0Reflex_StructuredResult(t *testing.T) {
	// MindPath is a typed constant — not free-form prose.
	paths := []MindPath{L0Reflex(events.TypeMessage), L0Reflex(events.TypePresence)}
	for i, p := range paths {
		switch p {
		case PathSleep, PathOrient:
			// valid — structured result
		default:
			t.Errorf("paths[%d] = unexpected value %v", i, p)
		}
	}
}

func TestMindPath_String(t *testing.T) {
	tests := []struct {
		p    MindPath
		want string
	}{
		{PathSleep, "sleep"},
		{PathOrient, "orient"},
		{MindPath(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("MindPath(%d).String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

// ──────────────────────────────────────────────
// Scheduler.Enter — integration with L0
// ──────────────────────────────────────────────

type enterMockAPI struct {
	s *dollstate.DollState
}

func (m *enterMockAPI) Inference() inference.Provider { return nil }
func (m *enterMockAPI) State() *dollstate.DollState   { return m.s }

func TestScheduler_Enter_SleepNoInference(t *testing.T) {
	// Arrange: a presence event — L0 must sleep without touching the provider.
	provider := inference.NewMockProvider("mock", "SHOULD NOT BE CALLED")
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(provider, log, mindAPI)

	// Act: enter with a sleep event type.
	result, err := sched.Enter(context.Background(), events.TypePresence, "hello")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}

	// Assert: LevelReflex, no actions.
	if result.Level != LevelReflex {
		t.Errorf("expected LevelReflex, got %v", result.Level)
	}
	if len(result.Actions) != 0 {
		t.Errorf("expected 0 actions for sleep, got %d", len(result.Actions))
	}
}

func TestScheduler_Enter_OrientCallsInference(t *testing.T) {
	// Arrange: a message event — L0 says orient, so provider must be called.
	provider := inference.NewMockProvider("mock", "Orient response")
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(provider, log, mindAPI)

	result, err := sched.Enter(context.Background(), events.TypeMessage, "Hello, Spark!")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}

	if result.Level != LevelOrient {
		t.Errorf("expected LevelOrient, got %v", result.Level)
	}
	if len(result.Actions) == 0 {
		t.Fatal("expected at least one action")
	}
	if result.Actions[0].Type != "respond" {
		t.Errorf("expected action type 'respond', got %q", result.Actions[0].Type)
	}
	text, ok := result.Actions[0].Payload["text"].(string)
	if !ok || text != "Orient response" {
		t.Errorf("expected payload text 'Orient response', got %v", result.Actions[0].Payload["text"])
	}
}

func TestScheduler_Enter_CommandOrients(t *testing.T) {
	provider := inference.NewMockProvider("mock", "Command handled")
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(provider, log, mindAPI)

	result, err := sched.Enter(context.Background(), events.TypeCommand, "/status")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}

	if result.Level != LevelOrient {
		t.Errorf("expected LevelOrient for command, got %v", result.Level)
	}
}

// ──────────────────────────────────────────────
// Existing Run still works unchanged
// ──────────────────────────────────────────────

func TestEnterDoesNotBreakExistingRun(t *testing.T) {
	provider := inference.NewMockProvider("mock", "Legacy response")
	log := logger.New(logger.InfoLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(provider, log, mindAPI)

	// Existing Run API must still work.
	result, err := sched.Run(context.Background(), LevelReflex, "test legacy")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Level != LevelReflex {
		t.Errorf("expected LevelReflex, got %v", result.Level)
	}
	if len(result.Actions) == 0 {
		t.Fatal("expected at least one action")
	}
	text, ok := result.Actions[0].Payload["text"].(string)
	if !ok || text != "Legacy response" {
		t.Errorf("expected 'Legacy response', got %v", result.Actions[0].Payload["text"])
	}
}