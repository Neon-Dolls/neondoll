package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollState"
)

// TestRoundTripIntentions verifies that a single intention survives save
// and reload, including all fields.
func TestRoundTripIntentions(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)

	state := decodeSpark(t)

	wakeTime := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)

	state.Intentions.Items = []dollstate.IntentionItem{
		{
			ID:       "int-001",
			Subject:  "review grocery prices",
			WakeTime: wakeTime,
			State:    dollstate.IntentionStatePending,
		},
	}

	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}

	if loaded.Intentions.Items == nil {
		t.Fatal("loaded Intentions.Items is nil, want 1 item")
	}
	if len(loaded.Intentions.Items) != 1 {
		t.Fatalf("loaded %d intentions, want 1", len(loaded.Intentions.Items))
	}

	got := loaded.Intentions.Items[0]
	if got.ID != "int-001" {
		t.Errorf("ID = %q, want int-001", got.ID)
	}
	if got.Subject != "review grocery prices" {
		t.Errorf("Subject = %q, want review grocery prices", got.Subject)
	}
	if got.WakeTime != wakeTime {
		t.Errorf("WakeTime = %q, want %q", got.WakeTime, wakeTime)
	}
	if got.State != dollstate.IntentionStatePending {
		t.Errorf("State = %q, want %q", got.State, dollstate.IntentionStatePending)
	}
}

// TestSaveLoadZeroIntentions saves an intention, then saves with zero
// intentions (empty Items) — verifies the intention is cleared.
func TestSaveLoadZeroIntentions(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)
	state := decodeSpark(t)

	// Save with one intention.
	state.Intentions.Items = []dollstate.IntentionItem{
		{ID: "int-001", Subject: "something", WakeTime: "later", State: dollstate.IntentionStatePending},
	}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (with intention): %v", err)
	}

	// Load and verify it's there.
	loaded, err := store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Intentions.Items) != 1 {
		t.Fatal("expected 1 intention before clear")
	}

	// Now save with empty intentions — should clear them.
	state.Intentions.Items = []dollstate.IntentionItem{}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (clear intentions): %v", err)
	}

	loaded, err = store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll after clear: %v", err)
	}
	if loaded.Intentions.Items != nil {
		t.Fatal("expected nil intentions after saving empty slice")
	}
}

// TestNilPreserveIntentions verifies that when Intentions.Items is nil,
// existing intentions in the DB are preserved.
func TestNilPreserveIntentions(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)
	state := decodeSpark(t)

	// Save with an intention.
	state.Intentions.Items = []dollstate.IntentionItem{
		{ID: "int-001", Subject: "preserve me", WakeTime: "later", State: dollstate.IntentionStatePending},
	}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (with intention): %v", err)
	}

	// Reload, set Intentions.Items to nil, save.
	state, err := store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	state.Intentions.Items = nil

	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (nil intentions): %v", err)
	}

	loaded, err := store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll after nil save: %v", err)
	}
	if loaded.Intentions.Items == nil {
		t.Fatal("nil Intentions.Items after nil-preserve save, expected previous value")
	}
	if len(loaded.Intentions.Items) != 1 || loaded.Intentions.Items[0].ID != "int-001" {
		t.Fatalf("expected preserved intention int-001, got %+v", loaded.Intentions.Items)
	}
}

// TestMultipleIntentionsRoundTrip saves and loads multiple intentions.
func TestMultipleIntentionsRoundTrip(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)
	state := decodeSpark(t)

	state.Intentions.Items = []dollstate.IntentionItem{
		{ID: "int-a", Subject: "alpha", WakeTime: "t1", State: dollstate.IntentionStatePending},
		{ID: "int-b", Subject: "beta", WakeTime: "t2", State: dollstate.IntentionStatePending},
		{ID: "int-c", Subject: "gamma", WakeTime: "t3", State: dollstate.IntentionStatePending},
	}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Intentions.Items) != 3 {
		t.Fatalf("got %d intentions, want 3", len(loaded.Intentions.Items))
	}
	for i, want := range []string{"int-a", "int-b", "int-c"} {
		if loaded.Intentions.Items[i].ID != want {
			t.Errorf("item[%d].ID = %q, want %q", i, loaded.Intentions.Items[i].ID, want)
		}
	}
}

// TestIntentionReplace saves intentions, then saves different ones to
// verify replacement semantics.
func TestIntentionReplace(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)
	state := decodeSpark(t)

	// Save first set.
	state.Intentions.Items = []dollstate.IntentionItem{
		{ID: "int-old", Subject: "old", WakeTime: "t1", State: dollstate.IntentionStatePending},
	}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (first): %v", err)
	}

	// Replace with new set.
	state.Intentions.Items = []dollstate.IntentionItem{
		{ID: "int-new", Subject: "new", WakeTime: "t2", State: dollstate.IntentionStatePending},
	}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (replace): %v", err)
	}

	loaded, err := store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Intentions.Items) != 1 {
		t.Fatalf("got %d intentions, want 1", len(loaded.Intentions.Items))
	}
	if loaded.Intentions.Items[0].ID != "int-new" {
		t.Errorf("item[0].ID = %q, want int-new", loaded.Intentions.Items[0].ID)
	}
}

// TestIntentionCloseReopen saves an intention, closes the store, opens a
// new store at the same path, and verifies the intention survives.
func TestIntentionCloseReopen(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)
	state := decodeSpark(t)

	state.Intentions.Items = []dollstate.IntentionItem{
		{ID: "int-persist", Subject: "survive restart", WakeTime: "later", State: dollstate.IntentionStatePending},
	}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store2.Close()

	loaded, err := store2.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll after reopen: %v", err)
	}
	if loaded.Intentions.Items == nil || len(loaded.Intentions.Items) != 1 {
		t.Fatalf("expected 1 intention after reopen, got %+v", loaded.Intentions.Items)
	}
	if loaded.Intentions.Items[0].ID != "int-persist" {
		t.Errorf("ID = %q after reopen", loaded.Intentions.Items[0].ID)
	}
}

// TestIntentionIsolation verifies that two dolls have independent intentions.
func TestIntentionIsolation(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)
	spark := decodeSpark(t)
	luna := newLunaState(t)

	spark.Intentions.Items = []dollstate.IntentionItem{
		{ID: "spark-int", Subject: "spark's intention", WakeTime: "t1", State: dollstate.IntentionStatePending},
	}
	luna.Intentions.Items = []dollstate.IntentionItem{
		{ID: "luna-int", Subject: "luna's intention", WakeTime: "t2", State: dollstate.IntentionStatePending},
	}

	if err := store.SaveDoll(context.Background(), spark); err != nil {
		t.Fatalf("SaveDoll spark: %v", err)
	}
	if err := store.SaveDoll(context.Background(), luna); err != nil {
		t.Fatalf("SaveDoll luna: %v", err)
	}

	loadedSpark, err := store.LoadDoll(context.Background(), spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll spark: %v", err)
	}
	loadedLuna, err := store.LoadDoll(context.Background(), luna.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll luna: %v", err)
	}

	if len(loadedSpark.Intentions.Items) != 1 || loadedSpark.Intentions.Items[0].ID != "spark-int" {
		t.Errorf("spark intentions wrong: %+v", loadedSpark.Intentions.Items)
	}
	if len(loadedLuna.Intentions.Items) != 1 || loadedLuna.Intentions.Items[0].ID != "luna-int" {
		t.Errorf("luna intentions wrong: %+v", loadedLuna.Intentions.Items)
	}
}

// TestExistingStateSurvivesIntentions creates a doll with drives and goals,
// then adds intentions — verifies existing drives/goals are not disturbed
// by the new intentions column.
func TestExistingStateSurvivesIntentions(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)
	state := decodeSpark(t)

	// Save with drives and goals but no intentions.
	state.Drives.Items = []dollstate.DriveItem{
		{ID: "drv-001", Name: "eat", Description: "need sustenance"},
	}
	state.Goals.Items = []dollstate.GoalItem{
		{ID: "gol-001", Name: "find food", State: dollstate.GoalStateActive},
	}
	state.Intentions.Items = nil // preserve existing (none)

	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (drives+goals only): %v", err)
	}

	// Load back and verify drives and goals are intact.
	loaded, err := store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Drives.Items) != 1 {
		t.Fatalf("got %d drives after load, want 1", len(loaded.Drives.Items))
	}
	if len(loaded.Goals.Items) != 1 {
		t.Fatalf("got %d goals after load, want 1", len(loaded.Goals.Items))
	}
	if loaded.Intentions.Items != nil {
		t.Fatal("Intentions.Items should be nil when none were saved")
	}

	// Now add intentions to the existing doll.
	state, err = store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll before adding intentions: %v", err)
	}
	state.Intentions.Items = []dollstate.IntentionItem{
		{ID: "int-001", Subject: "check inventory", WakeTime: "later", State: dollstate.IntentionStatePending},
	}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (with intentions): %v", err)
	}

	// Reload — all three should be present.
	loaded, err = store.LoadDoll(context.Background(), state.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll after adding intentions: %v", err)
	}
	if len(loaded.Drives.Items) != 1 {
		t.Errorf("drives: got %d, want 1", len(loaded.Drives.Items))
	}
	if len(loaded.Goals.Items) != 1 {
		t.Errorf("goals: got %d, want 1", len(loaded.Goals.Items))
	}
	if len(loaded.Intentions.Items) != 1 {
		t.Errorf("intentions: got %d, want 1", len(loaded.Intentions.Items))
	}
}

// newLunaState creates a minimal DollState for a second doll "Luna",
// used in isolation tests.
func newLunaState(t *testing.T) *dollstate.DollState {
	t.Helper()
	state := dollstate.NewDollState()
	state.Identity.DollID = "luna-001"
	state.Identity.CanonicalName = "Luna"
	state.Soul.Content = "A curious cat doll."
	return &state
}