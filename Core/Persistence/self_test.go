package persistence_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	selfstate "github.com/Neon-Dolls/neondoll/Core/Self"
	"github.com/Neon-Dolls/neondoll/DollState"
)

func TestDevelopingSelfPersistsAcrossRestartWithoutChangingSoul(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "core.db")
	store, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state := dollstate.NewDollState()
	state.Identity.DollID = "self-doll"
	state.Soul.Content = "Given and unchanged."
	if err := store.SaveDoll(ctx, &state); err != nil {
		t.Fatal(err)
	}
	selfStore := store.(selfstate.Store)
	manager, err := selfstate.NewManager(selfStore, state.Identity.DollID, selfstate.DefaultMaxTokens)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := manager.Revise(ctx, selfstate.RevisionRequest{Content: "# Self\n\nI am learning what patience means to me."})
	if err != nil {
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
	restarted, err := selfstate.NewManager(reopened.(selfstate.Store), state.Identity.DollID, selfstate.DefaultMaxTokens)
	if err != nil {
		t.Fatal(err)
	}
	current, err := restarted.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current != revision {
		t.Fatalf("restarted Self = %+v, want %+v", current, revision)
	}
	loaded, err := reopened.LoadDoll(ctx, state.Identity.DollID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Soul.Content != state.Soul.Content {
		t.Fatalf("Soul changed: %q", loaded.Soul.Content)
	}
}

func TestDevelopingSelfCompareAndSwapRejectsStaleWriter(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.NewStore(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := dollstate.NewDollState()
	state.Identity.DollID = "self-doll"
	if err := store.SaveDoll(ctx, &state); err != nil {
		t.Fatal(err)
	}
	first, _ := selfstate.NewManager(store.(selfstate.Store), state.Identity.DollID, selfstate.DefaultMaxTokens)
	second, _ := selfstate.NewManager(store.(selfstate.Store), state.Identity.DollID, selfstate.DefaultMaxTokens)
	revision, err := first.Revise(ctx, selfstate.RevisionRequest{Content: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Revise(ctx, selfstate.RevisionRequest{Content: "stale"}); !errors.Is(err, selfstate.ErrStaleRevision) {
		t.Fatalf("stale writer error = %v", err)
	}
	current, _ := first.Current(ctx)
	if current != revision {
		t.Fatalf("stale write changed current Self: %+v", current)
	}
}
