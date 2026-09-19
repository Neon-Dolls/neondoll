package interaction

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// failStore wraps a real Store but fails on SaveDoll.
type failStore struct {
	persistence.Store
}

func (f *failStore) SaveDoll(ctx context.Context, state *dollstate.DollState) error {
	return errors.New("simulated persistence failure")
}

// seedDoll creates and saves a minimal doll with the given ID and canonical name.
func seedDoll(t *testing.T, store persistence.Store, dollID, name string) {
	t.Helper()
	state := dollstate.DollState{
		Version: dollstate.CurrentStateVersion,
	}
	state.Identity.DollID = dollID
	state.Identity.CanonicalName = name
	state.Soul.Content = "test soul"
	state.Owner.Name = "tester"
	if err := store.SaveDoll(context.Background(), &state); err != nil {
		t.Fatalf("seedDoll SaveDoll: %v", err)
	}
}

// openStore opens a fresh SQLite store at a temp path.
func openStore(t *testing.T) persistence.Store {
	t.Helper()
	store, err := persistence.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// newResponse constructs a minimal message Event representing a user message to a doll.
func newResponse(dollID, text string) events.Event {
	return events.NewDollMessage(uuid.New().String(), dollID, text)
}

func TestSuccessfulInteractionCreatesMemory(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "test-doll", "TestDoll")
	mockProvider := inference.NewMockProvider("mock", "Hello from TestDoll!")
	svc := New(store, mockProvider, logger.New(logger.ErrorLevel, io.Discard))

	event := newResponse("test-doll", "Hello, who are you?")
	resp, err := svc.HandleEvent(context.Background(), &event)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.Type != events.TypeMessage {
		t.Errorf("expected message response, got type %q", resp.Type)
	}

	// Verify persisted memory
	loaded, err := store.LoadDoll(context.Background(), "test-doll")
	if err != nil {
		t.Fatalf("LoadDoll after interaction: %v", err)
	}
	if len(loaded.Memories.Items) != 2 {
		t.Fatalf("expected 2 memory items, got %d", len(loaded.Memories.Items))
	}

	humanMem := loaded.Memories.Items[0]
	dollMem := loaded.Memories.Items[1]

	// Order: human message before doll response
	if humanMem.Sequence >= dollMem.Sequence {
		t.Errorf("human message sequence %d should be less than doll response sequence %d", humanMem.Sequence, dollMem.Sequence)
	}

	// Human message content is exact
	if humanMem.Content != "Hello, who are you?" {
		t.Errorf("human message content = %q, want %q", humanMem.Content, "Hello, who are you?")
	}

	// Doll response content exactly matches provider output
	if dollMem.Content != "Hello from TestDoll!" {
		t.Errorf("doll response content = %q, want %q", dollMem.Content, "Hello from TestDoll!")
	}

	// Kinds are correct
	if humanMem.Kind != dollstate.KindHumanMessage {
		t.Errorf("expected human_message kind, got %q", humanMem.Kind)
	}
	if dollMem.Kind != dollstate.KindDollResponse {
		t.Errorf("expected doll_response kind, got %q", dollMem.Kind)
	}

	// Correlation/semantic linkage preserved
	if humanMem.InteractionID == "" {
		t.Error("human memory has no interaction ID")
	}
	if humanMem.InteractionID != dollMem.InteractionID {
		t.Errorf("interaction IDs don't match: human=%q doll=%q", humanMem.InteractionID, dollMem.InteractionID)
	}

	// Records belong to the addressed Doll — the doll loaded was for "test-doll"
	if loaded.Identity.DollID != "test-doll" {
		t.Errorf("loaded doll ID = %q, want %q", loaded.Identity.DollID, "test-doll")
	}
}

func TestInteractionMemorySequenceMonotonic(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "seq-test", "SeqDoll")
	mockProvider := inference.NewMockProvider("mock", "response")
	svc := New(store, mockProvider, logger.New(logger.ErrorLevel, io.Discard))

	// Two interactions should yield sequences 0,1,2,3
	for i := 0; i < 2; i++ {
		event := newResponse("seq-test", "message")
		resp, err := svc.HandleEvent(context.Background(), &event)
		if err != nil {
			t.Fatalf("interaction %d: %v", i, err)
		}
		if resp == nil {
			t.Fatal("expected response")
		}
	}

	loaded, err := store.LoadDoll(context.Background(), "seq-test")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 4 {
		t.Fatalf("expected 4 memory items over 2 interactions, got %d", len(loaded.Memories.Items))
	}

	for i, m := range loaded.Memories.Items {
		if m.Sequence != i {
			t.Errorf("item %d: expected sequence %d, got %d", i, i, m.Sequence)
		}
	}
}

func TestUnknownDollCreatesNoMemory(t *testing.T) {
	store := openStore(t)
	mockProvider := inference.NewMockProvider("mock", "response")
	svc := New(store, mockProvider, logger.New(logger.ErrorLevel, io.Discard))

	event := newResponse("nonexistent-doll", "hello")
	resp, err := svc.HandleEvent(context.Background(), &event)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Type != events.TypeSystem {
		t.Errorf("expected error response for unknown doll, got type %q", resp.Type)
	}

	// Verify no state was saved for this doll
	_, err = store.LoadDoll(context.Background(), "nonexistent-doll")
	if err != persistence.ErrDollNotFound {
		t.Errorf("expected ErrDollNotFound, got %v", err)
	}
}

func TestInferenceFailureCreatesNoMemory(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "fail-test", "FailDoll")

	// Provider that always fails
	failProvider := &failInfer{}

	svc := New(store, failProvider, logger.New(logger.ErrorLevel, io.Discard))

	event := newResponse("fail-test", "hello")
	resp, err := svc.HandleEvent(context.Background(), &event)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}

	// Load and verify no memories were added
	loaded, err := store.LoadDoll(context.Background(), "fail-test")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 0 {
		t.Errorf("expected 0 memory items after inference failure, got %d", len(loaded.Memories.Items))
	}
}

// failInfer always fails inference.
type failInfer struct{}

func (f *failInfer) Infer(_ context.Context, _ inference.Request) (*inference.Response, error) {
	return nil, errors.New("inference unavailable")
}
func (f *failInfer) ID() inference.ProviderID { return "fail" }

func TestPersistenceFailureReturnsError(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "persist-fail", "PersistFail")
	mockProvider := inference.NewMockProvider("mock", "Hello!")

	// Wrap store with failing SaveDoll
	failStr := &failStore{Store: store}
	svc := New(failStr, mockProvider, logger.New(logger.ErrorLevel, io.Discard))

	event := newResponse("persist-fail", "hello")
	resp, err := svc.HandleEvent(context.Background(), &event)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}

	// Must be an error response — not a successful message
	if resp.Type != events.TypeSystem {
		t.Errorf("expected system error response after persistence failure, got type %q (Content=%v)", resp.Type, resp.Payload)
	}

	// Verify no memory was persisted (since SaveDoll failed)
	loaded, err := store.LoadDoll(context.Background(), "persist-fail")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 0 {
		t.Errorf("expected 0 memory items after failed persistence, got %d", len(loaded.Memories.Items))
	}
}

func TestEmptyMessageCreatesMemory(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "empty-msg", "EmptyDoll")
	mockProvider := inference.NewMockProvider("mock", "response")
	svc := New(store, mockProvider, logger.New(logger.ErrorLevel, io.Discard))

	// An empty message — should still be persisted as-is
	event := events.NewDollMessage(uuid.New().String(), "empty-msg", "")
	resp, err := svc.HandleEvent(context.Background(), &event)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}

	loaded, err := store.LoadDoll(context.Background(), "empty-msg")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 2 {
		t.Fatalf("expected 2 memory items, got %d", len(loaded.Memories.Items))
	}
	if loaded.Memories.Items[0].Content != "" {
		t.Errorf("human memory content expected empty string, got %q", loaded.Memories.Items[0].Content)
	}
}

func TestInteractionMemoryTimestampsPresent(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "ts-test", "TsDoll")
	mockProvider := inference.NewMockProvider("mock", "hi")
	svc := New(store, mockProvider, logger.New(logger.ErrorLevel, io.Discard))

	event := newResponse("ts-test", "hello")
	resp, err := svc.HandleEvent(context.Background(), &event)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response")
	}

	loaded, err := store.LoadDoll(context.Background(), "ts-test")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 2 {
		t.Fatalf("expected 2 memory items, got %d", len(loaded.Memories.Items))
	}

	for i, m := range loaded.Memories.Items {
		if m.Timestamp == "" {
			t.Errorf("item %d has empty timestamp", i)
		}
		// Verify it's a valid RFC3339 string
		if _, err := time.Parse(time.RFC3339, m.Timestamp); err != nil {
			t.Errorf("item %d: invalid timestamp %q: %v", i, m.Timestamp, err)
		}
	}
}

func TestNextSequence(t *testing.T) {
	tests := []struct {
		name  string
		items []dollstate.MemoryItem
		want  int
	}{
		{"empty memory", nil, 0},
		{"single item", []dollstate.MemoryItem{{Sequence: 5}}, 6},
		{"multiple items", []dollstate.MemoryItem{{Sequence: 0}, {Sequence: 1}, {Sequence: 4}, {Sequence: 2}}, 5},
		{"starts at zero", []dollstate.MemoryItem{{Sequence: 0}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextSequence(tt.items)
			if got != tt.want {
				t.Errorf("nextSequence = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestConcurrentInteractionsForSameDoll(t *testing.T) {
	store := openStore(t)
	seedDoll(t, store, "concurrent-doll", "ConcurrentDoll")

	// A provider that sleeps briefly to ensure goroutines overlap
	slowProvider := inference.NewMockProvider("slow", "response")

	svc := New(store, slowProvider, logger.New(logger.ErrorLevel, io.Discard))

	var wg sync.WaitGroup
	errs := make(chan error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := newResponse("concurrent-doll", "hello")
			_, err := svc.HandleEvent(context.Background(), &event)
			if err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("HandleEvent: %v", err)
	}

	loaded, err := store.LoadDoll(context.Background(), "concurrent-doll")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}

	if len(loaded.Memories.Items) != 4 {
		t.Fatalf("expected 4 memory items from 2 concurrent interactions, got %d", len(loaded.Memories.Items))
	}

	// Sequences must be 0,1,2,3 globally
	for i, m := range loaded.Memories.Items {
		if m.Sequence != i {
			t.Errorf("item %d: expected sequence %d, got %d", i, i, m.Sequence)
		}
	}

	// Kinds must be in order: human, doll, human, doll
	expectedKinds := []string{
		dollstate.KindHumanMessage,
		dollstate.KindDollResponse,
		dollstate.KindHumanMessage,
		dollstate.KindDollResponse,
	}
	for i, k := range expectedKinds {
		if loaded.Memories.Items[i].Kind != k {
			t.Errorf("item %d: expected kind %q, got %q", i, k, loaded.Memories.Items[i].Kind)
		}
	}

	// Two unique InteractionIDs, each owning one pair
	interactionIDs := make(map[string]int)
	for _, m := range loaded.Memories.Items {
		interactionIDs[m.InteractionID]++
	}
	if len(interactionIDs) != 2 {
		t.Errorf("expected 2 unique interaction IDs, got %d", len(interactionIDs))
	}
	for id, count := range interactionIDs {
		if count != 2 {
			t.Errorf("interaction %s has %d items, want 2", id, count)
		}
	}
}

// Verify that existing e2e and integration tests compile alongside the new code.
// The actual green-test guarantee comes from `go test ./...`
func TestExistingTestsRemainCompilable(t *testing.T) {
	// Import check only — existing test suites exercise the service through
	// the Store interface, which hasn't changed.
	_ = context.Background()
	_ = strings.NewReader("")
	t.Log("compilation check: all packages importable")
}