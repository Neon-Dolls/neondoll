package cognition

import (
	"context"
	"testing"

	"github.com/Neon-Dolls/neondoll/internal/inference"
	"github.com/Neon-Dolls/neondoll/internal/logger"
	"github.com/Neon-Dolls/neondoll/state"
)

type mockMindAPI struct {
	s *state.DollState
	c *state.CoreConfig
}

func (m *mockMindAPI) Inference() inference.Provider { return nil }
func (m *mockMindAPI) State() *state.DollState       { return m.s }
func (m *mockMindAPI) Config() *state.CoreConfig     { return m.c }

func TestNewCognition(t *testing.T) {
	provider := inference.NewMockProvider("mock", "Hello, doll!")
	log := logger.New(logger.DebugLevel, nil)
	mindAPI := &mockMindAPI{s: &state.DollState{}, c: &state.CoreConfig{}}
	sched := New(provider, log, mindAPI)
	if sched == nil {
		t.Fatal("New returned nil")
	}
}

func TestCognitionRunReflex(t *testing.T) {
	provider := inference.NewMockProvider("mock", "Reflex response")
	log := logger.New(logger.InfoLevel, nil)
	mindAPI := &mockMindAPI{s: &state.DollState{}, c: &state.CoreConfig{}}
	sched := New(provider, log, mindAPI)

	result, err := sched.Run(context.Background(), LevelReflex, "touch hot surface")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Level != LevelReflex {
		t.Errorf("expected LevelReflex, got %v", result.Level)
	}
	if len(result.Actions) == 0 {
		t.Fatal("expected at least one action")
	}
	if result.Actions[0].Type != "respond" {
		t.Errorf("expected action type respond, got %s", result.Actions[0].Type)
	}
	text, ok := result.Actions[0].Payload["text"].(string)
	if !ok || text != "Reflex response" {
		t.Errorf("expected payload text 'Reflex response', got %v", result.Actions[0].Payload["text"])
	}
}

func TestLevelString(t *testing.T) {
	tests := []struct {
		l    Level
		want string
	}{
		{LevelReflex, "reflex"},
		{LevelOrient, "orient"},
		{LevelPlan, "plan"},
		{LevelReflect, "reflect"},
		{LevelDream, "dream"},
		{Level(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.l.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", tt.l, got, tt.want)
		}
	}
}