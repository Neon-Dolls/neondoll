package dollmind

import (
	"context"
	"strings"
	"testing"

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
	_, err := sched.Plan(context.Background(), events.TypeMessage, "hello", nil)
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

	plan, err := sched.Plan(context.Background(), events.TypeMessage, "Phase 3 time!", orient)
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

	_, err := sched.Plan(context.Background(), events.TypeCommand, "/status", orient)
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
	_, err := sched.Plan(context.Background(), events.TypeMessage, "test", orient)
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

	plan, err := sched.Plan(context.Background(), events.TypeMessage, "Checking continuity", orient)
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