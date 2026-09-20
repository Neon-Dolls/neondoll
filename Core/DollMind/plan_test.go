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
	if !strings.Contains(prompt, "respond") {
		t.Error("prompt should mention respond action type as an example")
	}
}

// ──────────────────────────────────────────────
// parsePlan tests
// ──────────────────────────────────────────────

func TestParsePlan_ValidWithActions(t *testing.T) {
	raw := `{"summary":"Greet Master","actions":[{"type":"respond","payload":{"text":"Hello Master!"}}]}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Greet Master" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(plan.Actions))
	}
	if plan.Actions[0].Type != "respond" {
		t.Errorf("action type = %q", plan.Actions[0].Type)
	}
	text, ok := plan.Actions[0].Payload["text"].(string)
	if !ok || text != "Hello Master!" {
		t.Errorf("payload text = %q", text)
	}
}

func TestParsePlan_StripsMarkdownFences(t *testing.T) {
	raw := "```json\n{\"summary\":\"test\",\"actions\":[{\"type\":\"respond\",\"payload\":{\"text\":\"ok\"}}]}\n```"
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "test" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(plan.Actions))
	}
}

func TestParsePlan_InvalidJSON(t *testing.T) {
	_, err := parsePlan("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParsePlan_EmptyActions(t *testing.T) {
	// Empty action list is valid — the doll may decide no immediate action is needed.
	raw := `{"summary":"Monitor only, no action needed","actions":[]}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Monitor only, no action needed" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if len(plan.Actions) != 0 {
		t.Errorf("expected 0 actions, got %d", len(plan.Actions))
	}
}

func TestParsePlan_OmitsActions(t *testing.T) {
	// No actions key at all — should produce empty list without error.
	raw := `{"summary":"Just observe"}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary != "Just observe" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if len(plan.Actions) != 0 {
		t.Errorf("expected 0 actions, got %d", len(plan.Actions))
	}
}

func TestParsePlan_MissingSummary(t *testing.T) {
	_, err := parsePlan(`{"actions":[{"type":"respond","payload":{"text":"hi"}}]}`)
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestParsePlan_MissingActionType(t *testing.T) {
	raw := `{"summary":"bad","actions":[{"payload":{"text":"hi"}}]}`
	_, err := parsePlan(raw)
	if err == nil {
		t.Fatal("expected error for action missing type")
	}
}

func TestParsePlan_NilPayload(t *testing.T) {
	// Action with nil payload should produce empty map, not crash.
	raw := `{"summary":"test","actions":[{"type":"respond"}]}`
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(plan.Actions))
	}
	if plan.Actions[0].Payload == nil {
		t.Error("expected non-nil payload map for action without payload")
	}
}

// ──────────────────────────────────────────────
// Scheduler.Plan tests
// ──────────────────────────────────────────────

func TestPlan_NilOrientation(t *testing.T) {
	provider := &orientTestProvider{response: `{"summary":"test","actions":[]}`}
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
	json := `{"summary":"Greet and check goals","actions":[{"type":"respond","payload":{"text":"Hello Master! Ready to work on Phase 3."}}]}`
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
	if len(plan.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(plan.Actions))
	}
	if plan.Actions[0].Type != "respond" {
		t.Errorf("action type = %q", plan.Actions[0].Type)
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
	json := `{"summary":"checked","actions":[]}`
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
		"PLANNING",
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