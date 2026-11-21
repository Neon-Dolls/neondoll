package dollmind

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	selfstate "github.com/Neon-Dolls/neondoll/Core/Self"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type selfTestStore struct {
	mu    sync.Mutex
	value dollstate.Self
}

func (store *selfTestStore) LoadSelf(context.Context, string) (dollstate.Self, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.value, nil
}

func (store *selfTestStore) ReplaceSelf(_ context.Context, _ string, expected string, next dollstate.Self) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.value.RevisionID != expected {
		return false, nil
	}
	store.value = next
	return true, nil
}

func (store *selfTestStore) SaveDoll(_ context.Context, state *dollstate.DollState) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.value = state.Self
	return nil
}

type selfMindAPI struct {
	state   *dollstate.DollState
	manager *selfstate.Manager
}

func (mind *selfMindAPI) Inference() inference.Provider { return nil }
func (mind *selfMindAPI) State() *dollstate.DollState   { return mind.state }
func (mind *selfMindAPI) Save() error                   { return nil }
func (mind *selfMindAPI) CurrentSelf(ctx context.Context) (dollstate.Self, error) {
	return mind.manager.Current(ctx)
}

func selfExecutor(t *testing.T, maxTokens int) (*ToolExecutor, *selfstate.Manager) {
	t.Helper()
	store := &selfTestStore{}
	manager, err := selfstate.NewManager(store, "doll", maxTokens)
	if err != nil {
		t.Fatal(err)
	}
	registry := body.NewRegistry()
	executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{}), actionpolicy.Default(), "doll", WithSelfManager(manager))
	return executor, manager
}

func selfTool(t *testing.T, executor *ToolExecutor) *inference.Tool {
	t.Helper()
	for _, tool := range executor.Tools() {
		if tool.CapabilityID == selfCapability && tool.OperationID == selfOperationRevise {
			copy := tool
			return &copy
		}
	}
	t.Fatal("Self revision tool not exposed")
	return nil
}

func TestSelfRevisionToolCreatesCompleteBoundedReplacement(t *testing.T) {
	executor, manager := selfExecutor(t, 20)
	tool := selfTool(t, executor)
	first := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "self-1", Arguments: map[string]any{
		"expected_revision_id": "", "content": "# Me\n\nI value patient curiosity.",
	}}, tool)
	if first.Status != inference.ToolResultSuccess || first.Result["complete_replacement"] != true || first.Result["memory_created"] != false {
		t.Fatalf("first result = %+v", first)
	}
	current, _ := manager.Current(context.Background())
	if current.Revision != 1 || !strings.Contains(current.Content, "patient curiosity") {
		t.Fatalf("current Self = %+v", current)
	}
	stale := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "self-2", Arguments: map[string]any{
		"expected_revision_id": "", "content": "overwrite",
	}}, tool)
	if stale.Status != inference.ToolResultFailure || stale.Error == nil || stale.Error.Code != "self_stale_revision" {
		t.Fatalf("stale result = %+v", stale)
	}
	oversized := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "self-3", Arguments: map[string]any{
		"expected_revision_id": current.RevisionID, "content": strings.Repeat("x", 81),
	}}, tool)
	if oversized.Status != inference.ToolResultFailure || oversized.Error == nil || oversized.Error.Code != "self_too_large" {
		t.Fatalf("oversized result = %+v", oversized)
	}
}

func TestReflectionExplicitlyRevisesSelfAndNextIterationSeesIt(t *testing.T) {
	executor, manager := selfExecutor(t, selfstate.DefaultMaxTokens)
	provider := &toolContextProvider{responses: []*inference.Response{
		{
			ToolCalls: []inference.ToolCall{{
				ID:   "develop-self",
				Name: "core.self__revise",
				Arguments: map[string]any{
					"expected_revision_id": "",
					"content":              "# Self\n\nI learned to question first impressions.",
				},
			}},
		},
		{Content: "I will carry that understanding forward."},
	}}
	mind := &selfMindAPI{state: &dollstate.DollState{Identity: dollstate.Identity{DollID: "doll", CanonicalName: "Spark"}}, manager: manager}
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), mind, WithToolExecutor(executor))
	if _, err := scheduler.Reflect(context.Background(), reflectionSignal("self-reflection")); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 || !strings.Contains(joinedInferenceContent(provider.requests[1].Messages), "question first impressions") {
		t.Fatalf("next reflection context did not contain revised Self: %+v", provider.requests)
	}
}

func TestReflectionDoesNotReviseSelfWithoutExplicitToolCall(t *testing.T) {
	_, manager := selfExecutor(t, selfstate.DefaultMaxTokens)
	provider := &universalContextProvider{responses: []string{"I reconsidered this, but no Self change is warranted."}}
	mind := &selfMindAPI{state: &dollstate.DollState{Identity: dollstate.Identity{DollID: "doll"}}, manager: manager}
	scheduler := New(provider, logger.New(logger.ErrorLevel, nil), mind)
	if _, err := scheduler.Reflect(context.Background(), reflectionSignal("no-change")); err != nil {
		t.Fatal(err)
	}
	current, err := manager.Current(context.Background())
	if err != nil || current.Revision != 0 || current.Content != "" {
		t.Fatalf("implicit Self change: %+v, %v", current, err)
	}
}
