package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

func TestResolveTaskProviderUsesSelectedEndpoints(t *testing.T) {
	interactionCalls := 0
	orientCalls := 0
	interactionServer := modelServer(t, &interactionCalls)
	defer interactionServer.Close()
	orientServer := modelServer(t, &orientCalls)
	defer orientServer.Close()

	resolver, err := config.NewModelResolver(config.InferenceConfig{
		DefaultModel: "interaction",
		Models: map[string]config.ModelConfiguration{
			"interaction": runtimeModel("interaction-model", interactionServer.URL),
			"orient":      runtimeModel("orient-model", orientServer.URL),
		},
		Tasks: map[config.InferenceTask]string{config.InferenceTaskOrient: "orient"},
	})
	if err != nil {
		t.Fatal(err)
	}
	interactionModel, interactionProvider, err := resolveTaskProvider(resolver, config.InferenceTaskInteraction)
	if err != nil {
		t.Fatal(err)
	}
	orientModel, orientProvider, err := resolveTaskProvider(resolver, config.InferenceTaskOrient)
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []struct {
		model    config.ModelConfiguration
		provider inference.Provider
	}{{interactionModel, interactionProvider}, {orientModel, orientProvider}} {
		if _, err := selected.provider.Infer(context.Background(), inference.Request{
			Model: selected.model.Model, Messages: []inference.Message{{Role: "user", Content: "test"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if interactionCalls != 1 || orientCalls != 1 || interactionModel.Name != "interaction" || orientModel.Name != "orient" {
		t.Fatalf("routing: interaction calls=%d model=%q; orient calls=%d model=%q", interactionCalls, interactionModel.Name, orientCalls, orientModel.Name)
	}
}

func TestResolveTaskProviderRejectsUnsupportedProvider(t *testing.T) {
	model := runtimeModel("model", "http://localhost")
	model.Provider = "unsupported"
	resolver, err := config.NewModelResolver(config.InferenceConfig{
		DefaultModel: "model", Models: map[string]config.ModelConfiguration{"model": model},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveTaskProvider(resolver, config.InferenceTaskPlan); err == nil {
		t.Fatal("resolveTaskProvider accepted unsupported provider")
	}
}

func runtimeModel(model, endpoint string) config.ModelConfiguration {
	return config.ModelConfiguration{
		Provider: "openai", Model: model, Endpoint: endpoint,
		Budget: inference.ModelContextBudget{ContextTokens: 4096, OutputTokens: 512, RequiredOverheadTokens: 128},
	}
}

func modelServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		*calls++
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}}})
	}))
}
