package interaction

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/DollMind"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type sessionContextProvider struct {
	mu        sync.Mutex
	responses []string
	requests  []inference.Request
	err       error
}

type blockingSessionProvider struct {
	entered chan inference.Request
	release chan struct{}
}

type interactionToolProvider struct {
	responses []*inference.Response
	requests  []inference.Request
}

type sizedInteractionTools struct{ output string }

func (tools sizedInteractionTools) Tools() []inference.Tool {
	return []inference.Tool{
		{Name: "runtime.info__read", CapabilityID: "runtime.info", OperationID: "read", TargetBodyID: string(body.LocalBodyID)},
		{Name: "runtime.info__list", CapabilityID: "runtime.info", OperationID: "list", TargetBodyID: string(body.LocalBodyID)},
	}
}

func (tools sizedInteractionTools) ExecuteCall(_ context.Context, call inference.ToolCall, _ *inference.Tool) inference.ToolResult {
	return inference.ToolResult{
		ToolCallID: call.ID, ExecutionID: "exec_" + call.ID, Status: inference.ToolResultSuccess,
		Result: map[string]any{"output": tools.output},
	}
}

func (provider *interactionToolProvider) ID() inference.ProviderID { return "interaction-tools" }
func (provider *interactionToolProvider) Infer(_ context.Context, request inference.Request) (*inference.Response, error) {
	provider.requests = append(provider.requests, request)
	return provider.responses[len(provider.requests)-1], nil
}

func (provider *blockingSessionProvider) ID() inference.ProviderID { return "blocking-session" }
func (provider *blockingSessionProvider) Infer(ctx context.Context, request inference.Request) (*inference.Response, error) {
	select {
	case provider.entered <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-provider.release:
		return &inference.Response{Content: "answer", ProviderID: provider.ID()}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (provider *sessionContextProvider) ID() inference.ProviderID { return "session-context" }
func (provider *sessionContextProvider) Infer(_ context.Context, request inference.Request) (*inference.Response, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.requests = append(provider.requests, request)
	if provider.err != nil {
		return nil, provider.err
	}
	index := len(provider.requests) - 1
	content := "response"
	if index < len(provider.responses) {
		content = provider.responses[index]
	}
	return &inference.Response{Content: content, ProviderID: provider.ID()}, nil
}

func TestSessionInteractionBuildsFreshMultiTurnContext(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "session-doll", "SessionDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-one")
	provider := &sessionContextProvider{responses: []string{"answer one", "answer two"}}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))

	for _, text := range []string{"question one", "question two"} {
		event := newResponse("session-doll", text)
		if _, err := service.HandleInteraction(context.Background(), created, &event); err != nil {
			t.Fatalf("HandleInteraction(%q): %v", text, err)
		}
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	second := messageContents(provider.requests[1].Messages)
	for _, expected := range []string{"question one", "answer one", "question two"} {
		if !containsString(second, expected) {
			t.Errorf("second context missing %q: %v", expected, second)
		}
	}
	if indexOf(second, "question one") >= indexOf(second, "answer one") || indexOf(second, "answer one") >= indexOf(second, "question two") {
		t.Fatalf("conversation order = %v", second)
	}
	stored, err := history.ListSessionMessages(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 4 || stored[0].Content != "question one" || stored[3].Content != "answer two" {
		t.Fatalf("stored history = %+v", stored)
	}
	if stored[0].TurnID == "" || stored[0].TurnID != stored[1].TurnID || stored[2].TurnID == "" || stored[2].TurnID != stored[3].TurnID || stored[0].TurnID == stored[2].TurnID {
		t.Fatalf("turn IDs do not identify independent atomic pairs: %+v", stored)
	}
	if provider.requests[0].TurnID != stored[0].TurnID || provider.requests[1].TurnID != stored[2].TurnID {
		t.Fatalf("turn ID was not stable across inference and history: requests=%+v history=%+v", provider.requests, stored)
	}
}

func TestSessionInteractionLongHistoryUsesBoundedSuffix(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "long-doll", "LongDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-long")
	large := strings.Repeat("history ", 900)
	for index := 0; index < 8; index++ {
		turnID := session.NewTurnID()
		_, err := history.AppendSessionMessages(context.Background(), created.ID, []contextbuilder.NewHistoryMessage{{
			TurnID: turnID, Role: contextbuilder.RoleUser, Content: large + string(rune('A'+index)),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	provider := &sessionContextProvider{}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))
	event := newResponse("long-doll", "required latest input")
	if _, err := service.HandleInteraction(context.Background(), created, &event); err != nil {
		t.Fatal(err)
	}
	request := provider.requests[0]
	if tokens := approximateRequestTokens(request.Messages); tokens > testInputTokenBudget {
		t.Fatalf("request tokens = %d, budget = %d", tokens, testInputTokenBudget)
	}
	contents := strings.Join(messageContents(request.Messages), "\n")
	if strings.Contains(contents, large+"A") || !strings.Contains(contents, "required latest input") || !strings.Contains(contents, large+"H") {
		t.Fatalf("window did not retain bounded suffix and current input")
	}
}

func TestSessionInteractionRejectsOversizedRequiredInput(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "oversized-doll", "OversizedDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-oversized")
	provider := &sessionContextProvider{}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))
	event := newResponse("oversized-doll", strings.Repeat("x", testInputTokenBudget*5))

	response, err := service.HandleInteraction(context.Background(), created, &event)
	if err != nil {
		t.Fatal(err)
	}
	if response.Type != events.TypeSystem || len(provider.requests) != 0 {
		t.Fatalf("oversized input response=%+v provider calls=%d", response, len(provider.requests))
	}
	stored, err := history.ListSessionMessages(context.Background(), created.ID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("oversized input persisted history: %+v, %v", stored, err)
	}
}

func TestSessionInteractionUsesConfiguredModelBudget(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "budget-doll", "BudgetDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-budget")
	turnID := session.NewTurnID()
	_, err := history.AppendSessionMessages(context.Background(), created.ID, []contextbuilder.NewHistoryMessage{
		{TurnID: turnID, Role: contextbuilder.RoleUser, Content: strings.Repeat("history ", 40)},
		{TurnID: turnID, Role: contextbuilder.RoleAssistant, Content: strings.Repeat("answer ", 40)},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &sessionContextProvider{}
	budget := inference.ModelContextBudget{ContextTokens: 80, OutputTokens: 20, RequiredOverheadTokens: 10}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard),
		WithModelConfiguration(config.ModelConfiguration{
			Name: "configured", Provider: "openai", Model: "configured-interaction-model",
			Endpoint: inference.DefaultBaseURL, Budget: budget,
		}))
	event := newResponse("budget-doll", "current input")
	if _, err := service.HandleInteraction(context.Background(), created, &event); err != nil {
		t.Fatal(err)
	}
	request := provider.requests[0]
	if request.MaxTokens != 20 {
		t.Fatalf("MaxTokens = %d, want 20", request.MaxTokens)
	}
	if request.Model != "configured-interaction-model" {
		t.Fatalf("Model = %q, want configured-interaction-model", request.Model)
	}
	if tokens := approximateRequestTokens(request.Messages); tokens > 50 {
		t.Fatalf("input tokens = %d, want at most 50", tokens)
	}
}

func TestSessionInteractionRejectsInvalidModelBudget(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "invalid-budget-doll", "BudgetDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-budget")
	provider := &sessionContextProvider{}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(config.ModelConfiguration{
		Name: "invalid", Provider: "openai", Model: "invalid-model", Endpoint: inference.DefaultBaseURL,
		Budget: inference.ModelContextBudget{ContextTokens: 512, OutputTokens: 512},
	}))
	event := newResponse("invalid-budget-doll", "current input")
	response, err := service.HandleInteraction(context.Background(), created, &event)
	if err != nil || response == nil || response.Type != events.TypeSystem {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("provider requests = %d, want 0", len(provider.requests))
	}
	stored, err := history.ListSessionMessages(context.Background(), created.ID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("history=%+v error=%v", stored, err)
	}
}

func TestSessionInteractionIsolatesSessionHistories(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "isolated-doll", "IsolatedDoll")
	sessions, history := interactionSessionStores(t, store)
	first := createInteractionSession(t, sessions, "body-isolated")
	second := createInteractionSession(t, sessions, "body-isolated")
	_, _ = history.AppendSessionMessages(context.Background(), first.ID, []contextbuilder.NewHistoryMessage{{TurnID: session.NewTurnID(), Role: contextbuilder.RoleUser, Content: "first secret"}})
	_, _ = history.AppendSessionMessages(context.Background(), second.ID, []contextbuilder.NewHistoryMessage{{TurnID: session.NewTurnID(), Role: contextbuilder.RoleUser, Content: "second secret"}})
	provider := &sessionContextProvider{}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))
	event := newResponse("isolated-doll", "continue first")
	if _, err := service.HandleInteraction(context.Background(), first, &event); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(messageContents(provider.requests[0].Messages), "\n")
	if !strings.Contains(content, "first secret") || strings.Contains(content, "second secret") {
		t.Fatalf("session context crossed isolation boundary: %q", content)
	}
}

func TestSessionHistoryReconstructsAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedDoll(t, store, "restart-doll", "RestartDoll")
	sessions, _ := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-restart")
	firstProvider := &sessionContextProvider{responses: []string{"before restart"}}
	firstService := New(store, firstProvider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))
	firstEvent := newResponse("restart-doll", "remember this")
	if _, err := firstService.HandleInteraction(context.Background(), created, &firstEvent); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedSessions, _ := interactionSessionStores(t, reopened)
	restored, err := restartedSessions.Lookup(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondProvider := &sessionContextProvider{}
	secondService := New(reopened, secondProvider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))
	secondEvent := newResponse("restart-doll", "after restart")
	if _, err := secondService.HandleInteraction(context.Background(), restored, &secondEvent); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(messageContents(secondProvider.requests[0].Messages), "\n")
	if !strings.Contains(content, "remember this") || !strings.Contains(content, "before restart") || !strings.Contains(content, "after restart") {
		t.Fatalf("reconstructed context = %q", content)
	}
}

func TestInteractionUsesUnifiedMultiStepExecutionAndPersistsOneTurn(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "tool-session-doll", "ToolSessionDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-tools")
	registry := body.NewRegistry()
	local, _ := registry.Local()
	guard := body.NewGuard(registry, body.NewRuleEvaluator(
		body.AuthorityRule{Doll: "tool-session-doll", Body: body.LocalBodyID, Capability: "runtime.info", Operation: "read", Decision: body.AuthorityAllow},
		body.AuthorityRule{Doll: "tool-session-doll", Body: body.LocalBodyID, Capability: "runtime.info", Operation: "list", Decision: body.AuthorityAllow},
	))
	provider := &interactionToolProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "interaction_A", Name: "runtime.info__read"}}},
		{ToolCalls: []inference.ToolCall{{ID: "interaction_B", Name: "runtime.info__list"}}},
		{Content: "interaction tools complete"},
	}}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard),
		WithModelConfiguration(testModelConfiguration),
		WithToolExecutor(dollmind.NewToolExecutor(guard, local, "tool-session-doll")),
	)
	event := newResponse("tool-session-doll", "inspect runtime")
	response, err := service.HandleInteraction(context.Background(), created, &event)
	if err != nil || response == nil || response.Type != events.TypeMessage {
		t.Fatalf("HandleInteraction response=%+v err=%v", response, err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("inference requests = %d, want 3", len(provider.requests))
	}
	turnID := provider.requests[0].TurnID
	for index, request := range provider.requests {
		if request.TurnID == "" || request.TurnID != turnID {
			t.Errorf("iteration %d TurnID = %q, want %q", index, request.TurnID, turnID)
		}
		if request.Model != testModelConfiguration.Model || request.MaxTokens != testModelConfiguration.Budget.OutputTokens {
			t.Errorf("iteration %d model/budget = %q/%d", index, request.Model, request.MaxTokens)
		}
		if tokens := approximateRequestTokens(request.Messages); tokens > testInputTokenBudget {
			t.Errorf("iteration %d input tokens = %d, budget = %d", index, tokens, testInputTokenBudget)
		}
		if count := strings.Count(strings.Join(messageContents(request.Messages), "\n"), "inspect runtime"); count != 1 {
			t.Errorf("iteration %d current-input count = %d, want 1", index, count)
		}
	}
	if got := interactionMessageToolResults(provider.requests[1].Messages); len(got) != 1 || got[0].ToolCallID != "interaction_A" {
		t.Fatalf("second iteration results = %+v", got)
	}
	if got := interactionMessageToolResults(provider.requests[2].Messages); len(got) != 2 || got[0].ToolCallID != "interaction_A" || got[1].ToolCallID != "interaction_B" {
		t.Fatalf("third iteration results = %+v", got)
	}
	stored, err := history.ListSessionMessages(context.Background(), created.ID)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored history=%+v err=%v", stored, err)
	}
	if stored[0].TurnID != turnID || stored[1].TurnID != turnID || stored[0].Role != contextbuilder.RoleUser || stored[1].Role != contextbuilder.RoleAssistant {
		t.Fatalf("durable turn = %+v", stored)
	}
	for _, message := range stored {
		if message.Role == contextbuilder.RoleTool {
			t.Fatalf("ephemeral tool exchange was persisted: %+v", stored)
		}
	}
}

func TestInteractionToolWindowOmitsConsumedExchangeButKeepsUnresolved(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "window-tool-doll", "WindowToolDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-window-tools")
	tools := sizedInteractionTools{output: strings.Repeat("bounded-tool-output-", 12)}
	provider := &interactionToolProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "call_A", Name: "runtime.info__read"}}},
		{ToolCalls: []inference.ToolCall{{ID: "call_B", Name: "runtime.info__list"}}},
		{Content: "bounded tools complete"},
	}}
	state, err := store.LoadDoll(context.Background(), "window-tool-doll")
	if err != nil {
		t.Fatal(err)
	}
	estimator := contextbuilder.ApproximateEstimator{}
	input := "inspect within a constrained window"
	baseCost := estimator.EstimateTokens(contextbuilder.Message{Role: contextbuilder.RoleUser, Content: input})
	for _, instruction := range append(interactionInstructions(state), inference.FormatToolsForPrompt(tools.Tools())) {
		baseCost += estimator.EstimateTokens(contextbuilder.Message{Role: contextbuilder.RoleSystem, Content: instruction})
	}
	exchangeContent, err := cognitiveexecution.FormatToolExchange(cognitiveexecution.ToolExchange{
		Call: inference.ToolCall{ID: "call_A", Name: "runtime.info__read"},
		Result: inference.ToolResult{ToolCallID: "call_A", ExecutionID: "exec_call_A", Status: inference.ToolResultSuccess,
			Result: map[string]any{"output": tools.output}},
	})
	if err != nil {
		t.Fatal(err)
	}
	exchangeCost := estimator.EstimateTokens(contextbuilder.Message{Role: contextbuilder.RoleTool, Content: exchangeContent})
	inputBudget := baseCost + exchangeCost + 4
	model := config.ModelConfiguration{
		Name: "constrained", Provider: "openai", Model: "constrained-interaction", Endpoint: inference.DefaultBaseURL,
		Budget: inference.ModelContextBudget{ContextTokens: inputBudget + 80, OutputTokens: 60, RequiredOverheadTokens: 20},
	}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard),
		WithModelConfiguration(model), WithToolExecutor(tools))
	event := newResponse("window-tool-doll", input)
	response, err := service.HandleInteraction(context.Background(), created, &event)
	if err != nil || response == nil || response.Type != events.TypeMessage {
		t.Fatalf("HandleInteraction response=%+v err=%v", response, err)
	}
	if got := interactionMessageToolResults(provider.requests[1].Messages); len(got) != 1 || got[0].ToolCallID != "call_A" {
		t.Fatalf("required first exchange = %+v", got)
	}
	if got := interactionMessageToolResults(provider.requests[2].Messages); len(got) != 1 || got[0].ToolCallID != "call_B" {
		t.Fatalf("bounded final exchanges = %+v, want only unresolved call_B", got)
	}
	stored, err := history.ListSessionMessages(context.Background(), created.ID)
	if err != nil || len(stored) != 2 || stored[0].TurnID != stored[1].TurnID {
		t.Fatalf("durable turn=%+v err=%v", stored, err)
	}
}

func TestInteractionUnifiedExecutionKeepsUnauthorizedToolsDenied(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "denied-tool-doll", "DeniedToolDoll")
	sessions, _ := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-denied-tools")
	registry := body.NewRegistry()
	if err := registry.RegisterCapability(body.LocalBodyID, body.Capability{ID: "private.info", Operations: []string{"read"}, Available: true}); err != nil {
		t.Fatal(err)
	}
	local, _ := registry.Local()
	guard := body.NewGuard(registry, body.NewRuleEvaluator())
	provider := &interactionToolProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "denied_A", Name: "private.info__read"}}},
		{Content: "denial handled"},
	}}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard),
		WithModelConfiguration(testModelConfiguration),
		WithToolExecutor(dollmind.NewToolExecutor(guard, local, "denied-tool-doll")),
	)
	event := newResponse("denied-tool-doll", "read private info")
	if _, err := service.HandleInteraction(context.Background(), created, &event); err != nil {
		t.Fatal(err)
	}
	results := interactionMessageToolResults(provider.requests[1].Messages)
	if len(results) != 1 || results[0].Status != inference.ToolResultFailure || results[0].Error == nil || results[0].Error.Code != "denied" {
		t.Fatalf("unauthorized result = %+v", results)
	}
}

func interactionMessageToolResults(messages []inference.Message) []inference.ToolResult {
	var results []inference.ToolResult
	for _, message := range messages {
		if message.ToolResult != nil {
			results = append(results, *message.ToolResult)
		}
	}
	return results
}

func TestFailedSessionInferenceDoesNotPersistTurn(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "failed-session-doll", "FailedSessionDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-failure")
	provider := &sessionContextProvider{err: errors.New("inference failed")}
	var diagnostics bytes.Buffer
	service := New(store, provider, logger.New(logger.ErrorLevel, &diagnostics), WithModelConfiguration(testModelConfiguration))
	event := newResponse("failed-session-doll", "do not fabricate")
	response, err := service.HandleInteraction(context.Background(), created, &event)
	if err != nil {
		t.Fatal(err)
	}
	if response.CorrelationID != event.ID {
		t.Fatalf("failure correlation = %q, want event %q", response.CorrelationID, event.ID)
	}
	turnID := provider.requests[0].TurnID
	if turnID == "" || string(turnID) == event.ID || string(turnID) == string(created.ID) {
		t.Fatalf("failure identities are not independent: turn=%q event=%q session=%q", turnID, event.ID, created.ID)
	}
	if !strings.Contains(diagnostics.String(), `"turn_id":"`+string(turnID)+`"`) || !strings.Contains(diagnostics.String(), `"event_correlation_id":"`+event.ID+`"`) {
		t.Fatalf("failure diagnostics lack correlation identities: %s", diagnostics.String())
	}
	stored, err := history.ListSessionMessages(context.Background(), created.ID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("failed inference history = %+v, %v", stored, err)
	}
}

func TestSameSessionInteractionsAreSerializedAcrossHistoryAppend(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "ordered-doll", "OrderedDoll")
	sessions, _ := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-ordered")
	provider := &blockingSessionProvider{entered: make(chan inference.Request, 2), release: make(chan struct{}, 2)}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))

	done := make(chan error, 2)
	first := newResponse("ordered-doll", "first")
	second := newResponse("ordered-doll", "second")
	go func() { _, err := service.HandleInteraction(context.Background(), created, &first); done <- err }()
	<-provider.entered
	go func() { _, err := service.HandleInteraction(context.Background(), created, &second); done <- err }()
	waitForSessionLockReferences(t, service, string(created.ID), 2)
	select {
	case <-provider.entered:
		t.Fatal("second inference entered before first interaction appended history")
	default:
	}
	provider.release <- struct{}{}
	secondRequest := <-provider.entered
	contents := messageContents(secondRequest.Messages)
	if !containsString(contents, "first") || !containsString(contents, "answer") || !containsString(contents, "second") {
		t.Fatalf("second request does not contain completed first turn: %v", contents)
	}
	provider.release <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	service.sessions.mu.Lock()
	defer service.sessions.mu.Unlock()
	if len(service.sessions.entries) != 0 {
		t.Fatalf("session lock entries retained: %d", len(service.sessions.entries))
	}
}

func waitForSessionLockReferences(t *testing.T, service *Service, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.sessions.mu.Lock()
		entry := service.sessions.entries[key]
		got := 0
		if entry != nil {
			got = entry.refs
		}
		service.sessions.mu.Unlock()
		if got == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("session lock references did not reach %d", want)
}

func TestDifferentSessionInteractionsRemainConcurrent(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "concurrent-doll", "ConcurrentDoll")
	sessions, _ := interactionSessionStores(t, store)
	firstSession := createInteractionSession(t, sessions, "body-concurrent")
	secondSession := createInteractionSession(t, sessions, "body-concurrent")
	provider := &blockingSessionProvider{entered: make(chan inference.Request, 2), release: make(chan struct{}, 2)}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))
	done := make(chan error, 2)

	for _, item := range []struct {
		resolved *session.InteractionSession
		text     string
	}{{firstSession, "first session"}, {secondSession, "second session"}} {
		item := item
		go func() {
			event := newResponse("concurrent-doll", item.text)
			_, err := service.HandleInteraction(context.Background(), item.resolved, &event)
			done <- err
		}()
	}
	requests := make([]inference.Request, 0, 2)
	for range 2 {
		select {
		case request := <-provider.entered:
			requests = append(requests, request)
		case <-time.After(time.Second):
			t.Fatal("different sessions did not reach inference concurrently")
		}
	}
	if requests[0].TurnID == "" || requests[1].TurnID == "" || requests[0].TurnID == requests[1].TurnID {
		t.Fatalf("concurrent sessions shared turn identity: %+v", requests)
	}
	for _, request := range requests {
		if string(request.TurnID) == string(firstSession.ID) || string(request.TurnID) == string(secondSession.ID) {
			t.Fatalf("turn ID aliases session ID: %+v", request)
		}
	}
	provider.release <- struct{}{}
	provider.release <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCanceledSessionWaiterReleasesLockBookkeeping(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "cancel-doll", "CancelDoll")
	sessions, _ := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-cancel")
	provider := &blockingSessionProvider{entered: make(chan inference.Request, 1), release: make(chan struct{}, 1)}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelConfiguration(testModelConfiguration))
	firstDone := make(chan error, 1)
	first := newResponse("cancel-doll", "first")
	go func() { _, err := service.HandleInteraction(context.Background(), created, &first); firstDone <- err }()
	<-provider.entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second := newResponse("cancel-doll", "second")
	if _, err := service.HandleInteraction(ctx, created, &second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	provider.release <- struct{}{}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	service.sessions.mu.Lock()
	defer service.sessions.mu.Unlock()
	if len(service.sessions.entries) != 0 {
		t.Fatalf("session lock entries retained: %d", len(service.sessions.entries))
	}
}

func interactionSessionStores(t *testing.T, store persistence.Store) (session.Service, contextbuilder.HistoryStore) {
	t.Helper()
	sessionStore, ok := store.(session.Store)
	if !ok {
		t.Fatal("store does not implement session.Store")
	}
	history, ok := store.(contextbuilder.HistoryStore)
	if !ok {
		t.Fatal("store does not implement HistoryStore")
	}
	return session.New(sessionStore), history
}

func createInteractionSession(t *testing.T, sessions session.Service, owner string) *session.InteractionSession {
	t.Helper()
	created, err := sessions.Create(context.Background(), session.CreateRequest{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: owner}})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func messageContents(messages []inference.Message) []string {
	contents := make([]string, len(messages))
	for index, message := range messages {
		contents[index] = message.Content
	}
	return contents
}

func approximateRequestTokens(messages []inference.Message) int {
	estimator := contextbuilder.ApproximateEstimator{}
	total := 0
	for _, message := range messages {
		total += estimator.EstimateTokens(contextbuilder.Message{Role: contextbuilder.Role(message.Role), Content: message.Content})
	}
	return total
}

func containsString(values []string, target string) bool { return indexOf(values, target) >= 0 }
func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}
