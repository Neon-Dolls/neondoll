package self

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/DollState"
)

type fakeStore struct {
	mu    sync.Mutex
	self  dollstate.Self
	dolls map[string]bool
}

func (store *fakeStore) LoadSelf(_ context.Context, dollID string) (dollstate.Self, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.dolls[dollID] {
		return dollstate.Self{}, ErrNotFound
	}
	return store.self, nil
}

func (store *fakeStore) ReplaceSelf(_ context.Context, dollID, expected string, next dollstate.Self) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.dolls[dollID] {
		return false, ErrNotFound
	}
	if store.self.RevisionID != expected {
		return false, nil
	}
	store.self = next
	return true, nil
}

func (store *fakeStore) SaveDoll(_ context.Context, state *dollstate.DollState) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.self = state.Self
	return nil
}

func TestCreateAndReplaceSelfRevision(t *testing.T) {
	store := &fakeStore{dolls: map[string]bool{"doll": true}, self: dollstate.Self{DisplayName: "Legacy name"}}
	manager, err := NewManager(store, "doll", DefaultMaxTokens)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	first, err := manager.Revise(context.Background(), RevisionRequest{Content: "# Me\n\nI am learning to be patient."})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.RevisionID == "" || first.Content == "" || first.DisplayName != "Legacy name" ||
		!first.CreatedAt.Equal(now) || !first.UpdatedAt.Equal(now) {
		t.Fatalf("first revision = %+v", first)
	}

	manager.now = func() time.Time { return now.Add(time.Hour) }
	second, err := manager.Revise(context.Background(), RevisionRequest{ExpectedRevisionID: first.RevisionID, Content: "# Me\n\nI understand patience differently now."})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || second.RevisionID == first.RevisionID || second.Content == first.Content ||
		!second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("second revision = %+v", second)
	}
	current, _ := manager.Current(context.Background())
	if current != second {
		t.Fatalf("current = %+v, want %+v", current, second)
	}
}

func TestSelfRevisionRejectsStaleAndOversizedWithoutMutation(t *testing.T) {
	store := &fakeStore{dolls: map[string]bool{"doll": true}}
	manager, _ := NewManager(store, "doll", 2)
	manager.now = func() time.Time { return time.Unix(10, 0).UTC() }
	first, err := manager.Revise(context.Background(), RevisionRequest{Content: "12345678"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Revise(context.Background(), RevisionRequest{Content: "replacement"}); err != ErrStaleRevision {
		t.Fatalf("stale error = %v", err)
	}
	if _, err := manager.Revise(context.Background(), RevisionRequest{ExpectedRevisionID: first.RevisionID, Content: "123456789"}); err != ErrContentTooLarge {
		t.Fatalf("size error = %v", err)
	}
	current, _ := manager.Current(context.Background())
	if current != first {
		t.Fatalf("rejected revision mutated current: %+v", current)
	}
}

func TestSelfContractIsBoundedAndContextContainsOnlyCurrentRevision(t *testing.T) {
	value := dollstate.Self{RevisionID: "rev-2", Revision: 2, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(), Content: "current understanding"}
	if err := ValidateActive(value, 6); err != nil {
		t.Fatal(err)
	}
	context := ContextText(value)
	if context == "" || EstimatedTokens(value.Content) > 6 {
		t.Fatalf("context = %q", context)
	}
	if ContextText(dollstate.Self{}) != "" {
		t.Fatal("empty Self produced cognitive context")
	}
}

func TestWholeDollSaveCannotRestoreStaleSelf(t *testing.T) {
	store := &fakeStore{dolls: map[string]bool{"doll": true}}
	manager, _ := NewManager(store, "doll", DefaultMaxTokens)
	active, err := manager.Revise(context.Background(), RevisionRequest{Content: "active revision"})
	if err != nil {
		t.Fatal(err)
	}
	staleState := &dollstate.DollState{Identity: dollstate.Identity{DollID: "doll"}}
	if err := manager.SaveState(context.Background(), staleState); err != nil {
		t.Fatal(err)
	}
	current, _ := manager.Current(context.Background())
	if current != active || staleState.Self != active {
		t.Fatalf("whole-Doll save restored stale Self: current=%+v state=%+v", current, staleState.Self)
	}
}
