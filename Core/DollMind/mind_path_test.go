package dollmind

import (
	"context"
	"strings"
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
	reqs       []inference.Request
}

func newSpy(name, orientResp string) *spyProvider {
	return &spyProvider{
		name:       name,
		orientResp: orientResp,
		planResp:   `{"summary":"acknowledge and continue","observations":["nothing notable"]}`,
	}
}

func (s *spyProvider) Infer(ctx context.Context, req inference.Request) (*inference.Response, error) {
	s.calls.Add(1)
	s.reqs = append(s.reqs, req)
	resp := s.orientResp
	if req.Purpose == inference.PurposePlan {
		resp = s.planResp
	}
	return &inference.Response{Content: resp, TokensUsed: 1}, nil
}

func (s *spyProvider) capturedReqs() []inference.Request {
	return s.reqs
}
func (s *spyProvider) ID() inference.ProviderID { return inference.ProviderID("spy") }

// ──────────────────────────────────────────────
// enterMockAPI — minimal MindAPI for Scheduler test harness
// ──────────────────────────────────────────────

type enterMockAPI struct {
	s *dollstate.DollState
}

func (m *enterMockAPI) Inference() inference.Provider { return nil }
func (m *enterMockAPI) State() *dollstate.DollState   { return m.s }
func (m *enterMockAPI) Save() error                    { return nil }

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

func TestL0Reflex_InternalWakeRequiresCognition(t *testing.T) {
	path := L0Reflex(events.TypeInternalWake)
	if path != PathOrient {
		t.Errorf("TypeInternalWake → %v, want PathOrient", path)
	}
}

func TestL0Reflex_InternalWakeDeterministic(t *testing.T) {
	// Internal wake must be as deterministic as any other event
	r1 := L0Reflex(events.TypeInternalWake)
	r2 := L0Reflex(events.TypeInternalWake)
	if r1 != r2 {
		t.Fatal("L0Reflex(TypeInternalWake) is not deterministic")
	}
}

func TestL0Reflex_StructuredResult(t *testing.T) {
	// MindPath is a typed constant — not free-form prose.
	paths := []MindPath{L0Reflex(events.TypeMessage), L0Reflex(events.TypePresence), L0Reflex(events.TypeInternalWake)}
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

// ──────────────────────────────────────────────
// Internal Wake — Phase 2: self-originated intention
// ──────────────────────────────────────────────

func TestScheduler_Enter_InternalWake_OrientReturnsOrientation(t *testing.T) {
	spy := newSpy("spy", `{"summary":"Spark's own intention has become due","matters":false,"reason":"self-check"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
	}}
	sched := New(spy, log, mindAPI)

	result, err := sched.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "wake-001",
		Subject:     "review goal progress",
		Description: "Time to check on active goals",
	})
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	if result.Level != LevelOrient {
		t.Errorf("expected LevelOrient for matters=false wake, got %v", result.Level)
	}
	if result.Orientation == nil {
		t.Fatal("expected Orientation to be set")
	}
	if result.Orientation.Matters {
		t.Error("expected matters=false")
	}
	if result.StateDirty {
		t.Error("expected StateDirty=false")
	}
	if calls := int(spy.calls.Load()); calls != 1 {
		t.Errorf("expected 1 inference call (orient only), got %d", calls)
	}

	// Prove structured fields reached the cognition context
	reqs := spy.capturedReqs()
	if len(reqs) == 0 {
		t.Fatal("no inference requests captured")
	}
	prompt := ""
	for _, m := range reqs[0].Messages {
		prompt += m.Content
	}
	for _, want := range []string{
		"review goal progress",
		"Time to check on active goals",
		"wake-001",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("wake prompt should contain %q", want)
		}
	}
	// Must not say "Event type: message"
	if strings.Contains(prompt, "Event type: message") {
		t.Error("orient prompt must not fabricate a message event type")
	}
}

func TestScheduler_Enter_InternalWake_CascadeToL2(t *testing.T) {
	spy := newSpy("spy", `{"summary":"intention requires planning","matters":true,"reason":"wake intention needs action"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
	}}
	sched := New(spy, log, mindAPI)

	result, err := sched.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "wake-007",
		Subject:     "review goal progress",
		Description: "Time to check on active goals",
	})
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	if result.Level != LevelPlan {
		t.Errorf("expected LevelPlan for matters=true wake → L2 cascade, got %v", result.Level)
	}
	if result.Orientation == nil {
		t.Fatal("expected Orientation to be set")
	}
	if !result.Orientation.Matters {
		t.Error("expected matters=true")
	}
	if result.Orientation.Summary != "intention requires planning" {
		t.Errorf("summary = %q", result.Orientation.Summary)
	}
	if result.Plan == nil {
		t.Fatal("expected Plan to be set (matters=true → L2 cascade)")
	}
	if result.Plan.Summary != "acknowledge and continue" {
		t.Errorf("Plan.Summary = %q", result.Plan.Summary)
	}
	if calls := int(spy.calls.Load()); calls != 2 {
		t.Errorf("expected 2 inference calls (orient + plan), got %d", calls)
	}
}

func TestScheduler_Enter_InternalWake_NoHumanMessageFabricated(t *testing.T) {
	spy := newSpy("spy", `{"summary":"internal wake","matters":false,"reason":"routine"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
		Owner:    dollstate.Owner{Name: "Zero"},
	}}
	sched := New(spy, log, mindAPI)

	_, err := sched.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "wake-ctx",
		Subject:     "review goals",
		Description: "Check active goals state",
	})
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	// Infer was called exactly once (orient only, matters=false)
	if calls := int(spy.calls.Load()); calls != 1 {
		t.Errorf("expected 1 inference call, got %d", calls)
	}
	// The orient prompt must not fabricate a human message
	sysPrompt := ""
	for _, call := range spy.capturedReqs() {
		for _, m := range call.Messages {
			sysPrompt += m.Content
		}
	}
	if sysPrompt == "" {
		t.Fatal("no inference requests captured")
	}
	// Must not say "Event type: message" — should be internal_wake context
	if strings.Contains(sysPrompt, "Event type: message") {
		t.Error("orient prompt must not fabricate a message event type")
	}
	// Must say "within your own mind" for self-origin
	if !strings.Contains(sysPrompt, "within your own mind") {
		t.Error("orient prompt must say 'within your own mind' for self-origin")
	}
	// Prove structured fields reach the context
	for _, want := range []string{"review goals", "Check active goals state", "wake-ctx"} {
		if !strings.Contains(sysPrompt, want) {
			t.Errorf("wake prompt should contain %q", want)
		}
	}
}

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