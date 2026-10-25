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
	builder  contextbuilder.SourceBuilder
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
	var turnID session.TurnID
	for index := range messages {
		if messages[index].TurnID != "" {
			turnID = messages[index].TurnID
			continue
		}
		if messages[index].Role == contextbuilder.RoleUser || turnID == "" {
			turnID = session.NewTurnID()
		}
		messages[index].TurnID = turnID
		if messages[index].Role == contextbuilder.RoleAssistant {
			turnID = ""
		}
	}
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

func TestBuildSelectsCompleteConversationTurns(t *testing.T) {
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
	if got := messageContents(result.Messages); !reflect.DeepEqual(got, []string{"333"}) {
		t.Fatalf("rolling messages = %v", got)
	}
	if result.Metadata.IncludedHistoryMessages != 1 || result.Metadata.OmittedHistoryMessages != 2 || len(result.Metadata.OmittedMessages) != 2 || result.Metadata.OmittedMessages[0].ID != stored[0].ID || result.Metadata.OmittedMessages[1].ID != stored[1].ID {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
	if *result.Metadata.HistoryStartSequence != 0 || *result.Metadata.HistoryEndSequence != 2 || *result.Metadata.WindowStartSequence != 2 || *result.Metadata.WindowEndSequence != 2 {
		t.Fatalf("window boundaries = %+v", result.Metadata)
	}
}

func TestBuildWindowAdvancesAndRespondsToBudgetChanges(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	stored := fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "one"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleAssistant, Content: "two"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "six"},
	)

	build := func(budget int) contextbuilder.Result {
		t.Helper()
		result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: budget})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	initial := build(6)
	if got := messageContents(initial.Messages); !reflect.DeepEqual(got, []string{"six"}) {
		t.Fatalf("initial window = %v", got)
	}
	fx.append(t, created.ID, contextbuilder.NewHistoryMessage{TurnID: stored[2].TurnID, Role: contextbuilder.RoleAssistant, Content: "ten"})
	advanced := build(6)
	if got := []string{advanced.Messages[0].Content, advanced.Messages[1].Content}; !reflect.DeepEqual(got, []string{"six", "ten"}) {
		t.Fatalf("advanced window = %v", got)
	}
	if *advanced.Metadata.WindowStartSequence != 2 || *advanced.Metadata.WindowEndSequence != 3 {
		t.Fatalf("advanced boundaries = %+v", advanced.Metadata)
	}

	expanded := build(12)
	if len(expanded.Messages) != 4 || *expanded.Metadata.WindowStartSequence != 0 || *expanded.Metadata.WindowEndSequence != 3 {
		t.Fatalf("expanded window = %+v", expanded)
	}
	repeated := build(6)
	if !reflect.DeepEqual(advanced, repeated) {
		t.Fatalf("same history and budget produced different results:\nfirst:  %+v\nsecond: %+v", advanced, repeated)
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

func TestBuildReservesRequiredTailAfterRollingHistory(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "old"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleAssistant, Content: "new"},
	)
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{
		SessionID: created.ID, TokenBudget: 10,
		RequiredMessages: []contextbuilder.Message{{Role: contextbuilder.RoleUser, Content: "current"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []contextbuilder.Message{{Role: contextbuilder.RoleUser, Content: "current"}}
	if !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("messages = %+v, want %+v", result.Messages, want)
	}
	_, err = fx.builder.Build(context.Background(), contextbuilder.Request{
		SessionID: created.ID, TokenBudget: 6,
		RequiredMessages: []contextbuilder.Message{{Role: contextbuilder.RoleUser, Content: "current"}},
	})
	if !errors.Is(err, contextbuilder.ErrRequiredContextExceedsBudget) {
		t.Fatalf("oversized required tail error = %v", err)
	}
}

func TestBuildPreservesPersistedSessionSystemMessages(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleSystem, Content: "required"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "optional message is too large"},
	)
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 || result.Messages[0] != (contextbuilder.Message{Role: contextbuilder.RoleSystem, Content: "required"}) {
		t.Fatalf("messages = %+v", result.Messages)
	}
}

func TestBuildSelectsTransientTailWithRequiredOverflow(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-transient")
	request := contextbuilder.Request{
		SessionID: created.ID, TokenBudget: 15,
		RequiredMessages: []contextbuilder.Message{{Role: contextbuilder.RoleUser, Content: "current"}},
		TransientMessages: []contextbuilder.TransientMessage{
			{ID: "old-tool", Message: contextbuilder.Message{Role: contextbuilder.RoleTool, Content: "old-tool"}, WindowGroup: "old-tool"},
			{ID: "new-tool", Message: contextbuilder.Message{Role: contextbuilder.RoleTool, Content: "new-tool"}, Required: true, WindowGroup: "new-tool"},
		},
	}
	result, err := fx.builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := []contextbuilder.Message{
		{Role: contextbuilder.RoleUser, Content: "current"},
		{Role: contextbuilder.RoleTool, Content: "new-tool"},
	}
	if !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("messages = %+v, want %+v", result.Messages, want)
	}
	if len(result.Metadata.OmittedMessages) != 1 || result.Metadata.OmittedMessages[0].ID != "old-tool" {
		t.Fatalf("omissions = %+v", result.Metadata.OmittedMessages)
	}
	request.TokenBudget = 14
	if _, err := fx.builder.Build(context.Background(), request); !errors.Is(err, contextbuilder.ErrRequiredContextExceedsBudget) {
		t.Fatalf("required transient overflow error = %v", err)
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
	if _, err := history.AppendSessionMessages(ctx, created.ID, []contextbuilder.NewHistoryMessage{
		{TurnID: session.TurnID("restart-turn"), Role: contextbuilder.RoleUser, Content: "old"},
		{TurnID: session.TurnID("restart-turn"), Role: contextbuilder.RoleAssistant, Content: "new"},
	}); err != nil {
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
	result, err := builder.Build(ctx, contextbuilder.Request{SessionID: created.ID, TokenBudget: 6})
	if err != nil || !reflect.DeepEqual(messageContents(result.Messages), []string{"old", "new"}) || *result.Metadata.WindowStartSequence != 0 || *result.Metadata.WindowEndSequence != 1 {
		t.Fatalf("reconstructed result = %+v, %v", result, err)
	}
}

func TestBuildOmitsOrphanedAssistantAndKeepsIncompleteUser(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	stored := fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleAssistant, Content: "orphan"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "incomplete user"},
	)
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageContents(result.Messages); !reflect.DeepEqual(got, []string{"incomplete user"}) {
		t.Fatalf("messages = %v", got)
	}
	if len(result.Metadata.OmittedMessages) != 1 || result.Metadata.OmittedMessages[0].ID != stored[0].ID || result.Metadata.OmittedMessages[0].Reason != contextbuilder.OmittedIncompleteTurn {
		t.Fatalf("omissions = %+v", result.Metadata.OmittedMessages)
	}
}

func TestBuildNeverPartiallyIncludesOversizedTurn(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleUser, Content: "1234"},
		contextbuilder.NewHistoryMessage{Role: contextbuilder.RoleAssistant, Content: "5678"},
	)
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 0 || result.Metadata.IncludedHistoryMessages != 0 || result.Metadata.OmittedHistoryMessages != 2 {
		t.Fatalf("oversized turn was partially included: %+v", result)
	}

	exact, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 8})
	if err != nil || !reflect.DeepEqual(messageContents(exact.Messages), []string{"1234", "5678"}) {
		t.Fatalf("exact-boundary turn = %+v, %v", exact, err)
	}
}

func TestBuildOmitsOrphanToolRecord(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	stored := fx.append(t, created.ID, contextbuilder.NewHistoryMessage{
		TurnID: session.TurnID("orphan-tool-turn"), Role: contextbuilder.RoleTool, Content: "unbound result",
	})
	result, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 0 || len(result.Metadata.OmittedMessages) != 1 || result.Metadata.OmittedMessages[0].ID != stored[0].ID || result.Metadata.OmittedMessages[0].Reason != contextbuilder.OmittedIncompleteTurn {
		t.Fatalf("orphan tool result = %+v", result)
	}
}

func TestBuildSelectsCompleteMultiRecordTurns(t *testing.T) {
	fx := newFixture(t)
	created := fx.createSession(t, "body-one")
	fx.append(t, created.ID,
		contextbuilder.NewHistoryMessage{TurnID: session.TurnID("old-turn"), Role: contextbuilder.RoleUser, Content: "aa"},
		contextbuilder.NewHistoryMessage{TurnID: session.TurnID("old-turn"), Role: contextbuilder.RoleTool, Content: "bb"},
		contextbuilder.NewHistoryMessage{TurnID: session.TurnID("old-turn"), Role: contextbuilder.RoleAssistant, Content: "cc"},
		contextbuilder.NewHistoryMessage{TurnID: session.TurnID("new-turn"), Role: contextbuilder.RoleUser, Content: "dd"},
		contextbuilder.NewHistoryMessage{TurnID: session.TurnID("new-turn"), Role: contextbuilder.RoleAssistant, Content: "ee"},
	)

	bounded, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageContents(bounded.Messages); !reflect.DeepEqual(got, []string{"dd", "ee"}) {
		t.Fatalf("bounded multi-record turns = %v", got)
	}
	if bounded.Metadata.IncludedHistoryMessages != 2 || bounded.Metadata.OmittedHistoryMessages != 3 {
		t.Fatalf("bounded metadata = %+v", bounded.Metadata)
	}

	expanded, err := fx.builder.Build(context.Background(), contextbuilder.Request{SessionID: created.ID, TokenBudget: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := messageContents(expanded.Messages); !reflect.DeepEqual(got, []string{"aa", "bb", "cc", "dd", "ee"}) {
		t.Fatalf("expanded multi-record turns = %v", got)
	}
}

func messageContents(messages []contextbuilder.Message) []string {
	result := make([]string, len(messages))
	for index, message := range messages {
		result[index] = message.Content
	}
	return result
}
