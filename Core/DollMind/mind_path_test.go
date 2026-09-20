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

// spyProvider records the number of Infer calls and returns different
// JSON depending on the request Purpose so the same spy works for both
// L1 Orient and L2 Plan tests without manual per-test formatting.
type spyProvider struct {
	name       string
	orientResp string
	planResp   string
	calls      atomic.Int64
}

func newSpy(name, orientResp string) *spyProvider {
	return &spyProvider{
		name:       name,
		orientResp: orientResp,
		planResp:   `{"summary":"acknowledge and continue","observations":["nothing notable"]}`,
	}
}

func (s *spyProvider) Infer(_ context.Context, req inference.Request) (*inference.Response, error) {
	s.calls.Add(1)
	resp := s.orientResp
	if req.Purpose == inference.PurposePlan {
		resp = s.planResp
	}
	return &inference.Response{Content: resp, TokensUsed: 1}, nil
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
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
	}}
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
	if result.Orientation != nil {
		t.Error("expected nil Orientation for sleep")
	}
	if result.StateDirty {
		t.Error("expected StateDirty=false for sleep")
	}
	if calls := int(spy.calls.Load()); calls != 0 {
		t.Errorf("expected 0 inference calls for sleep, got %d", calls)
	}
}

func TestScheduler_Enter_OrientReturnsOrientation(t *testing.T) {
	spy := newSpy("spy", `{"summary":"user sent a message","matters":false,"reason":"routine chat"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
	}}
	sched := New(spy, log, mindAPI)

	result, err := sched.Enter(context.Background(), events.TypeMessage, "Hello, Spark!")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}

	if result.Level != LevelOrient {
		t.Errorf("expected LevelOrient, got %v", result.Level)
	}
	if result.Orientation == nil {
		t.Fatal("expected Orientation to be set")
	}
	if result.Orientation.Summary != "user sent a message" {
		t.Errorf("summary = %q", result.Orientation.Summary)
	}
	if result.Orientation.Matters {
		t.Error("expected matters=false")
	}
	if result.Orientation.Reason != "routine chat" {
		t.Errorf("reason = %q", result.Orientation.Reason)
	}
	if len(result.Actions) != 0 {
		t.Errorf("expected 0 actions from orient, got %d", len(result.Actions))
	}
	if result.StateDirty {
		t.Error("expected StateDirty=false")
	}
	if calls := int(spy.calls.Load()); calls != 1 {
		t.Errorf("expected 1 inference call, got %d", calls)
	}
}

func TestScheduler_Enter_CommandReturnsOrientation(t *testing.T) {
	spy := newSpy("spy", `{"summary":"status requested","matters":true,"reason":"needs planning"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
	}}
	sched := New(spy, log, mindAPI)

	result, err := sched.Enter(context.Background(), events.TypeCommand, "/status")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if result.Level != LevelPlan {
		t.Errorf("expected LevelPlan for command (matters=true → cascades to L2), got %v", result.Level)
	}
	if result.Orientation == nil {
		t.Fatal("expected Orientation to be set")
	}
	if !result.Orientation.Matters {
		t.Error("expected matters=true for status command")
	}
	if result.Orientation.Summary != "status requested" {
		t.Errorf("summary = %q", result.Orientation.Summary)
	}
	if result.Orientation.Reason != "needs planning" {
		t.Errorf("reason = %q", result.Orientation.Reason)
	}
	if result.Plan == nil {
		t.Fatal("expected Plan to be set (matters=true → L2 cascade)")
	}
	if result.Plan.Summary != "acknowledge and continue" {
		t.Errorf("Plan.Summary = %q", result.Plan.Summary)
	}
	if result.Plan.ProposedAction != "" {
		t.Errorf("expected empty proposed_action for default spy, got %q", result.Plan.ProposedAction)
	}
	if len(result.Actions) != 0 {
		t.Errorf("expected 0 actions from command orient, got %d", len(result.Actions))
	}
	if result.StateDirty {
		t.Error("expected StateDirty=false")
	}
	if calls := int(spy.calls.Load()); calls != 2 {
		t.Errorf("expected 2 inference calls (orient + plan), got %d", calls)
	}
}

// ──────────────────────────────────────────────
// Inference-never-called proof — both L0 paths
// ──────────────────────────────────────────────

func TestScheduler_Enter_InferenceOnlyForPathOrient(t *testing.T) {
	spy := newSpy("spy", `{"summary":"cognition needed","matters":true,"reason":"user interaction"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
	}}
	sched := New(spy, log, mindAPI)

	// PathSleep — deterministic, no inference
	result, err := sched.Enter(context.Background(), events.TypePresence, "ping")
	if err != nil {
		t.Fatalf("Enter(sleep): %v", err)
	}
	if result.Level != LevelReflex {
		t.Errorf("sleep path → LevelReflex, got %v", result.Level)
	}
	if result.Orientation != nil {
		t.Error("expected nil Orientation for sleep")
	}

	// PathOrient — cognition-required, must call inference. Matters=true
	// cascades through to L2 Plan.
	result, err = sched.Enter(context.Background(), events.TypeMessage, "Hello!")
	if err != nil {
		t.Fatalf("Enter(orient): %v", err)
	}
	if result.Level != LevelPlan {
		t.Errorf("orient path (matters=true) → LevelPlan, got %v", result.Level)
	}
	if result.Orientation == nil {
		t.Fatal("expected Orientation for orient path")
	}
	if !result.Orientation.Matters {
		t.Error("expected matters=true for user message")
	}
	if result.Plan == nil {
		t.Fatal("expected Plan to be set via L2 cascade")
	}

	if calls := int(spy.calls.Load()); calls != 2 {
		t.Errorf("expected exactly 2 inference calls (orient + plan), got %d", calls)
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