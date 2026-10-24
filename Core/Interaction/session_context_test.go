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

	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))

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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(budget))
	event := newResponse("budget-doll", "current input")
	if _, err := service.HandleInteraction(context.Background(), created, &event); err != nil {
		t.Fatal(err)
	}
	request := provider.requests[0]
	if request.MaxTokens != 20 {
		t.Fatalf("MaxTokens = %d, want 20", request.MaxTokens)
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(inference.ModelContextBudget{
		ContextTokens: 512, OutputTokens: 512,
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))
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
	firstService := New(store, firstProvider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))
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
	secondService := New(reopened, secondProvider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))
	secondEvent := newResponse("restart-doll", "after restart")
	if _, err := secondService.HandleInteraction(context.Background(), restored, &secondEvent); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(messageContents(secondProvider.requests[0].Messages), "\n")
	if !strings.Contains(content, "remember this") || !strings.Contains(content, "before restart") || !strings.Contains(content, "after restart") {
		t.Fatalf("reconstructed context = %q", content)
	}
}

func TestFailedSessionInferenceDoesNotPersistTurn(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "failed-session-doll", "FailedSessionDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-failure")
	provider := &sessionContextProvider{err: errors.New("inference failed")}
	var diagnostics bytes.Buffer
	service := New(store, provider, logger.New(logger.ErrorLevel, &diagnostics), WithModelContextBudget(testModelContextBudget))
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))

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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))
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
