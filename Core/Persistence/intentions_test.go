package persistence_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollState"

	_ "modernc.org/sqlite"
)

// Fixed RFC3339 UTC timestamps used across all intention persistence tests.
const (
	wakeTimeA = "2026-09-20T12:00:00Z"
	wakeTimeB = "2026-09-20T12:30:00Z"
	wakeTimeC = "2026-09-20T13:00:00Z"
	wakeTimeD = "2026-09-20T13:30:00Z"
)

// TestRoundTripIntentions verifies that a single intention survives save
// and reload, including all fields.
func TestRoundTripIntentions(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)

	state := decodeSpark(t)

	state.Intentions.Items = []dollstate.IntentionItem{
		{
			ID:          "int-001",
			Subject:     "review grocery prices",
			Description: "compare current market rates against supplier contracts",
			WakeTime:    wakeTimeA,
			State:       dollstate.IntentionStatePending,
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
	if got.Description != "compare current market rates against supplier contracts" {
		t.Errorf("Description = %q, want compare current market rates against supplier contracts", got.Description)
	}
	if got.WakeTime != wakeTimeA {
		t.Errorf("WakeTime = %q, want %q", got.WakeTime, wakeTimeA)
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
		{ID: "int-001", Subject: "something", WakeTime: wakeTimeA, State: dollstate.IntentionStatePending},
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
		{ID: "int-001", Subject: "preserve me", WakeTime: wakeTimeA, State: dollstate.IntentionStatePending},
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
		{ID: "int-a", Subject: "alpha", WakeTime: wakeTimeA, State: dollstate.IntentionStatePending},
		{ID: "int-b", Subject: "beta", WakeTime: wakeTimeB, State: dollstate.IntentionStatePending},
		{ID: "int-c", Subject: "gamma", WakeTime: wakeTimeC, State: dollstate.IntentionStatePending},
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
		{ID: "int-old", Subject: "old", WakeTime: wakeTimeA, State: dollstate.IntentionStatePending},
	}
	if err := store.SaveDoll(context.Background(), state); err != nil {
		t.Fatalf("SaveDoll (first): %v", err)
	}

	// Replace with new set.
	state.Intentions.Items = []dollstate.IntentionItem{
		{ID: "int-new", Subject: "new", WakeTime: wakeTimeB, State: dollstate.IntentionStatePending},
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

// TestIntentionCloseReopen saves an intention with all canonical fields,
// closes the store, opens a new store at the same path, and verifies
// every field survives the real close/reopen boundary.
func TestIntentionCloseReopen(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	store := openStore(t, dbPath)
	state := decodeSpark(t)

	state.Intentions.Items = []dollstate.IntentionItem{
		{
			ID:          "int-persist",
			Subject:     "review supply chain",
			Description: "check inventory levels at all warehouses before restocking",
			WakeTime:    wakeTimeA,
			State:       dollstate.IntentionStatePending,
		},
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

	got := loaded.Intentions.Items[0]
	if got.ID != "int-persist" {
		t.Errorf("ID = %q, want int-persist", got.ID)
	}
	if got.Subject != "review supply chain" {
		t.Errorf("Subject = %q, want review supply chain", got.Subject)
	}
	if got.Description != "check inventory levels at all warehouses before restocking" {
		t.Errorf("Description = %q, want check inventory levels...", got.Description)
	}
	if got.WakeTime != wakeTimeA {
		t.Errorf("WakeTime = %q, want %q", got.WakeTime, wakeTimeA)
	}
	if got.State != dollstate.IntentionStatePending {
		t.Errorf("State = %q, want %q", got.State, dollstate.IntentionStatePending)
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
		{ID: "spark-int", Subject: "spark's intention", WakeTime: wakeTimeA, State: dollstate.IntentionStatePending},
	}
	luna.Intentions.Items = []dollstate.IntentionItem{
		{ID: "luna-int", Subject: "luna's intention", WakeTime: wakeTimeB, State: dollstate.IntentionStatePending},
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
		{ID: "int-001", Subject: "check inventory", WakeTime: wakeTimeA, State: dollstate.IntentionStatePending},
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

// TestMigrationAddsIntentionsColumn creates a SQLite database with the
// immediately previous schema (drives_json + goals_json present and
// populated, intentions_json absent), opens it with NewStore to trigger
// migration, then verifies:
//   - existing drives and goals survived intact
//   - Intentions.Items is nil (column defaulted to '[]' → decoded as nil)
//   - close/reopen idempotence (second NewStore doesn't error or lose data)
func TestMigrationAddsIntentionsColumn(t *testing.T) {
	dbPath, cleanup := tempDB(t)
	defer cleanup()

	// Step 1: Create a raw SQLite DB with the previous schema — base dolls
	// table plus drives_json and goals_json, but NO intentions_json column.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE dolls (
			doll_id       TEXT PRIMARY KEY,
			version       INTEGER NOT NULL,
			identity_json TEXT NOT NULL,
			soul_json     TEXT NOT NULL,
			owner_json    TEXT NOT NULL,
			drives_json   TEXT NOT NULL DEFAULT '[]',
			goals_json    TEXT NOT NULL DEFAULT '[]',
			created_at    TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		db.Close()
		t.Fatalf("create dolls table: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE memories (
			doll_id        TEXT NOT NULL,
			seq            INTEGER NOT NULL,
			mem_id         TEXT NOT NULL,
			interaction_id TEXT NOT NULL DEFAULT '',
			kind           TEXT NOT NULL,
			content        TEXT NOT NULL,
			timestamp      TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (doll_id, seq),
			FOREIGN KEY (doll_id) REFERENCES dolls(doll_id)
		);
	`)
	if err != nil {
		db.Close()
		t.Fatalf("create memories table: %v", err)
	}

	// Insert a doll with drives and goals data.
	// The drives_json and goals_json columns hold the full struct form
	// (Drives{Items: [...]}), not a bare array.
	drivesJSON := `{"items":[{"id":"drv-001","name":"eat","description":"need sustenance"}]}`
	goalsJSON := `{"items":[{"id":"gol-001","name":"find food","description":"locate and consume","state":"active"}]}`
	identityJSON := `{"doll_id":"spark-001","canonical_name":"Spark"}`
	soulJSON := `{"revision":1,"content":"I am Spark."}`
	ownerJSON := `{"owner_id":"zero","name":"Zero"}`

	_, err = db.Exec(`
		INSERT INTO dolls (doll_id, version, identity_json, soul_json, owner_json, drives_json, goals_json)
		VALUES ('spark-001', 1, ?, ?, ?, ?, ?)
	`, identityJSON, soulJSON, ownerJSON, drivesJSON, goalsJSON)
	if err != nil {
		db.Close()
		t.Fatalf("insert doll: %v", err)
	}
	db.Close()

	// Step 2: Open with NewStore — migration should add intentions_json column.
	s, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore after migration: %v", err)
	}

	loaded, err := s.LoadDoll(context.Background(), "spark-001")
	if err != nil {
		s.Close()
		t.Fatalf("LoadDoll after migration: %v", err)
	}
	s.Close()

	// Verify drives and goals survived the migration.
	if len(loaded.Drives.Items) != 1 {
		t.Errorf("drives: got %d, want 1", len(loaded.Drives.Items))
	}
	if len(loaded.Goals.Items) != 1 {
		t.Errorf("goals: got %d, want 1", len(loaded.Goals.Items))
	}

	// Verify drives content is correct.
	if loaded.Drives.Items[0].ID != "drv-001" {
		t.Errorf("drive ID = %q, want drv-001", loaded.Drives.Items[0].ID)
	}

	// Verify goals content is correct.
	if loaded.Goals.Items[0].ID != "gol-001" {
		t.Errorf("goal ID = %q, want gol-001", loaded.Goals.Items[0].ID)
	}

	// Intentions.Items must be nil — the column was absent, added with
	// DEFAULT '[]' by migration, and the nil-normalization decodes '[]' as nil.
	if loaded.Intentions.Items != nil {
		t.Errorf("Intentions.Items = %v, want nil after migration", loaded.Intentions.Items)
	}

	// Step 3: Reopen to prove migration idempotence.
	s2, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore idempotent reopen: %v", err)
	}
	defer s2.Close()

	loaded2, err := s2.LoadDoll(context.Background(), "spark-001")
	if err != nil {
		t.Fatalf("LoadDoll after idempotent reopen: %v", err)
	}

	// Verify data integrity maintained after second migration pass.
	if len(loaded2.Drives.Items) != 1 {
		t.Errorf("drives after reopen: got %d, want 1", len(loaded2.Drives.Items))
	}
	if len(loaded2.Goals.Items) != 1 {
		t.Errorf("goals after reopen: got %d, want 1", len(loaded2.Goals.Items))
	}
	if loaded2.Intentions.Items != nil {
		t.Errorf("Intentions.Items after reopen = %v, want nil", loaded2.Intentions.Items)
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
