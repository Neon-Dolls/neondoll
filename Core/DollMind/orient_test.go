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

// orientTestProvider returns a fixed response and captures the last request.
type orientTestProvider struct {
	name      string
	response  string
	lastReq   *inference.Request
	callCount atomic.Int64
}

func (p *orientTestProvider) Name() string                { return p.name }
func (p *orientTestProvider) ID() inference.ProviderID    { return inference.ProviderID(p.name) }
func (p *orientTestProvider) Infer(_ context.Context, req inference.Request) (*inference.Response, error) {
	p.callCount.Add(1)
	p.lastReq = &req
	return &inference.Response{
		Content:    p.response,
		ProviderID: inference.ProviderID(p.name),
		TokensUsed: 10,
	}, nil
}

// orientMockAPI provides a fixed DollState for tests.
type orientMockAPI struct {
	state *dollstate.DollState
}

func (m *orientMockAPI) Inference() inference.Provider { return nil }
func (m *orientMockAPI) State() *dollstate.DollState   { return m.state }
func (m *orientMockAPI) Save() error                    { return nil }

func TestBuildOrientPrompt_IncludesState(t *testing.T) {
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
				{ID: "g1", Name: "Phase 2", State: "active"},
			},
		},
	}
	prompt := buildOrientPrompt(state, events.TypeMessage, "Hello there")

	wants := []string{"Spark", "A curious little AI girl", "Zero", "Learn", "Phase 2", "ORIENTATION", "Hello there"}
	for _, w := range wants {
		if !strings.Contains(prompt, w) {
			t.Errorf("expected prompt to contain %q", w)
		}
	}
}

func TestBuildOrientPrompt_EmptyState(t *testing.T) {
	state := &dollstate.DollState{}
	prompt := buildOrientPrompt(state, events.TypePresence, "")
	if !strings.Contains(prompt, "ORIENTATION") {
		t.Error("empty state prompt should still contain orientation instruction")
	}
}

func TestBuildOrientPrompt_DrivesAndGoalsBothAppear(t *testing.T) {
	state := &dollstate.DollState{
		Drives: dollstate.Drives{
			Items: []dollstate.DriveItem{
				{ID: "d1", Name: "Explore"},
			},
		},
		Goals: dollstate.Goals{
			Items: []dollstate.GoalItem{
				{ID: "g1", Name: "Complete Phase 2", State: "active"},
			},
		},
	}
	prompt := buildOrientPrompt(state, events.TypeCommand, "/status")
	if !strings.Contains(prompt, "Explore") {
		t.Error("expected prompt to contain drive name")
	}
	if !strings.Contains(prompt, "Complete Phase 2") {
		t.Error("expected prompt to contain goal name")
	}
	if !strings.Contains(prompt, "[active]") {
		t.Error("expected prompt to contain goal state")
	}
}

func TestBuildOrientPrompt_RecentMemories(t *testing.T) {
	items := make([]dollstate.MemoryItem, 6)
	for i := 0; i < 6; i++ {
		items[i] = dollstate.MemoryItem{
			ID:      string(rune('a' + i)),
			Kind:    dollstate.KindHumanMessage,
			Content: "message " + string(rune('0'+i)),
		}
	}
	state := &dollstate.DollState{
		Memories: dollstate.Memories{Items: items},
	}
	prompt := buildOrientPrompt(state, events.TypeMessage, "test")
	// Should include the last 5 (so message 0 should NOT appear, but 1-5 should)
	if strings.Contains(prompt, "message 0") {
		t.Error("expected only the last 5 memory items, message 0 should be excluded")
	}
	for i := 1; i <= 5; i++ {
		want := "message " + string(rune('0'+i))
		if !strings.Contains(prompt, want) {
			t.Errorf("expected prompt to contain %q", want)
		}
	}
}

func TestParseOrientation_Valid(t *testing.T) {
	raw := `{"summary":"User is asking about the weather","matters":false,"reason":"Routine inquiry, no action needed"}`
	orient, err := parseOrientation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if orient.Summary != "User is asking about the weather" {
		t.Errorf("summary = %q", orient.Summary)
	}
	if orient.Matters {
		t.Error("expected matters=false")
	}
	if orient.Reason != "Routine inquiry, no action needed" {
		t.Errorf("reason = %q", orient.Reason)
	}
}

func TestParseOrientation_StripsMarkdownFences(t *testing.T) {
	raw := "```json\n{\"summary\":\"test\",\"matters\":true,\"reason\":\"important\"}\n```"
	orient, err := parseOrientation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if orient.Summary != "test" || !orient.Matters || orient.Reason != "important" {
		t.Error("parsed orientation fields incorrect after stripping fences")
	}
}

func TestParseOrientation_InvalidJSON(t *testing.T) {
	_, err := parseOrientation("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseOrientation_MissingSummary(t *testing.T) {
	_, err := parseOrientation(`{"matters":true,"reason":"test"}`)
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestParseOrientation_MissingReason(t *testing.T) {
	_, err := parseOrientation(`{"summary":"test","matters":true}`)
	if err == nil {
		t.Fatal("expected error for missing reason")
	}
}

func TestOrient_ReturnsOrientation(t *testing.T) {
	json := `{"summary":"User said hello","matters":false,"reason":"Greeting, no planning needed"}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	sched := New(provider, log, &orientMockAPI{
		state: &dollstate.DollState{
			Identity: dollstate.Identity{CanonicalName: "Spark"},
		},
	})

	orient, err := sched.Orient(context.Background(), events.TypeMessage, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if orient.Summary != "User said hello" {
		t.Errorf("summary = %q", orient.Summary)
	}
	if orient.Matters {
		t.Error("expected matters=false")
	}
	if orient.Reason != "Greeting, no planning needed" {
		t.Errorf("reason = %q", orient.Reason)
	}
	// Verify the inference request semantics
	if provider.lastReq == nil {
		t.Fatal("no inference request captured")
	}
	if len(provider.lastReq.Messages) == 0 {
		t.Fatal("no messages in request")
	}
	if provider.lastReq.Purpose != inference.PurposeOrient {
		t.Errorf("request.Purpose = %q, want %q", provider.lastReq.Purpose, inference.PurposeOrient)
	}
	sysMsg := provider.lastReq.Messages[0].Content
	if !strings.Contains(sysMsg, "Spark") {
		t.Error("orientation prompt should contain identity")
	}
}

func TestOrient_ContextIncludesState(t *testing.T) {
	json := `{"summary":"status check","matters":true,"reason":"Request requires planning"}`
	provider := &orientTestProvider{response: json}
	log := logger.New(logger.ErrorLevel, nil)
	state := &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
		Soul:     dollstate.Soul{Content: "A curious AI."},
		Owner:    dollstate.Owner{Name: "Master Zero"},
		Drives: dollstate.Drives{
			Items: []dollstate.DriveItem{
				{
					ID:          "drive-continuity-001",
					Name:        "maintain continuity",
					Description: "Spark must maintain her identity, drives, and goals across persistence restarts.",
				},
			},
		},
		Goals: dollstate.Goals{
			Items: []dollstate.GoalItem{
				{
					ID:          "goal-continuity-001",
					Name:        "prove goal continuity across persistence restart",
					Description: "A persistence restart must not lose Spark's goals.",
					State:       dollstate.GoalStateActive,
					DriveID:     "drive-continuity-001",
				},
			},
		},
	}
	sched := New(provider, log, &orientMockAPI{state: state})

	_, err := sched.Orient(context.Background(), events.TypeCommand, "/status")
	if err != nil {
		t.Fatal(err)
	}
	if provider.lastReq == nil {
		t.Fatal("no inference request captured")
	}
	if provider.lastReq.Purpose != inference.PurposeOrient {
		t.Errorf("request.Purpose = %q, want %q", provider.lastReq.Purpose, inference.PurposeOrient)
	}
	sysMsg := provider.lastReq.Messages[0].Content
	for _, want := range []string{
		"Spark", "A curious AI", "Master Zero",
		"maintain continuity",
		"prove goal continuity across persistence restart",
		"Spark must maintain her identity, drives, and goals",
		"active",
		"/status", "ORIENTATION",
	} {
		if !strings.Contains(sysMsg, want) {
			t.Errorf("orientation context should contain %q", want)
		}
	}
}

func TestOrient_MattersDecisions(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		matters bool
	}{
		{"matters true", `{"summary":"big","matters":true,"reason":"important"}`, true},
		{"matters false", `{"summary":"small","matters":false,"reason":"trivial"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &orientTestProvider{response: tt.json}
			log := logger.New(logger.ErrorLevel, nil)
			sched := New(provider, log, &orientMockAPI{
				state: &dollstate.DollState{
					Identity: dollstate.Identity{CanonicalName: "Spark"},
				},
			})
			orient, err := sched.Orient(context.Background(), events.TypeMessage, "test")
			if err != nil {
				t.Fatal(err)
			}
			if orient.Matters != tt.matters {
				t.Errorf("expected matters=%v, got %v", tt.matters, orient.Matters)
			}
		})
	}
}

func TestOrient_InvalidOutput(t *testing.T) {
	provider := &orientTestProvider{response: "this is not json"}
	log := logger.New(logger.ErrorLevel, nil)
	sched := New(provider, log, &orientMockAPI{
		state: &dollstate.DollState{
			Identity: dollstate.Identity{CanonicalName: "Spark"},
		},
	})
	_, err := sched.Orient(context.Background(), events.TypeMessage, "test")
	if err == nil {
		t.Fatal("expected error for invalid model output")
	}
	t.Logf("got expected error: %v", err)
}