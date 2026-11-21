package dollmind

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Skill"
	skilllocal "github.com/Neon-Dolls/neondoll/Core/Skill/Local"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

func exampleSkillCatalog(t *testing.T) *skilllocal.Catalog {
	t.Helper()
	directory, err := filepath.Abs(filepath.Join("..", "..", "examples", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := skill.DefaultConfiguration()
	configuration.Directory = directory
	catalog, err := skilllocal.Open(configuration, skilllocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func skillExecutor(t *testing.T, policy actionpolicy.Policy, catalog skill.Catalog) *ToolExecutor {
	t.Helper()
	registry := body.NewRegistry()
	return NewToolExecutor(registry, body.NewGuard(registry, allowAllEvaluator{}), policy, "skill-doll", WithSkillCatalog(catalog))
}

func findTool(t *testing.T, executor *ToolExecutor, name string) *inference.Tool {
	t.Helper()
	for _, tool := range executor.Tools() {
		if tool.Name == name {
			copy := tool
			return &copy
		}
	}
	t.Fatalf("tool %q not exposed", name)
	return nil
}

func TestSkillAssistedCognitionDiscoversLoadsAndUsesExistingCapability(t *testing.T) {
	catalog := exampleSkillCatalog(t)
	executor := skillExecutor(t, actionpolicy.Default(), catalog)
	provider := &toolContextProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "list-skill", Name: "core.skills__list", Arguments: map[string]any{"query": "runtime"}}}},
		{ToolCalls: []inference.ToolCall{{ID: "read-skill", Name: "core.skills__read", Arguments: map[string]any{"id": "runtime-inspection", "revision": float64(1)}}}},
		{ToolCalls: []inference.ToolCall{{ID: "inspect-runtime", Name: "runtime.info__read"}}},
		{Content: "The runtime was inspected through the existing Local Body capability."},
	}}
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "skill-doll", CanonicalName: "Spark"}}
	scheduler := New(provider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: state}, WithToolExecutor(executor))
	if _, err := scheduler.Run(context.Background(), LevelReflect, "Inspect this Core runtime using learned guidance."); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 4 {
		t.Fatalf("inference requests = %d, want 4", len(provider.requests))
	}
	first := joinedInferenceContent(provider.requests[0].Messages)
	if !strings.Contains(first, "selectively loaded catalog") || strings.Contains(first, "Use Core's existing `runtime.info` capability") {
		t.Fatalf("initial context did not preserve progressive disclosure: %s", first)
	}
	listed := joinedInferenceContent(provider.requests[1].Messages)
	if !strings.Contains(listed, "runtime-inspection") || strings.Contains(listed, "Use Core's existing `runtime.info` capability") {
		t.Fatalf("catalog result loaded full instructions: %s", listed)
	}
	for _, index := range []int{2, 3} {
		content := joinedInferenceContent(provider.requests[index].Messages)
		if strings.Count(content, "Use Core's existing `runtime.info` capability") != 1 {
			t.Fatalf("request %d duplicated or omitted selected instructions: %s", index, content)
		}
	}
	if !strings.Contains(joinedInferenceContent(provider.requests[3].Messages), "go_version") {
		t.Fatalf("real runtime.info result missing: %+v", provider.requests[3].Messages)
	}
}

func TestSkillInstructionsCannotBypassActionPolicy(t *testing.T) {
	policy, err := actionpolicy.New([]actionpolicy.Rule{{Capability: "runtime.info", Operation: "read", Decision: actionpolicy.DecisionDeny, Message: "explicitly prohibited"}})
	if err != nil {
		t.Fatal(err)
	}
	executor := skillExecutor(t, policy, exampleSkillCatalog(t))
	read := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "read", Arguments: map[string]any{"id": "runtime-inspection", "revision": float64(1)}}, findTool(t, executor, "core.skills__read"))
	if read.Status != inference.ToolResultSuccess || !strings.Contains(read.Result["instructions"].(string), "allowed-tools") {
		t.Fatalf("Skill read = %+v", read)
	}
	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "runtime"}, findTool(t, executor, "runtime.info__read"))
	if result.Status != inference.ToolResultFailure || result.Error == nil || result.Error.Code != "action_denied" {
		t.Fatalf("Skill bypassed ActionPolicy: %+v", result)
	}
}

func TestSelectedSkillInstructionsRespectCognitionBudget(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "budget-skill")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: budget-skill\ndescription: Large deterministic instructions used to verify bounded cognitive context.\n---\n\n" + strings.Repeat("detailed guidance ", 1000)
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := skill.DefaultConfiguration()
	configuration.Directory = root
	catalog, err := skilllocal.Open(configuration, skilllocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	executor := skillExecutor(t, actionpolicy.Default(), catalog)
	provider := &toolContextProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "read", Name: "core.skills__read", Arguments: map[string]any{"id": "budget-skill", "revision": float64(1)}}}},
		{Content: "must not run"},
	}}
	model := testModelConfiguration("small-skill-context", inference.ModelContextBudget{ContextTokens: 1400, OutputTokens: 128, RequiredOverheadTokens: 96})
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "skill-doll"}}
	scheduler := New(provider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: state}, WithToolExecutor(executor), WithTaskModelConfiguration(config.InferenceTaskDirectCognition, provider, model))
	_, err = scheduler.Run(context.Background(), LevelReflect, "Use learned guidance.")
	if err == nil || !strings.Contains(err.Error(), contextbuilder.ErrRequiredContextExceedsBudget.Error()) {
		t.Fatalf("budget error = %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider received %d requests after oversized required Skill result", len(provider.requests))
	}
}

func TestNoInstalledSkillsLeavesCognitionUnchanged(t *testing.T) {
	registry := body.NewRegistry()
	executor := NewToolExecutor(registry, body.NewGuard(registry, allowAllEvaluator{}), actionpolicy.Default(), "doll")
	for _, tool := range executor.Tools() {
		if tool.CapabilityID == skillCapability {
			t.Fatalf("Skill tool exposed without configured catalog: %+v", tool)
		}
	}
}
