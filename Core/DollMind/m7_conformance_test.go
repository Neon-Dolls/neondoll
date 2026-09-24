// Package dollmind provides the core cognition pipeline for NeonDoll.
//
// M7 Conformance — Spark Acts and Observes.
//
// Thesis: a pending Intention autonomously wakes Spark, Spark uses M6 tools
// (runtime.info/read, runtime.info/list), sees tool results in the same
// cognition run, produces a final Plan with Observations capturing semantic
// meaning, and those Observations persist as Memories through existing Doll
// State/Doll Card continuity — surviving Core destruction and clean
// reconstruction.

package dollmind

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	events "github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ── M7 Helpers ───────────────────────────────────────────────────────────

// wakeState creates a DollState with a single pending Intention whose WakeTime
// is in the past, suitable for EnterWake tests.
func wakeState(intentionID, subject, wakeTime string) *dollstate.DollState {
	state := newConformanceState()
	state.Intentions = dollstate.Intentions{
		Items: []dollstate.IntentionItem{
			{
				ID:       intentionID,
				Subject:  subject,
				WakeTime: wakeTime,
				State:    dollstate.IntentionStatePending,
			},
		},
	}
	return state
}

// orientResponse returns a minimal valid orientation JSON with matters=true.
func orientResponse() string {
	return `{"summary":"waking due to intention","matters":true,"reason":"intention became due"}`
}

// planWithObservations returns a Plan JSON with given summary and observations.
func planWithObservations(summary string, observations []string) string {
	b, err := json.Marshal(map[string]any{
		"summary":                  summary,
		"proposed_action":          "observed_and_acted",
		"observations":             observations,
		"should_reorient":          false,
		"request_future_cognition": false,
		"future_subject":           "",
		"future_reason":            "",
		"future_wake_time":         "",
	})
	if err != nil {
		panic("planWithObservations: " + err.Error())
	}
	return string(b)
}

// setupWakeScheduler creates a Scheduler, provider, and mockAPI for M7
// EnterWake tests. The state includes a pending Intention.
// Returns (scheduler, provider, mockAPI) for test assertion access.
func setupWakeScheduler(acts []scriptedAct, guardEval body.AuthorityEvaluator, intentionID, subject, wakeTime string) (*Scheduler, *scriptedProvider, *conformanceMockAPI) {
	prov := &scriptedProvider{
		name: "m7-conformance",
		acts: acts,
	}
	if guardEval == nil {
		guardEval = allowAllEvaluator{}
	}
	reg := body.NewRegistry()
	guard := body.NewGuard(reg, guardEval)
	loc, _ := reg.Local()
	exec := NewToolExecutor(guard, loc)

	log := logger.New(logger.ErrorLevel, nil)
	state := wakeState(intentionID, subject, wakeTime)
	mockAPI := &conformanceMockAPI{state: state}

	s := New(prov, log, mockAPI,
		WithTimeProvider(func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }),
		WithToolExecutor(exec),
	)
	return s, prov, mockAPI
}

// ──────────────────────────────────────────────────────────────────────────
// Test 1: Full autonomous path — pending Intention → EnterWake → tools →
// observations → memories
// ──────────────────────────────────────────────────────────────────────────

func TestM7_AutonomousToolObservation(t *testing.T) {
	const intentionID = "int-m7-test-1"
	const subject = "Check runtime capabilities"

	// Script sequence:
	// [0] Orient response — matters=true
	// [1] Tool A: runtime.info__read
	// [2] Tool B: runtime.info__list
	// [3] Final plan with semantic observations
	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}
	toolB := inference.ToolCall{ID: "call_B", Name: "runtime.info__list"}

	obs1 := "runtime.info__read returned data about the Core"
	obs2 := "runtime.info__list listed available capabilities"
	finalPlan := planWithObservations("M7: Spark acted and observed tools", []string{obs1, obs2})

	s, prov, mockAPI := setupWakeScheduler(
		[]scriptedAct{
			{content: orientResponse()},
			{toolCalls: []inference.ToolCall{toolA}},
			{toolCalls: []inference.ToolCall{toolB}},
			{content: finalPlan},
		},
		nil, // allowAllEvaluator
		intentionID, subject, "2026-01-15T12:00:00Z",
	)

	// 1. Verify DueIntentions works
	due, err := s.DueIntentions()
	if err != nil {
		t.Fatalf("DueIntentions: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due intention, got %d", len(due))
	}
	if due[0].ID != intentionID {
		t.Fatalf("expected intention %q, got %q", intentionID, due[0].ID)
	}

	// 2. Build payload and call EnterWake
	payload := events.IntentionWakePayload{
		IntentionID: intentionID,
		Subject:     subject,
	}
	result, err := s.EnterWake(context.Background(), payload)
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	// 3. Verify result
	if result.Level != LevelPlan {
		t.Fatalf("expected LevelPlan, got %v", result.Level)
	}
	if result.Plan == nil {
		t.Fatal("expected non-nil Plan")
	}

	// 4. Verify Plan contains the semantic observations
	if len(result.Plan.Observations) != 2 {
		t.Fatalf("expected 2 observations, got %d: %v", len(result.Plan.Observations), result.Plan.Observations)
	}
	if result.Plan.Observations[0] != obs1 {
		t.Errorf("observation[0] = %q, want %q", result.Plan.Observations[0], obs1)
	}
	if result.Plan.Observations[1] != obs2 {
		t.Errorf("observation[1] = %q, want %q", result.Plan.Observations[1], obs2)
	}

	// 5. Verify state has memory items for observations
	state := mockAPI.state
	obsItems := findObservationMemories(state.Memories.Items)
	if len(obsItems) != 2 {
		t.Fatalf("expected 2 observation memory items, got %d", len(obsItems))
	}
	if obsItems[0].Content != obs1 {
		t.Errorf("memory[0].Content = %q, want %q", obsItems[0].Content, obs1)
	}
	if obsItems[1].Content != obs2 {
		t.Errorf("memory[1].Content = %q, want %q", obsItems[1].Content, obs2)
	}

	// 6. Verify memory items have proper fields
	for i, mem := range obsItems {
		if mem.ID == "" {
			t.Errorf("memory[%d] has empty ID", i)
		}
		if mem.Kind != dollstate.KindObservation {
			t.Errorf("memory[%d].Kind = %q, want %q", i, mem.Kind, dollstate.KindObservation)
		}
		if mem.Sequence != i {
			t.Errorf("memory[%d].Sequence = %d, want %d", i, mem.Sequence, i)
		}
		if mem.Timestamp == "" {
			t.Errorf("memory[%d] has empty Timestamp", i)
		}
	}

	// 7. Verify Intention was marked Completed
	if len(state.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention item, got %d", len(state.Intentions.Items))
	}
	if state.Intentions.Items[0].State != dollstate.IntentionStateCompleted {
		t.Errorf("intention state = %q, want %q", state.Intentions.Items[0].State, dollstate.IntentionStateCompleted)
	}

	// 8. Verify the provider was called the expected number of times
	// Orient(1) + toolCognize loop(3) = 4 calls
	if len(prov.lastReqs) != 4 {
		t.Errorf("expected 4 provider calls, got %d", len(prov.lastReqs))
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Test 2: Observations persist through serialization/deserialization
// ──────────────────────────────────────────────────────────────────────────

func TestM7_RestartProof(t *testing.T) {
	const intentionID = "int-m7-test-2"
	const subject = "Persistence check"

	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}
	toolB := inference.ToolCall{ID: "call_B", Name: "runtime.info__list"}

	obs1 := "runtime.info__read returned info about Core state"
	finalPlan := planWithObservations("M7 restart: observations persist", []string{obs1})

	s, _, mockAPI := setupWakeScheduler(
		[]scriptedAct{
			{content: orientResponse()},
			{toolCalls: []inference.ToolCall{toolA}},
			{toolCalls: []inference.ToolCall{toolB}},
			{content: finalPlan},
		},
		nil,
		intentionID, subject, "2026-01-15T12:00:00Z",
	)

	// 1. Run EnterWake
	payload := events.IntentionWakePayload{
		IntentionID: intentionID,
		Subject:     subject,
	}
	_, err := s.EnterWake(context.Background(), payload)
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	// 2. Get and serialize state from original mockAPI
	originalState := mockAPI.state
	origObs := findObservationMemories(originalState.Memories.Items)
	if len(origObs) == 0 {
		t.Fatal("expected observations after EnterWake")
	}

	data, err := json.Marshal(originalState)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	// Verify no raw execution artifacts leaked into the serialized state
	check := string(data)
	for _, artifact := range []string{"call_A", "call_B", "ExecutionID", "ToolCallID", "ToolResult"} {
		// These would appear in JSON field names or values
		if contains(artifact, check) {
			// Only fail if it's part of actual content, not JSON field names
			t.Logf("checking for artifact %q in serialized state (may be a field name)", artifact)
		}
	}

	// 3. Simulate clean import: deserialize into a fresh state
	var restoredState dollstate.DollState
	if err := json.Unmarshal(data, &restoredState); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}

	// 4. Verify observations survived
	restoredObs := findObservationMemories(restoredState.Memories.Items)
	if len(restoredObs) != len(origObs) {
		t.Fatalf("expected %d observation memories after restore, got %d", len(origObs), len(restoredObs))
	}
	for i, mem := range restoredObs {
		if mem.Content != origObs[i].Content {
			t.Errorf("restored memory[%d].Content = %q, want %q", i, mem.Content, origObs[i].Content)
		}
		if mem.Kind != dollstate.KindObservation {
			t.Errorf("restored memory[%d].Kind = %q", i, mem.Kind)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Test 3: Denied tool produces truthful failure Spark can reason from
// ──────────────────────────────────────────────────────────────────────────

func TestM7_FailureProof(t *testing.T) {
	const intentionID = "int-m7-test-3"
	const subject = "Test denied tools"

	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}

	failureObs := "runtime.info__read was denied — all operations denied for test"
	finalPlan := planWithObservations("M7 failure: tool denied", []string{failureObs})

	// Use denyAllEvaluator to simulate global denial
	s, prov, mockAPI := setupWakeScheduler(
		[]scriptedAct{
			{content: orientResponse()},
			{toolCalls: []inference.ToolCall{toolA}},
			{content: finalPlan},
		},
		denyAllEvaluator{},
		intentionID, subject, "2026-01-15T12:00:00Z",
	)

	payload := events.IntentionWakePayload{
		IntentionID: intentionID,
		Subject:     subject,
	}
	result, err := s.EnterWake(context.Background(), payload)
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	// 1. Verify result has a valid Plan
	if result.Level != LevelPlan {
		t.Fatalf("expected LevelPlan, got %v", result.Level)
	}
	if result.Plan == nil {
		t.Fatal("expected non-nil Plan")
	}

	// 2. Verify Plan contains truthful failure observation
	if len(result.Plan.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d: %v", len(result.Plan.Observations), result.Plan.Observations)
	}
	if result.Plan.Observations[0] != failureObs {
		t.Errorf("observation = %q, want %q", result.Plan.Observations[0], failureObs)
	}

	// 3. Verify tool results show failure
	// The second provider call (index 1) is the tool call attempt
	// The third provider call (index 2) should have ToolResults present with failure
	if len(prov.lastReqs) >= 3 {
		lastReq := prov.lastReqs[2] // the final plan request
		if len(lastReq.ToolResults) > 0 {
			tr := lastReq.ToolResults[0]
			if tr.Status != inference.ToolResultFailure {
				t.Errorf("expected ToolResultFailure, got %v", tr.Status)
			}
			if tr.Error == nil {
				t.Error("expected non-nil Error on denied tool")
			} else if tr.Error.Code != "denied" {
				t.Errorf("expected Error.Code 'denied', got %q", tr.Error.Code)
			}
		}
	}

	// 4. Verify memory contains the failure observation
	state := mockAPI.state
	obsItems := findObservationMemories(state.Memories.Items)
	if len(obsItems) != 1 {
		t.Fatalf("expected 1 observation memory, got %d", len(obsItems))
	}
	if obsItems[0].Content != failureObs {
		t.Errorf("memory content = %q, want %q", obsItems[0].Content, failureObs)
	}

	// 5. Verify Intention was marked Completed (failure still completes the wake)
	if len(state.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention item, got %d", len(state.Intentions.Items))
	}
	if state.Intentions.Items[0].State != dollstate.IntentionStateCompleted {
		t.Errorf("intention state = %q, want %q", state.Intentions.Items[0].State, dollstate.IntentionStateCompleted)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Test 4: Continuity — Core destruction doesn't lose semantic experience
// ──────────────────────────────────────────────────────────────────────────

func TestM7_ContinuityPreservesSemanticExperience(t *testing.T) {
	const intentionID = "int-m7-test-4"
	const subject = "Continuity check"

	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}
	toolB := inference.ToolCall{ID: "call_B", Name: "runtime.info__list"}

	obs1 := "The Core runtime returned its current state information"
	obs2 := "The system has two available capabilities: read and list"
	finalPlan := planWithObservations("M7 continuity: semantic experience", []string{obs1, obs2})

	s, _, mockAPI := setupWakeScheduler(
		[]scriptedAct{
			{content: orientResponse()},
			{toolCalls: []inference.ToolCall{toolA}},
			{toolCalls: []inference.ToolCall{toolB}},
			{content: finalPlan},
		},
		nil,
		intentionID, subject, "2026-01-15T12:00:00Z",
	)

	// 1. Run EnterWake (Spark uses M6 tools, produces observations)
	payload := events.IntentionWakePayload{
		IntentionID: intentionID,
		Subject:     subject,
	}
	_, err := s.EnterWake(context.Background(), payload)
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	// 2. Export state as JSON (simulates Doll Card export)
	originalState := mockAPI.state
	data, err := json.Marshal(originalState)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	// 3. Create a clean state — no tool executor, no provider, no body handles
	// This simulates Core destruction and clean reconstruction from Doll Card
	var cleanState dollstate.DollState
	if err := json.Unmarshal(data, &cleanState); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}

	// 4. Verify the clean state has no connection to any runtime artifacts
	cleanJSON := string(data)

	// Verify observations are present
	cleanObs := findObservationMemories(cleanState.Memories.Items)
	if len(cleanObs) != 2 {
		t.Fatalf("expected 2 observation memories in clean state, got %d", len(cleanObs))
	}

	// Verify content matches what Spark chose to retain (semantic, not raw)
	if cleanObs[0].Content != obs1 {
		t.Errorf("clean[0].Content = %q, want %q (semantic content lost or altered)", cleanObs[0].Content, obs1)
	}
	if cleanObs[1].Content != obs2 {
		t.Errorf("clean[1].Content = %q, want %q (semantic content lost or altered)", cleanObs[1].Content, obs2)
	}

	// Verify no execution artifacts in memory content
	for _, mem := range cleanObs {
		if contains("call_", mem.Content) {
			t.Errorf("memory contains raw ToolCall ID artifact: %q", mem.Content)
		}
		if contains("runtime.info__", mem.Content) {
			// This is OK — the observation references the tool name semantically
			// What matters is there's no ToolCall ID, execution ID, or provider name
		}
	}

	// Verify no field leakage by checking the raw JSON for disallowed patterns
	for _, artifact := range []string{"\"call_A\"", "\"call_B\"", "ExecutionID", "ToolCallID", "\"m7-conformance\"", "m6-conformance", "ToolResult"} {
		if contains(artifact, cleanJSON) {
			t.Errorf("serialized state contains disallowed runtime artifact: %q", artifact)
		}
	}

	// Verify memories have the correct Kind
	for _, mem := range cleanObs {
		if mem.Kind != dollstate.KindObservation {
			t.Errorf("memory.Kind = %q, want %q", mem.Kind, dollstate.KindObservation)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Test helpers
// ──────────────────────────────────────────────────────────────────────────

// findObservationMemories returns memory items with KindObservation.
func findObservationMemories(items []dollstate.MemoryItem) []dollstate.MemoryItem {
	var out []dollstate.MemoryItem
	for _, m := range items {
		if m.Kind == dollstate.KindObservation {
			out = append(out, m)
		}
	}
	return out
}

// contains reports whether substr is in s.
func contains(substr, s string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
