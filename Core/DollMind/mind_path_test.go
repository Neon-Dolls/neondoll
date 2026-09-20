package dollmind

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ──────────────────────────────────────────────
// spyProvider — call-counting inference spy
// ──────────────────────────────────────────────

// spyProvider records the number of Infer calls without making assertions.
// Tests use spy.calls to prove the provider was (or was not) consulted.
type spyProvider struct {
	name     string
	response string
	calls    atomic.Int64
}

func newSpy(name, response string) *spyProvider {
	return &spyProvider{name: name, response: response}
}

func (s *spyProvider) Infer(_ context.Context, _ inference.Request) (*inference.Response, error) {
	s.calls.Add(1)
	return &inference.Response{Content: s.response, TokensUsed: 1}, nil
}

func (s *spyProvider) Name() string             { return s.name }
func (s *spyProvider) ID() inference.ProviderID { return inference.ProviderID("spy") }

// ──────────────────────────────────────────────
// enterMockAPI — minimal MindAPI for Scheduler test harness
// ──────────────────────────────────────────────

type enterMockAPI struct {
	s *dollstate.DollState
}

func (m *enterMockAPI) Inference() inference.Provider { return nil }
func (m *enterMockAPI) State() *dollstate.DollState   { return m.s }

// ──────────────────────────────────────────────
// L0Reflex — unit tests
// ──────────────────────────────────────────────

func TestL0Reflex_Deterministic(t *testing.T) {
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

func TestScheduler_Enter_SleepNoActions(t *testing.T) {
	spy := newSpy("spy", "SHOULD NOT MATTER")
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(spy, log, mindAPI)

	result, err := sched.Enter(context.Background(), events.TypePresence, "hello")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if result.Level != LevelReflex {
		t.Errorf("expected LevelReflex, got %v", result.Level)
	}
	if len(result.Actions) != 0 {
		t.Errorf("expected 0 actions for sleep, got %d", len(result.Actions))
	}
}

func TestScheduler_Enter_OrientSignalsLevelWithoutInference(t *testing.T) {
	spy := newSpy("spy", "SHOULD NOT MATTER")
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(spy, log, mindAPI)

	result, err := sched.Enter(context.Background(), events.TypeMessage, "Hello, Spark!")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}

	// Must signal that L1 orientation is needed — NOT produce a response.
	if result.Level != LevelOrient {
		t.Errorf("expected LevelOrient, got %v", result.Level)
	}
	if len(result.Actions) != 0 {
		t.Errorf("expected 0 actions (Phase 1 signals path, does not execute), got %d", len(result.Actions))
	}
}

func TestScheduler_Enter_CommandSignalsLevelWithoutInference(t *testing.T) {
	spy := newSpy("spy", "SHOULD NOT MATTER")
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(spy, log, mindAPI)

	result, err := sched.Enter(context.Background(), events.TypeCommand, "/status")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if result.Level != LevelOrient {
		t.Errorf("expected LevelOrient for command, got %v", result.Level)
	}
	if len(result.Actions) != 0 {
		t.Errorf("expected 0 actions for command orient, got %d", len(result.Actions))
	}
}

// ──────────────────────────────────────────────
// Inference-never-called proof — both L0 paths
// ──────────────────────────────────────────────

func TestScheduler_Enter_InferenceNeverCalled(t *testing.T) {
	// A single spy shared across both paths proves Enter never touches
	// the inference provider regardless of the L0 decision.
	spy := newSpy("spy", "SHOULD NEVER APPEAR")
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(spy, log, mindAPI)

	// PathSleep — deterministic event
	result, err := sched.Enter(context.Background(), events.TypePresence, "ping")
	if err != nil {
		t.Fatalf("Enter(sleep): %v", err)
	}
	if result.Level != LevelReflex {
		t.Errorf("sleep path → LevelReflex, got %v", result.Level)
	}

	// PathOrient — cognition-required event
	result, err = sched.Enter(context.Background(), events.TypeMessage, "Hello!")
	if err != nil {
		t.Fatalf("Enter(orient): %v", err)
	}
	if result.Level != LevelOrient {
		t.Errorf("orient path → LevelOrient, got %v", result.Level)
	}

	if calls := spy.calls.Load(); calls != 0 {
		t.Errorf("Infer called %d times via Enter; expected 0 — Phase 1 must not invoke inference", calls)
	}
}

// ──────────────────────────────────────────────
// Legacy Run backward compat
// ──────────────────────────────────────────────

func TestEnterDoesNotBreakExistingRun(t *testing.T) {
	provider := inference.NewMockProvider("mock", "Legacy response")
	log := logger.New(logger.InfoLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{}}
	sched := New(provider, log, mindAPI)

	// Existing Run API must still work — bypasses Enter entirely.
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