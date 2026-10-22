package contextbuilder_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

type contentLengthEstimator struct{}

func (contentLengthEstimator) EstimateTokens(message contextbuilder.Message) int {
	return len([]rune(message.Content))
}

type fixture struct {
	store    persistence.Store
	history  contextbuilder.HistoryStore
	sessions session.Service
	builder  contextbuilder.Builder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	history, ok := store.(contextbuilder.HistoryStore)
	if !ok {
		t.Fatal("persistence store does not implement context history")
	}
	sessionStore, ok := store.(session.Store)
	if !ok {
		t.Fatal("persistence store does not implement session storage")
	}
	sessions := session.New(sessionStore)
	builder, err := contextbuilder.New(sessions, history, contentLengthEstimator{})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{store: store, history: history, sessions: sessions, builder: builder}
}

func (fx *fixture) createSession(t *testing.T, owner string) *session.InteractionSession {
	t.Helper()
	created, err := fx.sessions.Create(context.Background(), session.CreateRequest{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: owner}})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func (fx *fixture) append(t *testing.T, id session.ID, messages ...contextbuilder.NewHistoryMessage) []contextbuilder.HistoryMessage {
	t.Helper()
	created, err := fx.history.AppendSessionMessages(context.Background(), id, messages)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestBuildPreservesSystemInstructionsAndHistoryOrder(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	stored := fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "one"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleAssistant, Content: "two"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "three"},
	)
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 20, SystemInstructions: []string{"system"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []contextbuilder.Message{
		{Role: contextbuilder.RoleSystem, Content: "system"},
		{Role: contextbuilder.RoleUser, Content: "one"},
		{Role: contextbuilder.RoleAssistant, Content: "two"},
		{Role: contextbuilder.RoleUser, Content: "three"},
	}
	if !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("messages = %+v", result.Messages)
	}
	if result.Metadata.EstimatedTokens != 17 || result.Metadata.IncludedHistoryMessages != 3 || result.Metadata.OmittedHistoryMessages != 0 {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
	if !reflect.DeepEqual(result.Metadata.IncludedMessageIDs, []string{stored[0].ID, stored[1].ID, stored[2].ID}) {
		t.Fatalf("included IDs = %v", result.Metadata.IncludedMessageIDs)
	}
}

func TestBuildUsesWholeMessageRollingBudget(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	stored := fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "111"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleAssistant, Content: "222"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "333"},
	)
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 6})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{result.Messages[0].Content, result.Messages[1].Content}; !reflect.DeepEqual(got, []string{"222", "333"}) {
		t.Fatalf("rolling messages = %v", got)
	}
	if result.Metadata.IncludedHistoryMessages != 2 || result.Metadata.OmittedHistoryMessages != 1 || len(result.Metadata.OmittedMessages) != 1 || result.Metadata.OmittedMessages[0].ID != stored[0].ID {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
}

func TestBuildIsolatesSessionsAndHandlesEmptyHistory(t *testing.T) {
	fx := newFixture(t)
	first := fx.createSession(t, "body-one")
	second := fx.createSession(t, "body-one")
	fx.append(t, first.ID, contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "first"})
	fx.append(t, second.ID, contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "second"})
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: first.ID, TokenBudget: 20})
	if err != nil || len(result.Messages) != 1 || result.Messages[0].Content != "first" {
		t.Fatalf("isolated result = %+v, %v", result, err)
	}
	empty := fx.createSession(t, "body-two")
	result, err = fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: empty.ID, TokenBudget: 20})
	if err != nil || len(result.Messages) != 0 || result.Metadata.HistoryMessages != 0 {
		t.Fatalf("empty result = %+v, %v", result, err)
	}
}

func TestBuildNeverTruncatesOversizedMessages(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "old"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "this message is much too large"},
	)
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 0 || result.Metadata.OmittedHistoryMessages != 2 {
		t.Fatalf("oversized message was truncated or skipped around: %+v", result)
	}
	if result.Metadata.OmittedMessages[1].EstimatedTokens != len([]rune("this message is much too large")) {
		t.Fatalf("oversized metadata = %+v", result.Metadata.OmittedMessages)
	}
}

func TestBuildRejectsRequiredSystemContextOverBudget(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	_, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 3, SystemInstructions: []string{"system"}})
	if !errors.Is(err, contextbuilder.ErrRequiredContextExceedsBudget) {
		t.Fatalf("Build error = %v", err)
	}
}

func TestConcurrentBuildsRemainSessionIsolated(t *testing.T) {
	fx := newFixture(t)
	first := fx.createSession(t, "body-one")
	second := fx.createSession(t, "body-two")
	fx.append(t, first.ID, contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "first"})
	fx.append(t, second.ID, contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "second"})
	var wg sync.WaitGroup
	for _, test := range []struct {
		id   session.ID
		want string
	}{{first.ID, "first"}, {second.ID, "second"}} {
		for range 20 {
			wg.Add(1)
			go func(id session.ID, want string) {
				defer wg.Done()
				result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: id, TokenBudget: 20})
				if err != nil || len(result.Messages) != 1 || result.Messages[0].Content != want {
					t.Errorf("Build(%s) = %+v, %v", id, result, err)
				}
			}(test.id, test.want)
		}
	}
	wg.Wait()
}

func TestBuildReconstructsPersistedHistoryAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	history := store.(contextbuilder.HistoryStore)
	sessions := session.New(store.(session.Store))
	created, err := sessions.Create(ctx, session.CreateRequest{Owner: session.Owner{Kind: session.OwnerBody, InstanceID: "body-one"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := history.AppendSessionMessages(ctx, created.ID, []contextbuilder.NewHistoryMessage{{Role: contextbuilder.RoleUser, Content: "persisted"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	builder, err := contextbuilder.New(session.New(reopened.(session.Store)), reopened.(contextbuilder.HistoryStore), contentLengthEstimator{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := builder.Build(ctx, contextbuilder.Request{SessionID: created.ID, TokenBudget: 20})
	if err != nil || len(result.Messages) != 1 || result.Messages[0].Content != "persisted" {
		t.Fatalf("reconstructed result = %+v, %v", result, err)
	}
}
