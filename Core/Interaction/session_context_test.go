package interaction

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard))

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
}

func TestSessionInteractionLongHistoryUsesBoundedSuffix(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "long-doll", "LongDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-long")
	large := strings.Repeat("history ", 900)
	for index := 0; index < 8; index++ {
		_, err := history.AppendSessionMessages(context.Background(), created.ID, []contextbuilder.NewHistoryMessage{{
			Role: contextbuilder.RoleUser, Content: large + string(rune('A'+index)),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	provider := &sessionContextProvider{}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard))
	event := newResponse("long-doll", "required latest input")
	if _, err := service.HandleInteraction(context.Background(), created, &event); err != nil {
		t.Fatal(err)
	}
	request := provider.requests[0]
	if tokens := approximateRequestTokens(request.Messages); tokens > InteractionContextTokenBudget {
		t.Fatalf("request tokens = %d, budget = %d", tokens, InteractionContextTokenBudget)
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard))
	event := newResponse("oversized-doll", strings.Repeat("x", InteractionContextTokenBudget*5))

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

func TestSessionInteractionIsolatesSessionHistories(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "isolated-doll", "IsolatedDoll")
	sessions, history := interactionSessionStores(t, store)
	first := createInteractionSession(t, sessions, "body-isolated")
	second := createInteractionSession(t, sessions, "body-isolated")
	_, _ = history.AppendSessionMessages(context.Background(), first.ID, []contextbuilder.NewHistoryMessage{{Role: contextbuilder.RoleUser, Content: "first secret"}})
	_, _ = history.AppendSessionMessages(context.Background(), second.ID, []contextbuilder.NewHistoryMessage{{Role: contextbuilder.RoleUser, Content: "second secret"}})
	provider := &sessionContextProvider{}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard))
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
	firstService := New(store, firstProvider, logger.New(logger.ErrorLevel, io.Discard))
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
	secondService := New(reopened, secondProvider, logger.New(logger.ErrorLevel, io.Discard))
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
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard))
	event := newResponse("failed-session-doll", "do not fabricate")
	if _, err := service.HandleInteraction(context.Background(), created, &event); err != nil {
		t.Fatal(err)
	}
	stored, err := history.ListSessionMessages(context.Background(), created.ID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("failed inference history = %+v, %v", stored, err)
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
