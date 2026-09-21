package dollmind

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"

	_ "modernc.org/sqlite"
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
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          "wake-001",
				Subject:     "review goal progress",
				Description: "Time to check on active goals",
				WakeTime:    "2006-01-02T15:04:05Z",
				State:       dollstate.IntentionStatePending,
			}},
		},
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

	// M9 P3: matters=false is fulfillment — Spark woke and reconsidered.
	// Intention must be Completed, WakeTime unchanged (no automatic rescheduling).
	state := mindAPI.State()
	var intention *dollstate.IntentionItem
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == "wake-001" {
			intention = &state.Intentions.Items[i]
			break
		}
	}
	if intention == nil {
		t.Fatal("intention wake-001 must exist in state")
	}
	if intention.State != dollstate.IntentionStateCompleted {
		t.Errorf("matters=false wake is fulfillment → expected Completed, got %s", intention.State)
	}
	if intention.WakeTime != "2006-01-02T15:04:05Z" {
		t.Errorf("WakeTime must be unchanged when completed, got %q", intention.WakeTime)
	}
}

func TestScheduler_Enter_InternalWake_CascadeToL2(t *testing.T) {
	spy := newSpy("spy", `{"summary":"intention requires planning","matters":true,"reason":"wake intention needs action"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          "wake-007",
				Subject:     "review goal progress",
				Description: "Time to check on active goals",
				WakeTime:    "2006-01-02T15:04:05Z",
				State:       dollstate.IntentionStatePending,
			}},
		},
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

	// Phase 3: matters=true + L2 completed → intention must be Completed
	state := mindAPI.State()
	var intention *dollstate.IntentionItem
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == "wake-007" {
			intention = &state.Intentions.Items[i]
			break
		}
	}
	if intention == nil {
		t.Fatal("intention wake-007 must exist in state")
	}
	if intention.State != dollstate.IntentionStateCompleted {
		t.Errorf("expected Completed after plan materialisation, got %s", intention.State)
	}
}

func TestScheduler_Enter_InternalWake_NoHumanMessageFabricated(t *testing.T) {
	spy := newSpy("spy", `{"summary":"internal wake","matters":false,"reason":"routine"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
		Owner:    dollstate.Owner{Name: "Zero"},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          "wake-ctx",
				Subject:     "review goals",
				Description: "Check active goals state",
				WakeTime:    "2006-01-02T15:04:05Z",
				State:       dollstate.IntentionStatePending,
			}},
		},
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

	// M9 P3: matters=false is fulfillment — Spark woke and reconsidered.
	// Intention must be Completed, WakeTime unchanged (no automatic rescheduling).
	state := mindAPI.State()
	var intention *dollstate.IntentionItem
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == "wake-ctx" {
			intention = &state.Intentions.Items[i]
			break
		}
	}
	if intention == nil {
		t.Fatal("intention wake-ctx must exist in state")
	}
	if intention.State != dollstate.IntentionStateCompleted {
		t.Errorf("matters=false wake is fulfillment → expected Completed, got %s", intention.State)
	}
	if intention.WakeTime != "2006-01-02T15:04:05Z" {
		t.Errorf("WakeTime must be unchanged when completed, got %q", intention.WakeTime)
	}
}

// ──────────────────────────────────────────────
// persistBackedAPI — wraps a real persistence.Store as a MindAPI
// ──────────────────────────────────────────────

// persistBackedAPI provides MindAPI backed by a real SQLite persistence.Store.
// Save persists to the store; State returns the in-memory state pointer.
// No state pointer or DollState crosses a close/reopen boundary.
type persistBackedAPI struct {
	state *dollstate.DollState
	store persistence.Store
}

func (m *persistBackedAPI) Inference() inference.Provider { return nil }
func (m *persistBackedAPI) State() *dollstate.DollState   { return m.state }
func (m *persistBackedAPI) Save() error {
	return m.store.SaveDoll(context.Background(), m.state)
}

// tempDB creates a temporary SQLite database path and returns a cleanup func.
func tempDB(t *testing.T) (string, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "neondoll-test-*.db")
	if err != nil {
		t.Fatalf("tempDB: %v", err)
	}
	path := f.Name()
	f.Close()
	return path, func() { os.Remove(path) }
}

// ──────────────────────────────────────────────
// M9 P3 acceptance tests
// ──────────────────────────────────────────────

// TestEnterWake_CognitionFailed_RemainsPending proves that when cognition
// returns an error, the Intention stays Pending with its original WakeTime.
// Core does not invent a new semantic wake time.
func TestEnterWake_CognitionFailed_RemainsPending(t *testing.T) {
	errProvider := &errSpy{errMsg: "inference unavailable"}
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          "fail-001",
				Subject:     "check system health",
				Description: "Periodic health check",
				WakeTime:    "2006-01-02T15:04:05Z",
				State:       dollstate.IntentionStatePending,
			}},
		},
	}}
	sched := New(errProvider, log, mindAPI)

	_, err := sched.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "fail-001",
		Subject:     "check system health",
		Description: "Periodic health check",
	})
	if err == nil {
		t.Fatal("expected error from failed cognition")
	}

	// Intention must still be Pending with unchanged WakeTime
	state := mindAPI.State()
	var intention *dollstate.IntentionItem
	for i := range state.Intentions.Items {
		if state.Intentions.Items[i].ID == "fail-001" {
			intention = &state.Intentions.Items[i]
			break
		}
	}
	if intention == nil {
		t.Fatal("intention fail-001 must exist in state")
	}
	if intention.State != dollstate.IntentionStatePending {
		t.Errorf("failed cognition → expected Pending, got %s", intention.State)
	}
	if intention.WakeTime != "2006-01-02T15:04:05Z" {
		t.Errorf("WakeTime must be unchanged after failed cognition, got %q", intention.WakeTime)
	}
}

// errSpy returns an error on every Infer call.
type errSpy struct {
	errMsg string
}

func (e *errSpy) Infer(_ context.Context, _ inference.Request) (*inference.Response, error) {
	return nil, fmt.Errorf("%s", e.errMsg)
}
func (e *errSpy) ID() inference.ProviderID { return inference.ProviderID("errSpy") }
func (e *errSpy) capturedReqs() []inference.Request { return nil }

// TestEnterWake_SaveFailureSurfaced proves that a persistence failure after
// successful cognition is surfaced as an error, not silently swallowed.
func TestEnterWake_SaveFailureSurfaced(t *testing.T) {
	spy := newSpy("spy", `{"summary":"health ok","matters":false,"reason":"all nominal"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &saveFailAPI{
		s: &dollstate.DollState{
			Identity: dollstate.Identity{CanonicalName: "TestDoll"},
			Intentions: dollstate.Intentions{
				Items: []dollstate.IntentionItem{{
					ID:          "save-fail-001",
					Subject:     "health check",
					Description: "routine",
					WakeTime:    "2006-01-02T15:04:05Z",
					State:       dollstate.IntentionStatePending,
				}},
			},
		},
		saveErr: fmt.Errorf("disk full"),
	}
	sched := New(spy, log, mindAPI)

	_, err := sched.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "save-fail-001",
		Subject:     "health check",
		Description: "routine",
	})
	if err == nil {
		t.Fatal("expected error from failed persistence")
	}
	if !strings.Contains(err.Error(), "persist completed state") {
		t.Errorf("error must mention persistence failure, got: %v", err)
	}
}

// saveFailAPI records save calls and returns a configurable error.
type saveFailAPI struct {
	s        *dollstate.DollState
	saveCalled int
	saveErr  error
}

func (m *saveFailAPI) Inference() inference.Provider { return nil }
func (m *saveFailAPI) State() *dollstate.DollState   { return m.s }
func (m *saveFailAPI) Save() error {
	m.saveCalled++
	return m.saveErr
}

// TestEnterWake_CloseReopenPreservesCompleted proves that a Completed
// Intention survives a real SQLite close/reopen cycle. The completed
// intention must be durably persisted, survive a Store Close + fresh
// NewStore/LoadDoll, retain its semantic fields and WakeTime, and no
// longer be returned by DueIntentions(). No state pointer or DollState
// copy crosses the restart boundary.
func TestEnterWake_CloseReopenPreservesCompleted(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	store1, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Initial state with one Pending intention.
	initState := &dollstate.DollState{
		Identity: dollstate.Identity{
			CanonicalName: "Spark",
			DollID:        "spark-close-reopen",
		},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          "cr-001",
				Subject:     "periodic review",
				Description: "Review active state",
				WakeTime:    "2006-01-02T15:04:05Z",
				State:       dollstate.IntentionStatePending,
			}},
		},
	}
	// Persist initial state.
	if err := store1.SaveDoll(context.Background(), initState); err != nil {
		t.Fatalf("SaveDoll (initial): %v", err)
	}

	// MindAPI backed by real persistence.
	spy := newSpy("spy", `{"summary":"check ok","matters":false,"reason":"routine"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI1 := &persistBackedAPI{state: initState, store: store1}
	sched1 := New(spy, log, mindAPI1)

	// Enter wake — matters=false → complete it.
	result, err := sched1.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "cr-001",
		Subject:     "periodic review",
		Description: "Review active state",
	})
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}
	if result.Level != LevelOrient {
		t.Errorf("expected LevelOrient, got %v", result.Level)
	}

	// Verify in-memory state before close.
	if initState.Intentions.Items[0].State != dollstate.IntentionStateCompleted {
		t.Errorf("before close: expected Completed, got %s", initState.Intentions.Items[0].State)
	}

	// Close store1 — the persistence boundary. No state is retained.
	if err := store1.Close(); err != nil {
		t.Fatalf("Close store1: %v", err)
	}

	// ─── Genuine close/reopen ───

	store2, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	defer store2.Close()

	// LoadDoll — the only way to get state back; no pointer/copy from before.
	reloaded, err := store2.LoadDoll(context.Background(), "spark-close-reopen")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}

	// Prove the target Intention is still Completed after restart.
	var intention *dollstate.IntentionItem
	for i := range reloaded.Intentions.Items {
		if reloaded.Intentions.Items[i].ID == "cr-001" {
			intention = &reloaded.Intentions.Items[i]
			break
		}
	}
	if intention == nil {
		t.Fatal("intention cr-001 must exist after close/reopen")
	}
	if intention.State != dollstate.IntentionStateCompleted {
		t.Errorf("close/reopen: expected Completed, got %s", intention.State)
	}
	if intention.WakeTime != "2006-01-02T15:04:05Z" {
		t.Errorf("close/reopen: WakeTime must be preserved, got %q", intention.WakeTime)
	}
	if intention.Subject != "periodic review" {
		t.Errorf("close/reopen: Subject must be preserved, got %q", intention.Subject)
	}
	if intention.Description != "Review active state" {
		t.Errorf("close/reopen: Description must be preserved, got %q", intention.Description)
	}

	// Verify the completed intention is not returned by DueIntentions().
	mindAPI2 := &persistBackedAPI{state: reloaded, store: store2}
	reopenSched := New(spy, log, mindAPI2)
	due, err := reopenSched.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions: %v", err)
	}
	for _, d := range due {
		if d.ID == "cr-001" {
			t.Error("close/reopen: Completed intention must not be due")
		}
	}
}

// TestEnterWake_UnrelatedIntentionsUntouched proves that waking one Intention
// does not modify the State of unrelated (non-target) Intentions, regardless
// of the cognition outcome.
func TestEnterWake_UnrelatedIntentionsUntouched(t *testing.T) {
	spy := newSpy("spy", `{"summary":"check","matters":false,"reason":"done"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{
				{
					ID:          "target-001",
					Subject:     "target review",
					Description: "target",
					WakeTime:    "2006-01-02T15:04:05Z",
					State:       dollstate.IntentionStatePending,
				},
				{
					ID:          "other-001",
					Subject:     "something else",
					Description: "unrelated",
					WakeTime:    "2006-01-02T20:00:00Z",
					State:       dollstate.IntentionStatePending,
				},
				{
					ID:          "other-002",
					Subject:     "completed item",
					Description: "already done",
					WakeTime:    "2006-01-01T00:00:00Z",
					State:       dollstate.IntentionStateCompleted,
				},
			},
		},
	}}
	sched := New(spy, log, mindAPI)

	_, err := sched.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: "target-001",
		Subject:     "target review",
		Description: "target",
	})
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	state := mindAPI.State()

	// target-001 must be Completed (fulfilled).
	targetFound := false
	for i := range state.Intentions.Items {
		item := state.Intentions.Items[i]
		switch item.ID {
		case "target-001":
			targetFound = true
			if item.State != dollstate.IntentionStateCompleted {
				t.Errorf("target: expected Completed, got %s", item.State)
			}
		case "other-001":
			if item.State != dollstate.IntentionStatePending {
				t.Errorf("other-001: untouched intention changed from Pending to %s", item.State)
			}
			if item.WakeTime != "2006-01-02T20:00:00Z" {
				t.Errorf("other-001: WakeTime changed to %q", item.WakeTime)
			}
		case "other-002":
			if item.State != dollstate.IntentionStateCompleted {
				t.Errorf("other-002: untouched intention changed from Completed to %s", item.State)
			}
		}
	}
	if !targetFound {
		t.Fatal("target-001 not found in state")
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

// ──────────────────────────────────────────────
// Phase 4 — Restart Reconstructs Future Wake
// ──────────────────────────────────────────────

// TestPhase4_RestartReconstructsFutureWake proves that after a genuine
// persistence restart, Core reconstructs the pending obligation from the
// loaded Doll State, discovers it when the deterministic reference time
// advances, dispatches internal wake cognition, persists the consumed
// state, and does not treat the fulfilled Intention as pending work on a
// subsequent restart.
//
// Acceptance path:
//   pending future-wake → persist → Store.Close → new Store → LoadDoll →
//   reconstructed Scheduler knows future Intention → advance refTime →
//   DueIntentions → EnterWake → cognition marks Completed → persist →
//   Store.Close → new Store → LoadDoll → DueIntentions returns nothing
//
// Two genuine close/reopen cycles prove that:
//   - Intention identity and semantic fields survive the first restart
//   - Lifecycle state (Completed) survives the second restart
//   - No retained state pointer crosses either boundary
//   - Inference is initiated by internal_wake, not a fabricated message
func TestPhase4_RestartReconstructsFutureWake(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	refTime := time.Date(2035, 6, 15, 12, 0, 0, 0, time.UTC)
	futureWake := "2035-06-15T14:00:00Z" // 2 hours after refTime

	// ─── Session 1: seed Spark with a future-wake Intention ───

	store1, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	initState := &dollstate.DollState{
		Identity: dollstate.Identity{
			CanonicalName: "Spark",
			DollID:        "spark-phase4",
		},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          "p4-wake-001",
				Subject:     "rethink purpose",
				Description: "A future wake to reconsider direction",
				WakeTime:    futureWake,
				State:       dollstate.IntentionStatePending,
			}},
		},
	}
	if err := store1.SaveDoll(context.Background(), initState); err != nil {
		t.Fatalf("SaveDoll (initial): %v", err)
	}

	// Verify: at refTime the Intention is NOT due (wake is in the future)
	var currentTime = refTime
	spy1 := newSpy("spy",
		`{"summary":"rethink","matters":false,"reason":"all good"}`)
	log := logger.New(logger.DebugLevel, nil)
	mindAPI1 := &persistBackedAPI{state: initState, store: store1}
	sched1 := &Scheduler{
		provider:     spy1,
		log:          log,
		mindAPI:      mindAPI1,
		timeProvider: func() time.Time { return currentTime },
	}

	due, err := sched1.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions (session 1): %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected no due intentions (wake in future), got %d", len(due))
	}

	// Persist and close — genuine persistence boundary. No state retained.
	if err := store1.Close(); err != nil {
		t.Fatalf("Close store1: %v", err)
	}

	// ─── Session 2: reopen, reconstruct obligation, advance time, wake ───

	store2, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}

	// LoadDoll — the only way to get state back; no pointer/copy from before.
	reloaded, err := store2.LoadDoll(context.Background(), "spark-phase4")
	if err != nil {
		t.Fatalf("LoadDoll (session 2): %v", err)
	}

	// Prove Intention identity and semantic fields survived the first restart
	if len(reloaded.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention after reload, got %d",
			len(reloaded.Intentions.Items))
	}
	it := reloaded.Intentions.Items[0]
	if it.ID != "p4-wake-001" {
		t.Errorf("Intention ID: expected %q, got %q", "p4-wake-001", it.ID)
	}
	if it.Subject != "rethink purpose" {
		t.Errorf("Intention Subject: expected %q, got %q", "rethink purpose", it.Subject)
	}
	if it.Description != "A future wake to reconsider direction" {
		t.Errorf("Intention Description: expected %q, got %q",
			"A future wake to reconsider direction", it.Description)
	}
	if it.WakeTime != futureWake {
		t.Errorf("Intention WakeTime: expected %q, got %q", futureWake, it.WakeTime)
	}
	if it.State != dollstate.IntentionStatePending {
		t.Errorf("Intention State: expected Pending, got %s", it.State)
	}

	// Reconstruct the runtime obligation: new Scheduler with loaded state.
	spy2 := newSpy("spy",
		`{"summary":"rethink","matters":false,"reason":"all good"}`)
	mindAPI2 := &persistBackedAPI{state: reloaded, store: store2}

	sched2 := &Scheduler{
		provider:     spy2,
		log:          log,
		mindAPI:      mindAPI2,
		timeProvider: func() time.Time { return currentTime },
	}

	// Step A: reference time still at refTime (12:00) — intention is NOT due
	due, err = sched2.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions (session 2, before time advance): %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected 0 due (wake at %s, ref at 12:00), got %d",
			futureWake, len(due))
	}

	// Step B: advance reference time past the wake time → Intention becomes due
	currentTime = time.Date(2035, 6, 15, 15, 0, 0, 0, time.UTC)

	due, err = sched2.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions (session 2, after time advance): %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due intention after time advance, got %d", len(due))
	}
	if due[0].ID != "p4-wake-001" {
		t.Errorf("due intention ID: expected %q, got %q", "p4-wake-001", due[0].ID)
	}

	// Step C: wake Spark for this due Intention
	result, err := sched2.EnterWake(context.Background(), events.IntentionWakePayload{
		IntentionID: due[0].ID,
		Subject:     due[0].Subject,
		Description: due[0].Description,
	})
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}
	if result.Level != LevelOrient {
		t.Errorf("expected LevelOrient, got %v", result.Level)
	}

	// Prove inference was initiated by internal Intention wake, not a human message
	sysPrompt := ""
	for _, call := range spy2.capturedReqs() {
		for _, m := range call.Messages {
			sysPrompt += m.Content
		}
	}
	if sysPrompt == "" {
		t.Fatal("no inference requests captured — wake did not enter cognition")
	}
	if strings.Contains(sysPrompt, "Event type: message") {
		t.Error("orient prompt must not fabricate a message event type")
	}
	if !strings.Contains(sysPrompt, "within your own mind") {
		t.Error("orient prompt must say 'within your own mind' for self-origin")
	}
	// Prove structured Intention fields reach the cognition context
	for _, want := range []string{"p4-wake-001", "rethink purpose", "A future wake to reconsider direction"} {
		if !strings.Contains(sysPrompt, want) {
			t.Errorf("wake prompt should contain Intention field %q", want)
		}
	}

	// Verify Intention is Completed in memory
	var intention *dollstate.IntentionItem
	for i := range reloaded.Intentions.Items {
		if reloaded.Intentions.Items[i].ID == "p4-wake-001" {
			intention = &reloaded.Intentions.Items[i]
			break
		}
	}
	if intention == nil {
		t.Fatal("intention p4-wake-001 must exist in state after wake")
	}
	if intention.State != dollstate.IntentionStateCompleted {
		t.Errorf("after wake: expected Completed, got %s", intention.State)
	}
	if intention.WakeTime != futureWake {
		t.Errorf("WakeTime must be preserved after completion, got %q", intention.WakeTime)
	}

	// Persist consumed state and close
	if err := store2.Close(); err != nil {
		t.Fatalf("Close store2: %v", err)
	}

	// ─── Session 3: second reopen — fulfilled Intention is not pending work ───

	store3, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore (third session): %v", err)
	}
	defer store3.Close()

	reloaded2, err := store3.LoadDoll(context.Background(), "spark-phase4")
	if err != nil {
		t.Fatalf("LoadDoll (session 3): %v", err)
	}

	mindAPI3 := &persistBackedAPI{state: reloaded2, store: store3}
	sched3 := &Scheduler{
		provider:     spy2,
		log:          log,
		mindAPI:      mindAPI3,
		timeProvider: func() time.Time { return currentTime },
	}

	// The fulfilled Intention must be Completed and not returned by DueIntentions
	var after *dollstate.IntentionItem
	for i := range reloaded2.Intentions.Items {
		if reloaded2.Intentions.Items[i].ID == "p4-wake-001" {
			after = &reloaded2.Intentions.Items[i]
			break
		}
	}
	if after == nil {
		t.Fatal("intention p4-wake-001 must still exist after second restart")
	}
	if after.State != dollstate.IntentionStateCompleted {
		t.Errorf("after second restart: expected Completed, got %s", after.State)
	}
	if after.WakeTime != futureWake {
		t.Errorf("WakeTime preserved across second restart: expected %q, got %q",
			futureWake, after.WakeTime)
	}

	due, err = sched3.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions (session 3): %v", err)
	}
	for _, d := range due {
		if d.ID == "p4-wake-001" {
			t.Error("fulfilled Intention must not be returned by DueIntentions")
		}
	}
}

// ──────────────────────────────────────────────
// M10 Phase 2 — Enter dispatch error boundary
// ──────────────────────────────────────────────

// dispatcherSpy implements Dispatcher with a configurable error.
type dispatcherSpy struct {
	err error
}

func (d *dispatcherSpy) Dispatch(_ context.Context, _ OutboundAction) error {
	return d.err
}

// TestEnter_OutboundActionDispatchFailure proves that when L2 Plan produces
// an OutboundAction and the Dispatcher returns an error, Enter() returns a
// wrapped non-nil error through the normal error boundary — not a nil-error
// result with a buried field.
func TestEnter_OutboundActionDispatchFailure(t *testing.T) {
	spy := newSpy("spy",
		`{"summary":"send message","matters":true,"reason":"needs to act"}`,
	)
	// Override plan response to include an outbound_action
	spy.planResp = `{"summary":"send","observations":[],"outbound_action":{"kind":"send_text","content":"Hello"}}`

	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{Identity: dollstate.Identity{CanonicalName: "TestDoll"}}}
	disp := &dispatcherSpy{err: fmt.Errorf("connection lost")}

	sched := New(spy, log, mindAPI, WithDispatcher(disp))

	_, err := sched.Enter(context.Background(), events.TypeMessage, "Hello")
	if err == nil {
		t.Fatal("expected non-nil error when dispatch fails, got nil")
	}
	if !strings.Contains(err.Error(), "enter dispatch") {
		t.Errorf("error must wrap 'enter dispatch', got: %v", err)
	}
	if !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("error must contain original dispatch error, got: %v", err)
	}
}

// TestEnter_OutboundActionDispatchSuccess proves that when dispatch succeeds,
// Enter() returns a successful result (LevelPlan) with no error.
func TestEnter_OutboundActionDispatchSuccess(t *testing.T) {
	spy := newSpy("spy",
		`{"summary":"send message","matters":true,"reason":"needs to act"}`,
	)
	spy.planResp = `{"summary":"send","observations":[],"outbound_action":{"kind":"send_text","content":"Hello"}}`

	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{Identity: dollstate.Identity{CanonicalName: "TestDoll"}}}
	disp := &dispatcherSpy{err: nil} // success

	sched := New(spy, log, mindAPI, WithDispatcher(disp))

	result, err := sched.Enter(context.Background(), events.TypeMessage, "Hello")
	if err != nil {
		t.Fatalf("expected nil error on successful dispatch, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Level != LevelPlan {
		t.Errorf("expected LevelPlan when L2 succeeds, got %v", result.Level)
	}
}

// TestEnter_OutboundAction_NoDispatcher proves that Enter succeeds normally
// when Plan has an OutboundAction but no Dispatcher is configured — the action
// is silently dropped (no transport is set up so there's nothing to dispatch).
func TestEnter_OutboundAction_NoDispatcher(t *testing.T) {
	spy := newSpy("spy",
		`{"summary":"send message","matters":true,"reason":"needs to act"}`,
	)
	spy.planResp = `{"summary":"send","observations":[],"outbound_action":{"kind":"send_text","content":"Hello"}}`

	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &enterMockAPI{s: &dollstate.DollState{Identity: dollstate.Identity{CanonicalName: "TestDoll"}}}

	// No WithDispatcher — dispatcher is nil
	sched := New(spy, log, mindAPI)

	result, err := sched.Enter(context.Background(), events.TypeMessage, "Hello")
	if err != nil {
		t.Fatalf("expected nil error when no dispatcher, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Level != LevelPlan {
		t.Errorf("expected LevelPlan, got %v", result.Level)
	}
}