// Package dollmind provides the core cognition pipeline for NeonDoll.
//
// M7 Conformance — Spark Acts and Observes.
//
// Thesis: a pending Intention autonomously wakes Spark, Spark uses M6 tools
// (runtime.info/read, runtime.info/list), sees tool results in the same
// cognition run, produces a final Plan. Spark may explicitly select semantic
// experience for durable retention. Only explicitly retained experience
// becomes portable Doll Memory through the Doll Card continuity boundary.

package dollmind

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	dollcard "github.com/Neon-Dolls/neondoll/DollCard"
	events "github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ── M7 Helpers ───────────────────────────────────────────────────────────

// wakeState creates a DollState with a single pending Intention whose WakeTime
// is in the past, suitable for EnterWake tests.
func wakeState(intentionID, subject, wakeTime string) *dollstate.DollState {
	state := newConformanceState()
	state.Identity.DollID = "spark-dev"
	state.Owner.Name = "Zero" // dollcard requires non-empty owner for encode/decode
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

// planWithObservations returns a Plan JSON with observations but NO retain_experience.
// Observations are ephemeral — available only to this cognition run.
func planWithObservations(summary string, observations []string) string {
	// Manually build without retain_experience field
	b := `{"summary":"` + summary + `","proposed_action":"observed_and_acted","observations":[`
	for i, obs := range observations {
		if i > 0 {
			b += ","
		}
		b += `"` + obs + `"`
	}
	b += `],"should_reorient":false,"request_future_cognition":false,"future_subject":"","future_reason":"","future_wake_time":""}`
	return b
}

// planWithRetainedExperience returns a Plan JSON with both observations and
// retain_experience. Only retain_experience entries become durable Doll Memory.
func planWithRetainedExperience(summary string, observations, retain []string) string {
	b := `{"summary":"` + summary + `","proposed_action":"observed_and_acted","observations":[`
	for i, obs := range observations {
		if i > 0 {
			b += ","
		}
		b += `"` + obs + `"`
	}
	b += `],"retain_experience":[`
	for i, exp := range retain {
		if i > 0 {
			b += ","
		}
		b += `"` + exp + `"`
	}
	b += `],"should_reorient":false,"request_future_cognition":false,"future_subject":"","future_reason":"","future_wake_time":""}`
	return b
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

// ══════════════════════════════════════════════════════════════════════════
// Test 1: Tool result available to cognition — observations are ephemeral
// ══════════════════════════════════════════════════════════════════════════

func TestM7_ToolResultAvailableToCognition(t *testing.T) {
	// Proves:
	//   1. tool result is available to the same cognition run
	//   2. Plan.Observations capture semantic meaning
	//   3. observations alone do NOT materialise as Doll Memory
	//   4. ordinary observations do NOT dirty Doll State

	const intentionID = "int-m7-test-1"
	const subject = "Check runtime capabilities"

	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}
	toolB := inference.ToolCall{ID: "call_B", Name: "runtime.info__list"}

	obs1 := "runtime.info__read returned data about the Core"
	obs2 := "runtime.info__list listed available capabilities"
	finalPlan := planWithObservations("M7: tool result available to cognition", []string{obs1, obs2})

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

	// 2. Build payload and call EnterWake
	payload := events.IntentionWakePayload{
		IntentionID: intentionID,
		Subject:     subject,
	}
	result, err := s.EnterWake(context.Background(), payload)
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	// 3. Verify result has a Plan with observations
	if result.Level != LevelPlan {
		t.Fatalf("expected LevelPlan, got %v", result.Level)
	}
	if result.Plan == nil {
		t.Fatal("expected non-nil Plan")
	}
	if len(result.Plan.Observations) != 2 {
		t.Fatalf("expected 2 observations, got %d: %v", len(result.Plan.Observations), result.Plan.Observations)
	}
	if result.Plan.Observations[0] != obs1 {
		t.Errorf("observation[0] = %q, want %q", result.Plan.Observations[0], obs1)
	}
	if result.Plan.Observations[1] != obs2 {
		t.Errorf("observation[1] = %q, want %q", result.Plan.Observations[1], obs2)
	}

	// 4. Verify tool results are available in the provider's last request
	//    (proves tool results fed back into same cognition run)
	lastReq := prov.lastReqs[len(prov.lastReqs)-1]
	if len(lastReq.ToolResults) == 0 {
		t.Error("expected tool results in final provider request")
	}
	for _, tr := range lastReq.ToolResults {
		if tr.Status != inference.ToolResultSuccess {
			t.Errorf("expected ToolResultSuccess, got %v", tr.Status)
		}
	}

	// 5. Verify StateDirty=false — observations alone do NOT materialise
	if result.StateDirty {
		t.Error("expected StateDirty=false (observations are ephemeral)")
	}

	// 6. Verify NO KindObservation memories were created
	obsItems := findObservationMemories(mockAPI.state.Memories.Items)
	if len(obsItems) != 0 {
		t.Errorf("expected 0 KindObservation memories (observations are ephemeral), got %d", len(obsItems))
	}

	// 7. Verify Intention was marked Completed
	if len(mockAPI.state.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention item, got %d", len(mockAPI.state.Intentions.Items))
	}
	if mockAPI.state.Intentions.Items[0].State != dollstate.IntentionStateCompleted {
		t.Errorf("intention state = %q, want %q",
			mockAPI.state.Intentions.Items[0].State, dollstate.IntentionStateCompleted)
	}
}

// ══════════════════════════════════════════════════════════════════════════
// Test 2: Explicitly retained experience becomes durable Doll Memory
// ══════════════════════════════════════════════════════════════════════════

func TestM7_ExplicitRetention(t *testing.T) {
	// Proves:
	//   1. Plan.Observations are available to cognition (ephemeral)
	//   2. Plan.RetainExperience entries are semantically distinct
	//   3. Only RetainExperience becomes KindObservation Doll Memory
	//   4. Observations alone do NOT create memory items
	//   5. State IS dirty when experience is explicitly retained

	const intentionID = "int-m7-test-2"
	const subject = "Test explicit retention"

	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}
	toolB := inference.ToolCall{ID: "call_B", Name: "runtime.info__list"}

	obs1 := "runtime.info__read returned info about Core state"
	obs2 := "runtime.info__list showed available capabilities"
	retain1 := "The Core runtime is operational and has two capabilities: read and list"
	finalPlan := planWithRetainedExperience(
		"M7: explicitly retained experience",
		[]string{obs1, obs2}, // ephemeral observations
		[]string{retain1},    // durable experience — only this survives
	)

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

	payload := events.IntentionWakePayload{
		IntentionID: intentionID,
		Subject:     subject,
	}
	result, err := s.EnterWake(context.Background(), payload)
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}

	// 1. Verify Plan has both observations and retain_experience
	if result.Plan == nil {
		t.Fatal("expected non-nil Plan")
	}
	if len(result.Plan.Observations) != 2 {
		t.Errorf("expected 2 ephemeral observations, got %d", len(result.Plan.Observations))
	}
	if len(result.Plan.RetainExperience) != 1 {
		t.Fatalf("expected 1 retained experience entry, got %d", len(result.Plan.RetainExperience))
	}
	if result.Plan.RetainExperience[0] != retain1 {
		t.Errorf("retain_experience[0] = %q, want %q", result.Plan.RetainExperience[0], retain1)
	}

	// 2. Verify ONLY retained experience became KindObservation memory
	obsItems := findObservationMemories(mockAPI.state.Memories.Items)
	if len(obsItems) != 1 {
		t.Fatalf("expected exactly 1 KindObservation memory (only retained), got %d: %v",
			len(obsItems), obsItems)
	}
	if obsItems[0].Content != retain1 {
		t.Errorf("memory[0].Content = %q, want %q (retained experience)", obsItems[0].Content, retain1)
	}
	if obsItems[0].Kind != dollstate.KindObservation {
		t.Errorf("memory[0].Kind = %q, want %q", obsItems[0].Kind, dollstate.KindObservation)
	}
	if obsItems[0].ID == "" {
		t.Error("memory[0] has empty ID")
	}
	if obsItems[0].Timestamp == "" {
		t.Error("memory[0] has empty Timestamp")
	}

	// 3. Verify StateDirty=true — retained experience materialised
	if !result.StateDirty {
		t.Error("expected StateDirty=true (retained experience was materialised)")
	}

	// 4. Verify total memory count is exactly 1 (no extra observations leaked)
	if len(mockAPI.state.Memories.Items) != 1 {
		t.Errorf("expected exactly 1 total memory item, got %d: %v",
			len(mockAPI.state.Memories.Items), mockAPI.state.Memories.Items)
	}
}

// ══════════════════════════════════════════════════════════════════════════
// Test 3: Retained experience survives real Doll Card export/import
// ══════════════════════════════════════════════════════════════════════════

func TestM7_ContinuityBoundary(t *testing.T) {
	// Proves:
	//   1. Retained experience survives actual Doll Card encode/decode
	//   2. No ToolCall/Execution/provider/runtime artifacts in decoded state
	//   3. Semantic experience is portable independent of execution machinery
	//   4. KindObservation memories carry correct semantic content

	const intentionID = "int-m7-test-3"
	const subject = "Continuity check"

	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}
	toolB := inference.ToolCall{ID: "call_B", Name: "runtime.info__list"}

	retain1 := "The Core runtime returned its current state information"
	retain2 := "The system has two available capabilities: read and list"
	finalPlan := planWithRetainedExperience(
		"M7 continuity: semantic experience",
		[]string{retain1, retain2}, // also visible as ephemeral observations
		[]string{retain1, retain2}, // explicitly retained for durability
	)

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
	result, err := s.EnterWake(context.Background(), payload)
	if err != nil {
		t.Fatalf("EnterWake: %v", err)
	}
	if !result.StateDirty {
		t.Error("expected StateDirty=true (retained experience was materialised)")
	}

	// 2. Export state to Doll Card (canonical encode/decode boundary)
	var cardBuf bytes.Buffer
	if err := dollcard.Encode(mockAPI.state, &cardBuf); err != nil {
		t.Fatalf("dollcard.Encode: %v", err)
	}
	cardBytes := cardBuf.Bytes()
	if len(cardBytes) == 0 {
		t.Fatal("dollcard produced empty output")
	}

	// 3. Decode into a clean state — no connection to any Core runtime
	restored, err := dollcard.DecodeFromReader(bytes.NewReader(cardBytes), int64(len(cardBytes)))
	if err != nil {
		t.Fatalf("dollcard.DecodeFromReader: %v", err)
	}
	if restored == nil {
		t.Fatal("DecodeFromReader returned nil")
	}

	// 4. Verify selected experience survived as KindObservation memories
	restoredObs := findObservationMemories(restored.Memories.Items)
	if len(restoredObs) != 2 {
		t.Fatalf("expected 2 KindObservation memories after restore, got %d: %v",
			len(restoredObs), restoredObs)
	}

	// Verify content matches what Spark chose to retain
	if restoredObs[0].Content != retain1 {
		t.Errorf("restored[0].Content = %q, want %q (semantic content)", restoredObs[0].Content, retain1)
	}
	if restoredObs[1].Content != retain2 {
		t.Errorf("restored[1].Content = %q, want %q (semantic content)", restoredObs[1].Content, retain2)
	}

	// Verify memory kind
	for i, mem := range restoredObs {
		if mem.Kind != dollstate.KindObservation {
			t.Errorf("restored[%d].Kind = %q, want %q", i, mem.Kind, dollstate.KindObservation)
		}
	}

	// 5. Verify no execution artifacts in memory content
	for _, mem := range restoredObs {
		if strings.Contains(mem.Content, "call_") {
			t.Errorf("memory contains raw ToolCall ID artifact: %q", mem.Content)
		}
		if strings.Contains(mem.Content, "ExecutionID") {
			t.Errorf("memory contains execution ID artifact: %q", mem.Content)
		}
		if strings.Contains(mem.Content, "ToolResult") {
			t.Errorf("memory contains ToolResult artifact: %q", mem.Content)
		}
	}

	// 6. Verify no provider or runtime metadata leaked into restored state
	rawJSON := cardBytes // the actual card bytes from encode
	cardStr := string(rawJSON)
	for _, artifact := range []string{"call_A", "call_B", "m7-conformance", "ExecutionID", "ToolCallID", "ToolResult"} {
		if strings.Contains(cardStr, artifact) {
			t.Errorf("Doll Card contains disallowed runtime artifact: %q", artifact)
		}
	}

	// 7. Verify the restored state has no connection to original execution
	if restored.Identity.CanonicalName != "Spark" {
		t.Errorf("restored Identity.CanonicalName = %q, want %q",
			restored.Identity.CanonicalName, "Spark")
	}
}

// ══════════════════════════════════════════════════════════════════════════
// Test 4: Denied tool produces truthful failure — explicit retention only
// ══════════════════════════════════════════════════════════════════════════

func TestM7_FailureExperience(t *testing.T) {
	// Proves:
	//   1. Denied tool returns truthful failure ToolResult
	//   2. Same L2 run continues with failure observation visible
	//   3. Only explicitly retained experience becomes memory
	//   4. No fabricated success — failure observation is truthful
	//   5. Only retained content persists, not every observation

	const intentionID = "int-m7-test-4"
	const subject = "Test denied tools"

	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}

	failureObs := "runtime.info__read was denied — all operations denied for test"
	retainExperience := "The authority guard denied the runtime.info__read tool. The Core respects security boundaries."
	finalPlan := planWithRetainedExperience(
		"M7 failure: tool denied",
		[]string{failureObs},       // truthful observation available to cognition
		[]string{retainExperience}, // only this is selected for durable memory
	)

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

	// 1. Verify result has a valid Plan with truthful failure observation
	if result.Level != LevelPlan {
		t.Fatalf("expected LevelPlan, got %v", result.Level)
	}
	if result.Plan == nil {
		t.Fatal("expected non-nil Plan")
	}
	if len(result.Plan.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d: %v", len(result.Plan.Observations), result.Plan.Observations)
	}
	if result.Plan.Observations[0] != failureObs {
		t.Errorf("observation = %q, want %q (truthful failure)", result.Plan.Observations[0], failureObs)
	}

	// 2. Verify Plan has retain_experience
	if len(result.Plan.RetainExperience) != 1 {
		t.Fatalf("expected 1 retained experience entry, got %d", len(result.Plan.RetainExperience))
	}
	if result.Plan.RetainExperience[0] != retainExperience {
		t.Errorf("retain_experience[0] = %q, want %q", result.Plan.RetainExperience[0], retainExperience)
	}

	// 3. Verify tool results show failure in the final provider request
	//    This is a hard requirement — if the denied ToolResult never reaches
	//    the continuing cognition, the M7 failure-acceptance condition fails.
	lastReq := prov.lastReqs[len(prov.lastReqs)-1]
	if len(lastReq.ToolResults) != 1 {
		t.Fatalf("expected exactly 1 ToolResult in final provider request (denied tool must produce visible failure), got %d", len(lastReq.ToolResults))
	}
	tr := lastReq.ToolResults[0]
	if tr.Status != inference.ToolResultFailure {
		t.Errorf("expected ToolResultFailure, got %v", tr.Status)
	}
	if tr.Error == nil {
		t.Error("expected non-nil Error on denied tool")
	} else if tr.Error.Code != "denied" {
		t.Errorf("expected Error.Code 'denied', got %q", tr.Error.Code)
	}

	// 4. Verify ONLY retained experience became memory (not the observation)
	obsItems := findObservationMemories(mockAPI.state.Memories.Items)
	if len(obsItems) != 1 {
		t.Fatalf("expected exactly 1 KindObservation memory (only retained), got %d: %v",
			len(obsItems), obsItems)
	}
	if obsItems[0].Content != retainExperience {
		t.Errorf("memory.Content = %q, want %q (retained experience)", obsItems[0].Content, retainExperience)
	}
	if obsItems[0].Kind != dollstate.KindObservation {
		t.Errorf("memory.Kind = %q, want %q", obsItems[0].Kind, dollstate.KindObservation)
	}

	// 5. Verify state is dirty (retained experience was materialised)
	if !result.StateDirty {
		t.Error("expected StateDirty=true (retained experience was materialised)")
	}

	// 6. Verify total memory count is exactly 1 (no extra observations leaked)
	if len(mockAPI.state.Memories.Items) != 1 {
		t.Errorf("expected exactly 1 total memory item, got %d: %v",
			len(mockAPI.state.Memories.Items), mockAPI.state.Memories.Items)
	}

	// 7. Verify Intention was marked Completed (failure still completes the wake)
	if len(mockAPI.state.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention item, got %d", len(mockAPI.state.Intentions.Items))
	}
	if mockAPI.state.Intentions.Items[0].State != dollstate.IntentionStateCompleted {
		t.Errorf("intention state = %q, want %q",
			mockAPI.state.Intentions.Items[0].State, dollstate.IntentionStateCompleted)
	}
}
