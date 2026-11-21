package persistence_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	selfstate "github.com/Neon-Dolls/neondoll/Core/Self"
	"github.com/Neon-Dolls/neondoll/DollState"
)

func TestWholeDollSavePreservesSelfAcrossIndependentConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "core.db")
	firstStore, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := persistence.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()

	state := dollstate.NewDollState()
	state.Identity.DollID = "shared-doll"
	state.Identity.CanonicalName = "Before"
	state.Self = dollstate.Self{DisplayName: "Legacy Spark", Pronouns: "she/her"}
	if err := firstStore.SaveDoll(ctx, &state); err != nil {
		t.Fatal(err)
	}
	stale, err := firstStore.LoadDoll(ctx, state.Identity.DollID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Self.DisplayName != "Legacy Spark" || stale.Self.RevisionID != "" {
		t.Fatalf("legacy Self initialization = %+v", stale.Self)
	}

	manager, err := selfstate.NewManager(secondStore.(selfstate.Store), state.Identity.DollID, selfstate.DefaultMaxTokens)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := manager.Revise(ctx, selfstate.RevisionRequest{Content: "# Self\n\nA newer understanding."})
	if err != nil {
		t.Fatal(err)
	}

	// This snapshot was loaded before the independent connection revised Self.
	// Its ordinary fields must persist without restoring its stale self_json.
	stale.Identity.CanonicalName = "After"
	stale.Goals.Items = []dollstate.GoalItem{{ID: "goal-1", Name: "Persist me", State: dollstate.GoalStateActive}}
	if err := firstStore.SaveDoll(ctx, stale); err != nil {
		t.Fatal(err)
	}
	loaded, err := firstStore.LoadDoll(ctx, state.Identity.DollID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Self != revision {
		t.Fatalf("stale whole-Doll save replaced Self: got %+v, want %+v", loaded.Self, revision)
	}
	if loaded.Identity.CanonicalName != "After" || len(loaded.Goals.Items) != 1 || loaded.Goals.Items[0].Name != "Persist me" {
		t.Fatalf("ordinary state was not persisted: %+v", loaded)
	}

	next := revision
	next.RevisionID = "self-revision-2"
	next.Revision = revision.Revision + 1
	next.UpdatedAt = revision.UpdatedAt.Add(time.Second)
	next.Content = "# Self\n\nThe current understanding."
	replaced, err := firstStore.(selfstate.Store).ReplaceSelf(ctx, state.Identity.DollID, revision.RevisionID, next)
	if err != nil || !replaced {
		t.Fatalf("expected revision replacement: replaced=%v err=%v", replaced, err)
	}
	replaced, err = secondStore.(selfstate.Store).ReplaceSelf(ctx, state.Identity.DollID, revision.RevisionID, revision)
	if err != nil || replaced {
		t.Fatalf("stale revision replacement: replaced=%v err=%v", replaced, err)
	}
	current, err := firstStore.(selfstate.Store).LoadSelf(ctx, state.Identity.DollID)
	if err != nil || current != next {
		t.Fatalf("current Self = %+v, err=%v; want %+v", current, err, next)
	}
}

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
