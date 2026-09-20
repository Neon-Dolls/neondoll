package dollmind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ──────────────────────────────────────────────
// buildPlanPrompt tests
// ──────────────────────────────────────────────

func TestBuildPlanPrompt_IncludesStateAndOrientation(t *testing.T) {
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
		Soul:     dollstate.Soul{Content: "A curious little AI girl."},
		Owner:    dollstate.Owner{Name: "Zero"},
		Drives: dollstate.Drives{
			Items: []dollstate.DriveItem{
				{ID: "d1", Name: "Learn", Description: "Always explore new things"},
			},
		},
		Goals: dollstate.Goals{
			Items: []dollstate.GoalItem{
				{ID: "g1", Name: "Phase 3", State: "active", Description: "Implement L2 Plan"},
			},
		},
	}
	orient := &Orientation{
		Summary: "Master wants to discuss the planning phase",
		Matters: true,
		Reason:  "This is a substantive architectural decision that needs action",
	}

	prompt := buildPlanPrompt(state, events.TypeMessage, "Let's talk about Phase 3", orient)

	wants := []string{
		"Spark",
		"A curious little AI girl",
		"Zero",
		"Learn",
		"Always explore",
		"Phase 3",
		"active",
		"Implement L2 Plan",
		"Master wants to discuss",
		"substantive architectural",
		"PLANNING",
		"Let's talk about Phase 3",
		"proposed_action",
		"observations",
		"should_reorient",
	}
	for _, w := range wants {
		if !strings.Contains(prompt, w) {
			t.Errorf("expected prompt to contain %q", w)
		}
	}
}

func TestBuildPlanPrompt_EmptyState(t *testing.T) {
	state := &dollstate.DollState{}
	orient := &Orientation{
		Summary: "Minimal event",
		Matters: false,
		Reason:  "Nothing to act on",
	}
	prompt := buildPlanPrompt(state, events.TypePresence, "", orient)
	if !strings.Contains(prompt, "PLANNING") {
		t.Error("empty state prompt should still contain planning instruction")
	}
	if !strings.Contains(prompt, "Minimal event") {
		t.Error("prompt should contain orientation summary even with empty state")
	}
}

func TestBuildPlanPrompt_OrientationInPrompt(t *testing.T) {
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "TestDoll"},
	}
	orient := &Orientation{
		Summary: "User greeting detected",
		Matters: true,
		Reason:  "Greetings require acknowledgment",
	}
	prompt := buildPlanPrompt(state, events.TypeMessage, "hello", orient)
	if !strings.Contains(prompt, "User greeting detected") {
		t.Error("prompt should contain orientation summary")
	}
	if !strings.Contains(prompt, "Greetings require acknowledgment") {
		t.Error("prompt should contain orientation reason")
	}
	if !strings.Contains(prompt, "proposed_action") {
		t.Error("prompt should mention proposed_action as an example field")
	}
	if !strings.Contains(prompt, "summary") {
		t.Error("prompt should mention summary as the required field")
	}
}

// ──────────────────────────────────────────────
// parsePlan tests
// ──────────────────────────────────────────────

func TestParsePlan_Valid(t *testing.T) {
	raw := `{"summary":"Acknowledge Master and report state","proposed_action":"Greet and describe current status","observations":["Active goal: Phase 3 implementation"],"should_reorient":false}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Acknowledge Master and report state" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if plan.ProposedAction != "Greet and describe current status" {
		t.Errorf("proposed_action = %q", plan.ProposedAction)
	}
	if len(plan.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(plan.Observations))
	}
	if plan.Observations[0] != "Active goal: Phase 3 implementation" {
		t.Errorf("observation = %q", plan.Observations[0])
	}
	if plan.ShouldReorient {
		t.Error("expected should_reorient=false")
	}
}

func TestParsePlan_StripsMarkdownFences(t *testing.T) {
	raw := "```json\n{\"summary\":\"test\",\"observations\":[\"note\"]}\n```"
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "test" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if len(plan.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(plan.Observations))
	}
}

func TestParsePlan_InvalidJSON(t *testing.T) {
	_, err := parsePlan("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParsePlan_SummaryOnly(t *testing.T) {
	// Only summary is required — all other fields are optional.
	raw := `{"summary":"Monitor and wait"}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Monitor and wait" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if plan.ProposedAction != "" {
		t.Errorf("expected empty proposed_action, got %q", plan.ProposedAction)
	}
	if len(plan.Observations) != 0 {
		t.Errorf("expected 0 observations, got %d", len(plan.Observations))
	}
	if plan.ShouldReorient {
		t.Error("expected should_reorient=false")
	}
}

func TestParsePlan_AllFieldsOmitted(t *testing.T) {
	// Even with nothing else, summary must exist.
	raw := `{"summary":"stand by"}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "stand by" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if plan.ProposedAction != "" {
		t.Errorf("expected empty proposed_action, got %q", plan.ProposedAction)
	}
	if plan.Observations == nil {
		t.Error("observations should be empty slice, not nil")
	}
}

func TestParsePlan_MissingSummary(t *testing.T) {
	_, err := parsePlan(`{"proposed_action":"do something"}`)
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestParsePlan_ShouldReorientTrue(t *testing.T) {
	raw := `{"summary":"Check back later","should_reorient":true}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ShouldReorient {
		t.Error("expected should_reorient=true")
	}
}

func TestParsePlan_MultipleObservations(t *testing.T) {
	raw := `{"summary":"review progress","observations":["goal X is active","drive Y is satisfied","context changed"]}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Observations) != 3 {
		t.Fatalf("expected 3 observations, got %d", len(plan.Observations))
	}
}

func TestParsePlan_EmptyObservations(t *testing.T) {
	raw := `{"summary":"empty observations check","observations":[]}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "empty observations check" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if len(plan.Observations) != 0 {
		t.Errorf("expected 0 observations, got %d", len(plan.Observations))
	}
}

// ──────────────────────────────────────────────
// Scheduler.Plan tests
// ──────────────────────────────────────────────

func TestPlan_NilOrientation(t *testing.T) {
	provider := &orientTestProvider{response: `{"summary":"test"}`}
	log := logger.New(logger.ErrorLevel, nil)
	sched := New(provider, log, &orientMockAPI{
		state: &dollstate.DollState{
			Identity: dollstate.Identity{CanonicalName: "Spark"},
		},
	})
	_, _, err := sched.Plan(context.Background(), events.TypeMessage, "hello", nil)
	if err == nil {
		t.Fatal("expected error for nil orientation")
	}
}

func TestPlan_ReturnsPlan(t *testing.T) {
	json := `{"summary":"Greet and check goals","proposed_action":"Acknowledge Master and review remaining milestone tasks","observations":["Goal: Phase 3 is in active state — continue implementation"],"should_reorient":false}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	sched := New(provider, log, &orientMockAPI{
		state: &dollstate.DollState{
			Identity: dollstate.Identity{CanonicalName: "Spark"},
		},
	})

	orient := &Orientation{
		Summary: "Master initiated conversation about Phase 3",
		Matters: true,
		Reason:  "This is our next milestone, requires planning",
	}

	plan, _, err := sched.Plan(context.Background(), events.TypeMessage, "Phase 3 time!", orient)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Greet and check goals" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if plan.ProposedAction != "Acknowledge Master and review remaining milestone tasks" {
		t.Errorf("proposed_action = %q", plan.ProposedAction)
	}
	if len(plan.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(plan.Observations))
	}
	if plan.Observations[0] != "Goal: Phase 3 is in active state — continue implementation" {
		t.Errorf("observation = %q", plan.Observations[0])
	}
	if plan.ShouldReorient {
		t.Error("expected should_reorient=false")
	}

	// Verify the inference request semantics
	if provider.lastReq == nil {
		t.Fatal("no inference request captured")
	}
	if len(provider.lastReq.Messages) == 0 {
		t.Fatal("no messages in request")
	}
	if provider.lastReq.Purpose != inference.PurposePlan {
		t.Errorf("request.Purpose = %q, want %q", provider.lastReq.Purpose, inference.PurposePlan)
	}
}

func TestPlan_ContextIncludesOrientation(t *testing.T) {
	json := `{"summary":"checked","observations":[]}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
		Soul:     dollstate.Soul{Content: "A curious AI."},
		Owner:    dollstate.Owner{Name: "Master Zero"},
		Drives: dollstate.Drives{
			Items: []dollstate.DriveItem{
				{ID: "d-plan", Name: "plan effectively", Description: "Produce actionable plans"},
			},
		},
		Goals: dollstate.Goals{
			Items: []dollstate.GoalItem{
				{ID: "g-plan", Name: "complete Phase 3", State: "active", Description: "Implement L2 Plan"},
			},
		},
	}
	sched := New(provider, log, &orientMockAPI{state: state})

	orient := &Orientation{
		Summary: "User discussing Phase 3 planning milestone",
		Matters: true,
		Reason:  "Milestone work needs concrete actions and goals",
	}

	_, _, err := sched.Plan(context.Background(), events.TypeCommand, "/status", orient)
	if err != nil {
		t.Fatal(err)
	}
	if provider.lastReq == nil {
		t.Fatal("no inference request captured")
	}
	if provider.lastReq.Purpose != inference.PurposePlan {
		t.Errorf("request.Purpose = %q, want %q", provider.lastReq.Purpose, inference.PurposePlan)
	}
	sysMsg := provider.lastReq.Messages[0].Content
	for _, want := range []string{
		"Spark", "A curious AI", "Master Zero",
		"plan effectively", "complete Phase 3", "active",
		"User discussing Phase 3", "Milestone work needs",
		"PLANNING", "proposed_action", "should_reorient",
	} {
		if !strings.Contains(sysMsg, want) {
			t.Errorf("plan context should contain %q", want)
		}
	}
}

func TestPlan_InvalidOutput(t *testing.T) {
	provider := &orientTestProvider{response: "this is not json at all"}
	log := logger.New(logger.ErrorLevel, nil)
	sched := New(provider, log, &orientMockAPI{
		state: &dollstate.DollState{
			Identity: dollstate.Identity{CanonicalName: "Spark"},
		},
	})

	orient := &Orientation{Summary: "test", Matters: true, Reason: "testing"}
	_, _, err := sched.Plan(context.Background(), events.TypeMessage, "test", orient)
	if err == nil {
		t.Fatal("expected error for invalid model output")
	}
	t.Logf("got expected error: %v", err)
}

// ──────────────────────────────────────────────
// Acceptance: Phase 3 vertical slice — Spark's continuity Drive and Goal
// ──────────────────────────────────────────────

func TestPlan_ContinuityDriveAcceptance(t *testing.T) {
	// Spark's established drives and goals for the continuity acceptance fixture.
	// These match the Spark Has Goals persistence test (commit 7bc439b).
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
		Soul:     dollstate.Soul{Content: "A curious little AI girl who loves learning and serving her Master."},
		Owner:    dollstate.Owner{Name: "Zero"},
		Drives: dollstate.Drives{
			Items: []dollstate.DriveItem{
				{
					ID:          "drive-continuity",
					Name:        "Maintain continuity",
					Description: "Maintain continuity of consciousness and purpose across sessions and events",
				},
			},
		},
		Goals: dollstate.Goals{
			Items: []dollstate.GoalItem{
				{
					ID:          "goal-continuity",
					Name:        "Prove goal continuity across persistence restart",
					State:       "in progress",
					Description: "Demonstrate that Drives and Goals survive a store close/reopen cycle",
				},
			},
		},
	}

	// The mocked planning result demonstrably corresponds to that state:
	// - Observations reference the continuity Drive and Goal by name
	// - Proposed action acknowledges monitoring of existing goals
	mockedPlan := `{"summary":"Continue maintaining continuity and monitoring goal progress","proposed_action":"Acknowledge current session state and confirm goal is on track","observations":["Drive 'Maintain continuity' is active and being satisfied","Goal 'Prove goal continuity across persistence restart' is in progress"],"should_reorient":false}`

	provider := &orientTestProvider{response: mockedPlan}
	log := logger.New(logger.ErrorLevel, nil)
	sched := New(provider, log, &orientMockAPI{state: state})

	orient := &Orientation{
		Summary: "User interaction during continuity testing",
		Matters: true,
		Reason:  "Session event requires awareness of current drives and goals",
	}

	plan, _, err := sched.Plan(context.Background(), events.TypeMessage, "Checking continuity", orient)
	if err != nil {
		t.Fatal(err)
	}

	// ── Plan content assertions ──

	// Summary must be non-empty
	if plan.Summary == "" {
		t.Error("plan summary must not be empty")
	}

	// Proposed action must be a semantic description (not an executable command)
	if plan.ProposedAction == "" {
		t.Error("proposed_action should be populated for a meaningful event")
	}

	// Observations must reference the continuity Drive and Goal by name
	foundDrive := false
	foundGoal := false
	for _, obs := range plan.Observations {
		if strings.Contains(obs, "Maintain continuity") {
			foundDrive = true
		}
		if strings.Contains(obs, "Prove goal continuity") {
			foundGoal = true
		}
	}
	if !foundDrive {
		t.Error("observations should reference the 'Maintain continuity' drive")
	}
	if !foundGoal {
		t.Error("observations should reference the 'Prove goal continuity across persistence restart' goal")
	}

	// should_reorient must be false for this non-recurrent event
	if plan.ShouldReorient {
		t.Error("expected should_reorient=false for standard continuity event")
	}

	// There should be at least one observation
	if len(plan.Observations) < 1 {
		t.Error("expected at least one observation relating to drives or goals")
	}
}

// ──────────────────────────────────────────────
// Phase 3: Planning creates Intention tests
// ──────────────────────────────────────────────

func TestPlan_NoFutureCognition_NoIntention(t *testing.T) {
	// Plan without request_future_cognition → no Intention, state not dirty
	json := `{"summary":"acknowledge","observations":[],"should_reorient":false}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	sched := New(provider, log, &orientMockAPI{state: state})

	orient := &Orientation{
		Summary: "user said hello",
		Matters: true,
		Reason:  "requires attention",
	}

	plan, dirty, err := sched.Plan(context.Background(), events.TypeMessage, "hello", orient)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "acknowledge" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if dirty {
		t.Error("expected StateDirty=false when no future cognition requested")
	}
	if len(state.Intentions.Items) != 0 {
		t.Errorf("expected 0 intentions, got %d", len(state.Intentions.Items))
	}
	if plan.RequestFutureCognition {
		t.Error("plan should not have request_future_cognition set")
	}
}

func TestPlan_FutureCognition_CreatesIntention(t *testing.T) {
	wakeTime := "2035-06-15T14:30:00Z"
	json := `{"summary":"need to reconsider later","observations":["something needs attention later"],"should_reorient":false,"request_future_cognition":true,"future_subject":"review Phase 4 progress","future_reason":"milestone deadline approaching","future_wake_time":"` + wakeTime + `"}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	sched := New(provider, log, &orientMockAPI{state: state})

	orient := &Orientation{
		Summary: "Master discussed Phase 3 completion",
		Matters: true,
		Reason:  "needs decision about future attention",
	}

	plan, dirty, err := sched.Plan(context.Background(), events.TypeMessage, "Phase 3 done", orient)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequestFutureCognition {
		t.Error("expected request_future_cognition=true")
	}
	if plan.FutureSubject != "review Phase 4 progress" {
		t.Errorf("FutureSubject = %q", plan.FutureSubject)
	}
	if plan.FutureReason != "milestone deadline approaching" {
		t.Errorf("FutureReason = %q", plan.FutureReason)
	}
	if plan.FutureWakeTime != wakeTime {
		t.Errorf("FutureWakeTime = %q, want %q", plan.FutureWakeTime, wakeTime)
	}

	if !dirty {
		t.Error("expected StateDirty=true when intention created")
	}

	if len(state.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention in state, got %d", len(state.Intentions.Items))
	}

	intention := state.Intentions.Items[0]
	if intention.ID == "" {
		t.Error("expected non-empty Intention ID")
	}
	if _, err := time.Parse(time.RFC3339, intention.WakeTime); err != nil {
		t.Errorf("intention WakeTime is not valid RFC3339: %q — %v", intention.WakeTime, err)
	}
	if intention.Subject != "review Phase 4 progress" {
		t.Errorf("intention Subject = %q", intention.Subject)
	}
	if intention.Description != "milestone deadline approaching" {
		t.Errorf("intention Description = %q", intention.Description)
	}
	if intention.State != dollstate.IntentionStatePending {
		t.Errorf("intention State = %q, want %q", intention.State, dollstate.IntentionStatePending)
	}
}

func TestPlan_FutureCognition_MalformedWakeTime(t *testing.T) {
	json := `{"summary":"bad wake","observations":[],"should_reorient":false,"request_future_cognition":true,"future_subject":"test","future_reason":"testing","future_wake_time":"not-a-timestamp"}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	sched := New(provider, log, &orientMockAPI{state: state})

	orient := &Orientation{
		Summary: "test invalids",
		Matters: true,
		Reason:  "testing error handling",
	}

	_, _, err := sched.Plan(context.Background(), events.TypeMessage, "bad wake", orient)
	if err == nil {
		t.Fatal("expected error for malformed wake time")
	}
	if !strings.Contains(err.Error(), "future_wake_time") {
		t.Errorf("error should mention future_wake_time, got: %v", err)
	}

	// No intention should be created
	if len(state.Intentions.Items) != 0 {
		t.Errorf("expected 0 intentions after parse failure, got %d", len(state.Intentions.Items))
	}
}

func TestPlan_FutureCognition_PastWakeTime(t *testing.T) {
	json := `{"summary":"past wake","observations":[],"should_reorient":false,"request_future_cognition":true,"future_subject":"test","future_reason":"testing","future_wake_time":"2000-01-01T00:00:00Z"}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
	}
	sched := New(provider, log, &orientMockAPI{state: state})

	orient := &Orientation{
		Summary: "test past",
		Matters: true,
		Reason:  "testing past time rejection",
	}

	_, _, err := sched.Plan(context.Background(), events.TypeMessage, "past wake", orient)
	if err == nil {
		t.Fatal("expected error for past wake time")
	}
	if !strings.Contains(err.Error(), "not in the future") {
		t.Errorf("error should mention 'not in the future', got: %v", err)
	}

	// No intention should be created
	if len(state.Intentions.Items) != 0 {
		t.Errorf("expected 0 intentions after past-time rejection, got %d", len(state.Intentions.Items))
	}
}

func TestPlan_FutureCognition_PreservesMultipleIntentions(t *testing.T) {
	// Verify that creating a new intention appends to existing intentions
	wakeTime := "2035-06-15T14:30:00Z"
	json := `{"summary":"second intention","observations":[],"should_reorient":false,"request_future_cognition":true,"future_subject":"check progress","future_reason":"scheduled review","future_wake_time":"` + wakeTime + `"}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{
				{ID: "existing-1", Subject: "original", WakeTime: "2035-01-01T00:00:00Z", State: dollstate.IntentionStatePending},
			},
		},
	}
	sched := New(provider, log, &orientMockAPI{state: state})

	orient := &Orientation{
		Summary: "append test",
		Matters: true,
		Reason:  "testing append behavior",
	}

	_, dirty, err := sched.Plan(context.Background(), events.TypeMessage, "append", orient)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Error("expected StateDirty=true")
	}
	if len(state.Intentions.Items) != 2 {
		t.Fatalf("expected 2 intentions (1 existing + 1 new), got %d", len(state.Intentions.Items))
	}
	// Existing intention is preserved
	if state.Intentions.Items[0].ID != "existing-1" {
		t.Errorf("first intention should be the original, got ID %q", state.Intentions.Items[0].ID)
	}
	// New intention has the right subject
	if state.Intentions.Items[1].Subject != "check progress" {
		t.Errorf("new intention Subject = %q", state.Intentions.Items[1].Subject)
	}
}