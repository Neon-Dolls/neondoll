package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

func validModel(provider, model, endpoint string, contextTokens int) ModelConfiguration {
	return ModelConfiguration{
		Provider: provider,
		Model:    model,
		Endpoint: endpoint,
		Budget: inference.ModelContextBudget{
			ContextTokens: contextTokens, OutputTokens: 1024, RequiredOverheadTokens: 256,
		},
	}
}

func TestLoadRejectsObsoleteFlatInferenceConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"inference":{"provider":"openai","base_url":"http://obsolete"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted obsolete flat inference configuration")
	}
}

func TestModelResolverDefaultsUnspecifiedContextCapacity(t *testing.T) {
	resolver, err := NewModelResolver(InferenceConfig{
		DefaultModel: "primary",
		Models: map[string]ModelConfiguration{
			"primary": validModel("openai", "local-model", "http://localhost:8080", 0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolver.Default()
	if resolved.Name != "primary" || resolved.Budget.ContextTokens != 32768 {
		t.Fatalf("resolved default = %+v", resolved)
	}
	if input, err := resolved.Budget.InputTokens(); err != nil || input != 31488 {
		t.Fatalf("input budget = %d, %v; want 31488", input, err)
	}
}

func TestModelResolverSupportsMultipleNamedConfigurations(t *testing.T) {
	resolver, err := NewModelResolver(InferenceConfig{
		DefaultModel: "balanced",
		Models: map[string]ModelConfiguration{
			"balanced": validModel("openai", "shared-model", "http://models.internal/v1", 16384),
			"compact":  validModel("openai", "shared-model", "http://models.internal/v1", 4096),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	balanced := resolver.Default()
	compact, err := resolver.Resolve("compact")
	if err != nil {
		t.Fatal(err)
	}
	if balanced.Name != "balanced" || compact.Name != "compact" || balanced.Model != compact.Model || balanced.Endpoint != compact.Endpoint {
		t.Fatalf("resolved models = balanced %+v, compact %+v", balanced, compact)
	}
	if balanced.Budget.ContextTokens != 16384 || compact.Budget.ContextTokens != 4096 {
		t.Fatalf("independent consumption limits lost: balanced=%+v compact=%+v", balanced.Budget, compact.Budget)
	}
}

func TestModelResolverRejectsUnknownReferences(t *testing.T) {
	_, err := NewModelResolver(InferenceConfig{
		DefaultModel: "missing",
		Models: map[string]ModelConfiguration{
			"known": validModel("openai", "model", "http://localhost", 4096),
		},
	})
	if !errors.Is(err, ErrUnknownModelReference) {
		t.Fatalf("NewModelResolver error = %v, want ErrUnknownModelReference", err)
	}

	resolver, err := NewModelResolver(InferenceConfig{
		DefaultModel: "known",
		Models: map[string]ModelConfiguration{
			"known": validModel("openai", "model", "http://localhost", 4096),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve("missing"); !errors.Is(err, ErrUnknownModelReference) {
		t.Fatalf("Resolve error = %v, want ErrUnknownModelReference", err)
	}
}

func TestModelResolverRejectsInvalidConfigurationsAndBudgets(t *testing.T) {
	tests := []struct {
		name  string
		model ModelConfiguration
	}{
		{name: "missing provider", model: validModel("", "model", "http://localhost", 4096)},
		{name: "missing model", model: validModel("openai", "", "http://localhost", 4096)},
		{name: "missing endpoint", model: validModel("openai", "model", "", 4096)},
		{name: "missing output reserve", model: ModelConfiguration{Provider: "openai", Model: "model", Endpoint: "http://localhost", Budget: inference.ModelContextBudget{ContextTokens: 4096}}},
		{name: "insufficient budget", model: ModelConfiguration{Provider: "openai", Model: "model", Endpoint: "http://localhost", Budget: inference.ModelContextBudget{ContextTokens: 1024, OutputTokens: 1024}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewModelResolver(InferenceConfig{DefaultModel: "model", Models: map[string]ModelConfiguration{"model": test.model}})
			if !errors.Is(err, ErrInvalidModelConfiguration) {
				t.Fatalf("error = %v, want ErrInvalidModelConfiguration", err)
			}
		})
	}
}
