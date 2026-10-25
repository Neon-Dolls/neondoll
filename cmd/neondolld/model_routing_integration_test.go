package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Interaction"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type capturedChatRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	Messages  []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type routingEndpoint struct {
	server    *httptest.Server
	mu        sync.Mutex
	requests  []capturedChatRequest
	responses []string
}

func newRoutingEndpoint(t *testing.T, responses ...string) *routingEndpoint {
	t.Helper()
	endpoint := &routingEndpoint{responses: responses}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var captured capturedChatRequest
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		endpoint.mu.Lock()
		index := len(endpoint.requests)
		endpoint.requests = append(endpoint.requests, captured)
		content := "done"
		if index < len(endpoint.responses) {
			content = endpoint.responses[index]
		}
		endpoint.mu.Unlock()

		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func (endpoint *routingEndpoint) snapshot() []capturedChatRequest {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	return append([]capturedChatRequest(nil), endpoint.requests...)
}

func TestProductionTaskModelRoutingIntegration(t *testing.T) {
	interactionEndpoint := newRoutingEndpoint(t, "interaction response")
	orientEndpoint := newRoutingEndpoint(t, `{"summary":"pulse matters","matters":true,"reason":"routing proof"}`)
	planEndpoint := newRoutingEndpoint(t,
		"TOOL_CALL: runtime.info__read\nARGUMENTS:\n{}",
		"TOOL_CALL: runtime.info__list\nARGUMENTS:\n{}",
		`{"summary":"plan complete","proposed_action":"done","observations":["ok"],"should_reorient":false,"request_future_cognition":false}`,
	)
	directEndpoint := newRoutingEndpoint(t, "direct response")

	configuration := config.Defaults()
	configuration.Inference = config.InferenceConfig{
		DefaultModel: "default-direct",
		Models: map[string]config.ModelConfiguration{
			"default-direct": modelConfiguration("shared-model", directEndpoint.server.URL, 2048, 300, 100),
			"interaction":    modelConfiguration("shared-model", interactionEndpoint.server.URL, 1024, 128, 64),
			"orient":         modelConfiguration("orient-model", orientEndpoint.server.URL, 1200, 160, 80),
			"plan":           modelConfiguration("plan-model", planEndpoint.server.URL, 1600, 200, 100),
		},
		Tasks: map[config.InferenceTask]string{
			config.InferenceTaskInteraction: "interaction",
			config.InferenceTaskOrient:      "orient",
			config.InferenceTaskPlan:        "plan",
			// Direct cognition is intentionally unassigned and must use the default.
		},
	}
	configPath := filepath.Join(t.TempDir(), "neondoll.json")
	if err := config.Save(configPath, &configuration); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := config.NewModelResolver(loaded.Inference)
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
	planModel, planProvider, err := resolveTaskProvider(resolver, config.InferenceTaskPlan)
	if err != nil {
		t.Fatal(err)
	}
	directModel, directProvider, err := resolveTaskProvider(resolver, config.InferenceTaskDirectCognition)
	if err != nil {
		t.Fatal(err)
	}

	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	state := &dollstate.DollState{Version: dollstate.CurrentStateVersion}
	state.Identity = dollstate.Identity{DollID: "routing-doll", CanonicalName: "Routing Doll"}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	log := logger.New(logger.ErrorLevel, io.Discard)

	sessionStore, ok := store.(session.Store)
	if !ok {
		t.Fatal("persistence store does not implement session.Store")
	}
	sessions := session.New(sessionStore)
	owner := session.Owner{Kind: session.OwnerBody, InstanceID: "routing-body"}
	interactionSession, err := sessions.Create(context.Background(), session.CreateRequest{Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	interactionService := interaction.New(store, interactionProvider, log, interaction.WithModelConfiguration(interactionModel))
	router, err := interaction.NewRouter(sessions, interactionService)
	if err != nil {
		t.Fatal(err)
	}
	event := events.NewDollMessage("routing-event", "routing-doll", "hello")
	if _, err := router.RouteInteraction(context.Background(), interaction.RouteRequest{
		SessionID: interactionSession.ID, Owner: owner, Event: &event,
	}); err != nil {
		t.Fatal(err)
	}

	registry := body.NewRegistry()
	toolExecutor, err := newProductionToolExecutor(registry)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := dollmind.New(directProvider, log, &productionMindAPI{state: state, store: store, provider: directProvider},
		dollmind.WithModelConfiguration(directModel),
		dollmind.WithTaskModelConfiguration(config.InferenceTaskOrient, orientProvider, orientModel),
		dollmind.WithTaskModelConfiguration(config.InferenceTaskPlan, inference.NewToolEnabledProvider(planProvider), planModel),
		dollmind.WithToolExecutor(toolExecutor),
	)
	if err := scheduler.EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: 0.8, EffectivePressure: 0.7}); err != nil {
		t.Fatal(err)
	}
	if _, err := scheduler.Run(context.Background(), dollmind.LevelReflect, "direct thought"); err != nil {
		t.Fatal(err)
	}

	assertEndpointRequests(t, "interaction", interactionEndpoint.snapshot(), "shared-model", 128, 832, 1)
	assertEndpointRequests(t, "orient", orientEndpoint.snapshot(), "orient-model", 160, 960, 1)
	planRequests := planEndpoint.snapshot()
	assertEndpointRequests(t, "plan", planRequests, "plan-model", 200, 1300, 3)
	assertToolContinuation(t, planRequests[1], "tp_call_1", "runtime.info__read")
	assertToolContinuation(t, planRequests[2], "tp_call_1", "runtime.info__list")
	assertEndpointRequests(t, "direct/default", directEndpoint.snapshot(), "shared-model", 300, 1648, 1)
}

func TestProductionTaskModelRoutingFailsBeforeInference(t *testing.T) {
	endpoint := newRoutingEndpoint(t)
	configuration := config.Defaults()
	configuration.Inference = config.InferenceConfig{
		DefaultModel: "default",
		Models: map[string]config.ModelConfiguration{
			"default": modelConfiguration("default", endpoint.server.URL, 1024, 128, 64),
		},
		Tasks: map[config.InferenceTask]string{config.InferenceTaskPlan: "missing"},
	}
	path := filepath.Join(t.TempDir(), "invalid-reference.json")
	if err := config.Save(path, &configuration); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("configuration with unknown task model reference loaded successfully")
	}

	configuration.Inference.Tasks = nil
	model := configuration.Inference.Models["default"]
	model.Provider = "unsupported"
	configuration.Inference.Models["default"] = model
	path = filepath.Join(t.TempDir(), "unsupported-provider.json")
	if err := config.Save(path, &configuration); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := config.NewModelResolver(loaded.Inference)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveTaskProvider(resolver, config.InferenceTaskInteraction); err == nil {
		t.Fatal("unsupported provider reached runtime composition")
	}
	if got := len(endpoint.snapshot()); got != 0 {
		t.Fatalf("inference requests = %d, want 0", got)
	}
}

func modelConfiguration(model, endpoint string, contextTokens, outputTokens, overheadTokens int) config.ModelConfiguration {
	return config.ModelConfiguration{
		Provider: "openai", Model: model, Endpoint: endpoint,
		Budget: inference.ModelContextBudget{
			ContextTokens: contextTokens, OutputTokens: outputTokens, RequiredOverheadTokens: overheadTokens,
		},
	}
}

func assertEndpointRequests(t *testing.T, task string, requests []capturedChatRequest, model string, outputBudget, inputBudget, count int) {
	t.Helper()
	if len(requests) != count {
		t.Fatalf("%s requests = %d, want %d", task, len(requests), count)
	}
	estimator := contextbuilder.ApproximateEstimator{}
	for index, request := range requests {
		if request.Model != model || request.MaxTokens != outputBudget {
			t.Errorf("%s request %d: model=%q max_tokens=%d, want %q/%d", task, index, request.Model, request.MaxTokens, model, outputBudget)
		}
		tokens := 0
		for _, message := range request.Messages {
			tokens += estimator.EstimateTokens(contextbuilder.Message{Role: contextbuilder.Role(message.Role), Content: message.Content})
		}
		if tokens > inputBudget {
			t.Errorf("%s request %d input tokens = %d, want at most %d", task, index, tokens, inputBudget)
		}
	}
}

func assertToolContinuation(t *testing.T, request capturedChatRequest, callID, toolName string) {
	t.Helper()
	for _, message := range request.Messages {
		if strings.Contains(message.Content, "Tool result for call "+callID+": SUCCESS") && strings.Contains(message.Content, toolName) {
			return
		}
	}
	t.Fatalf("continuation request did not contain correlated result for %s/%s", callID, toolName)
}
