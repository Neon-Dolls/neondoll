package interaction

import (
	"context"
	"io"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

var testModelContextBudget = inference.ModelContextBudget{
	ContextTokens: 9216, OutputTokens: 768, RequiredOverheadTokens: 256,
}

const testInputTokenBudget = 8192

func seedDoll(t *testing.T, store persistence.Store, dollID, name string) {
	t.Helper()
	state := dollstate.DollState{Version: dollstate.CurrentStateVersion}
	state.Identity.DollID = dollID
	state.Identity.CanonicalName = name
	state.Soul.Content = "test soul"
	state.Owner.Name = "tester"
	if err := store.SaveDoll(context.Background(), &state); err != nil {
		t.Fatalf("seed doll: %v", err)
	}
}

func openStore(t *testing.T) persistence.Store {
	t.Helper()
	store, err := persistence.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newResponse(dollID, text string) events.Event {
	return events.NewDollMessage("interaction-id", dollID, text)
}

func TestSessionInteractionCreatesDollMemory(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "test-doll", "TestDoll")
	sessions, _ := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-one")
	service := New(store, inference.NewMockProvider("mock", "Hello from TestDoll!"), logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))
	event := newResponse("test-doll", "Hello, who are you?")

	response, err := service.HandleInteraction(context.Background(), created, &event)
	if err != nil || response == nil || response.Type != events.TypeMessage {
		t.Fatalf("HandleInteraction response=%+v err=%v", response, err)
	}
	loaded, err := store.LoadDoll(context.Background(), "test-doll")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Memories.Items) != 2 {
		t.Fatalf("memory items = %d, want 2", len(loaded.Memories.Items))
	}
	if loaded.Memories.Items[0].Content != "Hello, who are you?" || loaded.Memories.Items[1].Content != "Hello from TestDoll!" {
		t.Fatalf("memories = %+v", loaded.Memories.Items)
	}
}

func TestSessionInteractionRejectsInvalidPayloadWithoutPersistence(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "test-doll", "TestDoll")
	sessions, history := interactionSessionStores(t, store)
	created := createInteractionSession(t, sessions, "body-one")
	provider := &sessionContextProvider{}
	service := New(store, provider, logger.New(logger.ErrorLevel, io.Discard), WithModelContextBudget(testModelContextBudget))

	for name, payload := range map[string]any{"empty": events.MessagePayload{Text: "   "}, "unsupported": map[string]string{"text": "hello"}} {
		t.Run(name, func(t *testing.T) {
			event := events.Event{ID: name, DollID: "test-doll", Type: events.TypeMessage, Payload: payload}
			response, err := service.HandleInteraction(context.Background(), created, &event)
			if err != nil || response == nil || response.Type != events.TypeSystem {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
	if len(provider.requests) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(provider.requests))
	}
	stored, err := history.ListSessionMessages(context.Background(), created.ID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("history=%+v err=%v", stored, err)
	}
	loaded, err := store.LoadDoll(context.Background(), "test-doll")
	if err != nil || len(loaded.Memories.Items) != 0 {
		t.Fatalf("doll memories=%+v err=%v", loaded.Memories.Items, err)
	}
}

func TestNextSequence(t *testing.T) {
	items := []dollstate.MemoryItem{{Sequence: 0}, {Sequence: 4}, {Sequence: 2}}
	if got := nextSequence(items); got != 5 {
		t.Fatalf("nextSequence = %d, want 5", got)
	}
}
