package persistence_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollCard"
	"github.com/Neon-Dolls/neondoll/DollState"

	_ "modernc.org/sqlite"
)

// tempDB creates a temporary SQLite database path and a cleanup function.
func tempDB(t *testing.T) (string, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "neondoll-test-*.db")
	if err != nil {
		t.Fatalf("tempDB: %v", err)
	}
	path := f.Name()
	f.Close()
	return path, func() { os.Remove(path) }
}

func openStore(t *testing.T, path string) persistence.Store {
	t.Helper()
	s, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", path, err)
	}
	return s
}

func decodeSpark(t *testing.T) *dollstate.DollState {
	t.Helper()
	state, err := dollcard.Decode(filepath.Join("..", "..", "testdata", "dolls", "spark.dollcard"))
	if err != nil {
		t.Fatalf("Decode(spark.dollcard): %v", err)
	}
	return state
}

// TestRoundTrip verifies Spark's core fields survive save/load.
func TestRoundTrip(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}

	// Verify all continuity-bearing fields survive.
	if loaded.Version != spark.Version {
		t.Errorf("Version = %d, want %d", loaded.Version, spark.Version)
	}
	if loaded.Identity.DollID != spark.Identity.DollID {
		t.Errorf("DollID = %q, want %q", loaded.Identity.DollID, spark.Identity.DollID)
	}
	if loaded.Identity.CanonicalName != spark.Identity.CanonicalName {
		t.Errorf("CanonicalName = %q, want %q", loaded.Identity.CanonicalName, spark.Identity.CanonicalName)
	}
	if loaded.Soul.Content != spark.Soul.Content {
		t.Errorf("Soul.Content mismatch")
	}
	if loaded.Soul.Revision != spark.Soul.Revision {
		t.Errorf("Soul.Revision = %d, want %d", loaded.Soul.Revision, spark.Soul.Revision)
	}
	if loaded.Owner.Content != spark.Owner.Content {
		t.Errorf("Owner.Content mismatch")
	}
}

// TestStableIdentity verifies the exact DollID from spark.dollcard is preserved.
func TestStableIdentity(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	const wantID = "3f3cd340-cac2-4674-b1ac-36c5510096eb"
	spark := decodeSpark(t)
	if spark.Identity.DollID != wantID {
		t.Fatalf("Spark DollID = %q, want %q", spark.Identity.DollID, wantID)
	}

	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, wantID)
	if err != nil {
		t.Fatalf("LoadDoll(%q): %v", wantID, err)
	}
	if loaded.Identity.DollID != wantID {
		t.Errorf("Loaded DollID = %q, want %q", loaded.Identity.DollID, wantID)
	}
}

// TestMissingDoll verifies loading an unknown DollID returns ErrDollNotFound.
func TestMissingDoll(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	_, err := s.LoadDoll(ctx, "nonexistent-doll-id")
	if err == nil {
		t.Fatal("expected error for missing doll, got nil")
	}
	if err != persistence.ErrDollNotFound {
		t.Errorf("error = %v, want %v", err, persistence.ErrDollNotFound)
	}
}

// TestInvalidDollID verifies saving with an empty DollID returns ErrInvalidDollID.
func TestInvalidDollID(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	state := dollstate.NewDollState()
	state.Identity.DollID = ""

	err := s.SaveDoll(ctx, &state)
	if err == nil {
		t.Fatal("expected error for empty DollID, got nil")
	}
	if err != persistence.ErrInvalidDollID {
		t.Errorf("error = %v, want %v", err, persistence.ErrInvalidDollID)
	}
}

// TestReplacement verifies that saving updated state for the same DollID
// replaces the existing record (UPSERT) and LoadDoll returns the new state.
func TestReplacement(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (original): %v", err)
	}

	// Modify Spark's soul.
	spark.Soul.Content = spark.Soul.Content + "\n\n*Updated after initial persistence.*"
	spark.Soul.Revision = 2

	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (updated): %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}

	if loaded.Soul.Revision != 2 {
		t.Errorf("Soul.Revision = %d, want 2", loaded.Soul.Revision)
	}
	if !strings.Contains(loaded.Soul.Content, "Updated after initial persistence") {
		t.Error("Soul.Content does not contain updated text")
	}
}

// TestNilDollState verifies SaveDoll with a nil state returns ErrInvalidDollID
// and does not panic.
func TestNilDollState(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	err := s.SaveDoll(ctx, nil)
	if err == nil {
		t.Fatal("SaveDoll(nil): expected error, got nil")
	}
	if err != persistence.ErrInvalidDollID {
		t.Errorf("SaveDoll(nil): error = %v, want %v", err, persistence.ErrInvalidDollID)
	}
}

// ---------------------------------------------------------------------------
// Phase 2: Memory persistence tests
// ---------------------------------------------------------------------------

const testDollID = "3f3cd340-cac2-4674-b1ac-36c5510096eb"

func makeTestMemory(seq int, kind, id, content string) dollstate.MemoryItem {
	return dollstate.MemoryItem{
		ID:       id,
		Kind:     kind,
		Content:  content,
		Sequence: seq,
	}
}

// TestMemoryZero verifies loading a doll with no memories returns zero memories.
func TestMemoryZero(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if loaded.Memories.Items == nil {
		return // nil is the expected zero-memory state from NewDollState
	}
	if len(loaded.Memories.Items) != 0 {
		t.Errorf("Memories.Items = %d entries, want 0", len(loaded.Memories.Items))
	}
}

// TestMemoryOne verifies saving and loading a single memory record.
func TestMemoryOne(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "mem-1", "Hello, Spark!"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 1 {
		t.Fatalf("Memories.Items = %d entries, want 1", len(loaded.Memories.Items))
	}
	m := loaded.Memories.Items[0]
	if m.ID != "mem-1" {
		t.Errorf("Memory.ID = %q, want %q", m.ID, "mem-1")
	}
	if m.Kind != dollstate.KindHumanMessage {
		t.Errorf("Memory.Kind = %q, want %q", m.Kind, dollstate.KindHumanMessage)
	}
	if m.Content != "Hello, Spark!" {
		t.Errorf("Memory.Content = %q, want %q", m.Content, "Hello, Spark!")
	}
	if m.Sequence != 0 {
		t.Errorf("Memory.Sequence = %d, want 0", m.Sequence)
	}
}

// TestMemoryOrderedPair verifies that two memory records (human + doll)
// round-trip in the same order they were saved.
func TestMemoryOrderedPair(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "mem-h1", "Who are you?"),
		makeTestMemory(1, dollstate.KindDollResponse, "mem-d1", "I am Spark, your interface to the NeonDoll."),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 2 {
		t.Fatalf("Memories.Items = %d entries, want 2", len(loaded.Memories.Items))
	}
	// Check order by Sequence.
	if loaded.Memories.Items[0].ID != "mem-h1" || loaded.Memories.Items[1].ID != "mem-d1" {
		t.Errorf("Order after load: [0]=%q, [1]=%q; want [mem-h1, mem-d1]",
			loaded.Memories.Items[0].ID, loaded.Memories.Items[1].ID)
	}
	if loaded.Memories.Items[0].Sequence != 0 {
		t.Errorf("Sequence[0] = %d, want 0", loaded.Memories.Items[0].Sequence)
	}
	if loaded.Memories.Items[1].Sequence != 1 {
		t.Errorf("Sequence[1] = %d, want 1", loaded.Memories.Items[1].Sequence)
	}
	if loaded.Memories.Items[0].Kind != dollstate.KindHumanMessage {
		t.Errorf("Kind[0] = %q, want %q", loaded.Memories.Items[0].Kind, dollstate.KindHumanMessage)
	}
	if loaded.Memories.Items[1].Kind != dollstate.KindDollResponse {
		t.Errorf("Kind[1] = %q, want %q", loaded.Memories.Items[1].Kind, dollstate.KindDollResponse)
	}
}

// TestMemoryCloseReopen is the Phase 2 acceptance test:
//
//	decode spark → construct two memories → persist → close Store A
//	→ open Store B → LoadDoll(SparkID) → same two records, same order
//
// Store B must NOT reread spark.dollcard.
func TestMemoryCloseReopen(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()

	// Phase A: Decode, add memories, persist, close.
	spark := decodeSpark(t)
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "mem-close-1", "Memory before close?"),
		makeTestMemory(1, dollstate.KindDollResponse, "mem-close-2", "I remember everything."),
	}
	s1 := openStore(t, path)
	if err := s1.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close (s1): %v", err)
	}

	// Phase B: Open brand-new store — same DB, no card re-read.
	s2 := openStore(t, path)
	defer s2.Close()

	loaded, err := s2.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll from second store: %v", err)
	}
	if len(loaded.Memories.Items) != 2 {
		t.Fatalf("Memories.Items after reopen = %d entries, want 2", len(loaded.Memories.Items))
	}
	if loaded.Memories.Items[0].ID != "mem-close-1" || loaded.Memories.Items[1].ID != "mem-close-2" {
		t.Errorf("Order after reopen: [0]=%q, [1]=%q; want [mem-close-1, mem-close-2]",
			loaded.Memories.Items[0].ID, loaded.Memories.Items[1].ID)
	}
	// Verify core state also survives.
	if loaded.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName after reopen = %q, want %q", loaded.Identity.CanonicalName, "Spark")
	}
}

// TestMemoryIsolation verifies that each doll has its own memory and
// memories from one doll do not leak into another.
func TestMemoryIsolation(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Doll A: Spark with one memory.
	spark := decodeSpark(t)
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "spark-mem", "Spark's memory"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (spark): %v", err)
	}

	// Doll B: a second doll with different memory.
	dollB := dollstate.NewDollState()
	dollB.Identity = dollstate.Identity{DollID: "doll-b", CanonicalName: "DollB"}
	dollB.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "b-mem", "DollB's memory"),
	}
	if err := s.SaveDoll(ctx, &dollB); err != nil {
		t.Fatalf("SaveDoll (dollB): %v", err)
	}

	// Load Spark — should only have Spark's memory.
	loadedSpark, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll(spark): %v", err)
	}
	if len(loadedSpark.Memories.Items) != 1 {
		t.Fatalf("Spark has %d memories, want 1", len(loadedSpark.Memories.Items))
	}
	if loadedSpark.Memories.Items[0].ID != "spark-mem" {
		t.Errorf("Spark memory = %q, want %q", loadedSpark.Memories.Items[0].ID, "spark-mem")
	}

	// Load DollB — should only have DollB's memory.
	loadedB, err := s.LoadDoll(ctx, "doll-b")
	if err != nil {
		t.Fatalf("LoadDoll(dollB): %v", err)
	}
	if len(loadedB.Memories.Items) != 1 {
		t.Fatalf("DollB has %d memories, want 1", len(loadedB.Memories.Items))
	}
	if loadedB.Memories.Items[0].ID != "b-mem" {
		t.Errorf("DollB memory = %q, want %q", loadedB.Memories.Items[0].ID, "b-mem")
	}
}

// TestMemoryExistingStateSurvives verifies that adding memories to a doll
// does not corrupt or replace the existing core state (identity, soul, owner).
func TestMemoryExistingStateSurvives(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (no memories): %v", err)
	}

	// Load, add memories, save again.
	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	loaded.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "after-save-mem", "Memory added after first save"),
	}
	if err := s.SaveDoll(ctx, loaded); err != nil {
		t.Fatalf("SaveDoll (with memories): %v", err)
	}

	// Load again — should have both core state AND memories.
	final, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll (final): %v", err)
	}
	// Core state should be intact.
	if final.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName = %q, want %q", final.Identity.CanonicalName, "Spark")
	}
	if final.Soul.Revision != 1 {
		t.Errorf("Soul.Revision = %d, want 1", final.Soul.Revision)
	}
	if final.Soul.Content == "" {
		t.Error("Soul.Content is empty — core state was lost")
	}
	// Memories should be present.
	if len(final.Memories.Items) != 1 {
		t.Fatalf("Memories = %d entries, want 1", len(final.Memories.Items))
	}
	if final.Memories.Items[0].ID != "after-save-mem" {
		t.Errorf("Memory.ID = %q, want %q", final.Memories.Items[0].ID, "after-save-mem")
	}
}

// TestMemoryNoAccidentalErase verifies that a subsequent SaveDoll that does
// not explicitly manage memories (nil Items) does NOT erase existing ones.
func TestMemoryNoAccidentalErase(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Save Spark with one memory.
	spark := decodeSpark(t)
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "persistent-mem", "This memory must survive."),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (with memory): %v", err)
	}

	// Load and save again without touching Memories (state.Memories will be
	// populated from LoadDoll, so Items will be non-nil — this is the "pass
	// through without losing" path).
	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll (first): %v", err)
	}
	if err := s.SaveDoll(ctx, loaded); err != nil {
		t.Fatalf("SaveDoll (pass-through): %v", err)
	}

	// Verify memory survived.
	final, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll (final): %v", err)
	}
	if len(final.Memories.Items) != 1 {
		t.Fatalf("Memories after pass-through save = %d entries, want 1", len(final.Memories.Items))
	}
	if final.Memories.Items[0].ID != "persistent-mem" {
		t.Errorf("Memory.ID = %q, want %q", final.Memories.Items[0].ID, "persistent-mem")
	}
}

// TestMemoryNilItemsPreserves verifies that saving a state created by
// NewDollState (which has nil Memories.Items) does NOT erase existing memory.
// This tests the nil-Items-semantics: nil means "do not touch".
func TestMemoryNilItemsPreserves(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Save Spark with memories.
	spark := decodeSpark(t)
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "survivor-mem", "Don't delete me!"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (with memory): %v", err)
	}

	// Construct a fresh state (nil Memories.Items) and save it.
	fresh := &dollstate.DollState{
		Version:  dollstate.CurrentStateVersion,
		Identity: spark.Identity,
		Soul:     spark.Soul,
		Owner:    spark.Owner,
		// Memories left as zero value → nil Items → preserve
	}
	if err := s.SaveDoll(ctx, fresh); err != nil {
		t.Fatalf("SaveDoll (nil-memories state): %v", err)
	}

	// Verify memories survived despite nil Items in the saved state.
	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 1 {
		t.Fatalf("Memories = %d entries, want 1 (nil-Items save should preserve)", len(loaded.Memories.Items))
	}
	if loaded.Memories.Items[0].ID != "survivor-mem" {
		t.Errorf("Memory.ID = %q, want %q", loaded.Memories.Items[0].ID, "survivor-mem")
	}
}

// TestMemoryEmptySliceClears verifies that saving with an explicit empty
// slice clears memories (non-nil but zero-length = deliberate delete).
func TestMemoryEmptySliceClears(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Save Spark with one memory.
	spark := decodeSpark(t)
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "doomed-mem", "I shall be deleted."),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (with memory): %v", err)
	}

	// Save with explicit empty slice (deliberate deletion).
	spark.Memories.Items = []dollstate.MemoryItem{}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (empty slice): %v", err)
	}

	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 0 {
		t.Errorf("Memories after empty-slice save = %d entries, want 0", len(loaded.Memories.Items))
	}
}

// TestMemoryUpdateReplaces verifies that saving with a new set of memories
// fully replaces the previous set (not appends).
func TestMemoryUpdateReplaces(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "old-mem", "Old memory"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (first): %v", err)
	}

	// Replace with new memories.
	spark.Memories.Items = []dollstate.MemoryItem{
		makeTestMemory(0, dollstate.KindHumanMessage, "new-mem", "New memory"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (replace): %v", err)
	}

	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 1 {
		t.Fatalf("Memories = %d entries, want 1", len(loaded.Memories.Items))
	}
	if loaded.Memories.Items[0].ID != "new-mem" {
		t.Errorf("Memory.ID = %q, want %q", loaded.Memories.Items[0].ID, "new-mem")
	}
}

// TestContinuityIntegration is the acceptance test for the milestone:
//
//	Decode spark.dollcard → Save → Close store → Open new store → Load by DollID
//
// The second store must NOT read the Doll Card.
func TestContinuityIntegration(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()

	// Phase 1: Decode and persist.
	spark := decodeSpark(t)
	s1 := openStore(t, path)
	if err := s1.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close (s1): %v", err)
	}

	// Phase 2: Open a brand-new store instance on the same DB — must NOT read the card.
	s2 := openStore(t, path)
	defer s2.Close()

	loaded, err := s2.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll from second store: %v", err)
	}

	// Verify continuity: all fields must match original Spark state.
	if loaded.Identity.DollID != spark.Identity.DollID {
		t.Errorf("DollID = %q, want %q", loaded.Identity.DollID, spark.Identity.DollID)
	}
	if loaded.Identity.CanonicalName != spark.Identity.CanonicalName {
		t.Errorf("CanonicalName = %q, want %q", loaded.Identity.CanonicalName, spark.Identity.CanonicalName)
	}
	if loaded.Soul.Content != spark.Soul.Content {
		t.Errorf("Soul.Content mismatch")
	}
	if loaded.Owner.Content != spark.Owner.Content {
		t.Errorf("Owner.Content mismatch")
	}
}

// TestMemoryFullRecordRoundTrip verifies every field of a MemoryItem
// survives a round trip through SaveDoll → LoadDoll: ID, InteractionID,
// Kind, Content, Sequence, Timestamp.
func TestMemoryFullRecordRoundTrip(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	store := openStore(t, path)

	state := dollstate.NewDollState()
	state.Identity.DollID = "roundtrip-doll"
	state.Version = dollstate.CurrentStateVersion
	state.Identity.CanonicalName = "Roundtrip Doll"
	state.Soul.Content = "test soul"
	state.Owner.Content = "test owner"

	state.Memories.Items = []dollstate.MemoryItem{
		{
			ID:            "mem-42",
			InteractionID: "iact-roundtrip",
			Kind:          dollstate.KindHumanMessage,
			Content:       "Hello! This is a full-record test.",
			Sequence:      1,
			Timestamp:     "2026-09-19T12:00:00Z",
		},
	}

	if err := store.SaveDoll(ctx, &state); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := store.LoadDoll(ctx, "roundtrip-doll")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 1 {
		t.Fatalf("got %d memories, want 1", len(loaded.Memories.Items))
	}

	m := loaded.Memories.Items[0]
	if m.ID != "mem-42" {
		t.Errorf("ID = %q, want %q", m.ID, "mem-42")
	}
	if m.InteractionID != "iact-roundtrip" {
		t.Errorf("InteractionID = %q, want %q", m.InteractionID, "iact-roundtrip")
	}
	if m.Kind != dollstate.KindHumanMessage {
		t.Errorf("Kind = %q, want %q", m.Kind, dollstate.KindHumanMessage)
	}
	if m.Content != "Hello! This is a full-record test." {
		t.Errorf("Content = %q, want %q", m.Content, "Hello! This is a full-record test.")
	}
	if m.Sequence != 1 {
		t.Errorf("Sequence = %d, want %d", m.Sequence, 1)
	}
	if m.Timestamp != "2026-09-19T12:00:00Z" {
		t.Errorf("Timestamp = %q, want %q", m.Timestamp, "2026-09-19T12:00:00Z")
	}
}

// TestMemoryRollbackOnFailure verifies that when a SaveDoll memory
// replacement fails partway through (duplicate sequence → PK violation),
// the existing memories remain intact — proving the SQLite transaction
// rolls back the whole save.
func TestMemoryRollbackOnFailure(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	store := openStore(t, path)

	// Save a doll with two original memories.
	state := dollstate.NewDollState()
	state.Identity.DollID = "rollback-doll"
	state.Version = dollstate.CurrentStateVersion
	state.Identity.CanonicalName = "Rollback Doll"
	state.Soul.Content = "test soul"
	state.Owner.Content = "test owner"

	state.Memories.Items = []dollstate.MemoryItem{
		{ID: "orig-a", Kind: dollstate.KindHumanMessage, Content: "original A", Sequence: 1, InteractionID: "iact-1", Timestamp: "2026-09-19T10:00:00Z"},
		{ID: "orig-b", Kind: dollstate.KindDollResponse, Content: "original B", Sequence: 2, InteractionID: "iact-1", Timestamp: "2026-09-19T10:01:00Z"},
	}
	if err := store.SaveDoll(ctx, &state); err != nil {
		t.Fatalf("initial SaveDoll: %v", err)
	}

	// Confirm originals are persisted.
	loaded, err := store.LoadDoll(ctx, "rollback-doll")
	if err != nil {
		t.Fatalf("first LoadDoll: %v", err)
	}
	if len(loaded.Memories.Items) != 2 {
		t.Fatalf("expected 2 original memories, got %d", len(loaded.Memories.Items))
	}

	// Attempt to replace with a set that will fail on the second insert
	// (same seq = primary key violation).
	state.Memories.Items = []dollstate.MemoryItem{
		{ID: "replace-a", Kind: dollstate.KindHumanMessage, Content: "replacement A", Sequence: 1, InteractionID: "iact-2", Timestamp: "2026-09-19T12:00:00Z"},
		{ID: "replace-b", Kind: dollstate.KindDollResponse, Content: "replacement B", Sequence: 1, InteractionID: "iact-2", Timestamp: "2026-09-19T12:01:00Z"},
	}
	err = store.SaveDoll(ctx, &state)
	if err == nil {
		t.Fatal("SaveDoll should have failed (duplicate seq PK violation)")
	}

	// Reload — the original memories must still be there (rollback).
	reloaded, err := store.LoadDoll(ctx, "rollback-doll")
	if err != nil {
		t.Fatalf("LoadDoll after failure: %v", err)
	}
	if len(reloaded.Memories.Items) != 2 {
		t.Fatalf("expected 2 original memories after rollback, got %d", len(reloaded.Memories.Items))
	}
	if reloaded.Memories.Items[0].ID != "orig-a" {
		t.Errorf("memory 0 ID = %q, want %q", reloaded.Memories.Items[0].ID, "orig-a")
	}
	if reloaded.Memories.Items[1].ID != "orig-b" {
		t.Errorf("memory 1 ID = %q, want %q", reloaded.Memories.Items[1].ID, "orig-b")
	}
}

// TestPrePhase2Migration verifies that an existing database created without
// the drives_json/goals_json columns is correctly upgraded by createSchema
// and retains its existing Doll and Memory state.
func TestPrePhase2Migration(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	// Open a raw connection and create the pre-Phase-2 schema (no drives_json/goals_json).
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	prePhase2 := `
	CREATE TABLE IF NOT EXISTS dolls (
		doll_id      TEXT PRIMARY KEY,
		version      INTEGER NOT NULL,
		identity_json TEXT NOT NULL,
		soul_json     TEXT NOT NULL,
		owner_json    TEXT NOT NULL,
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE TABLE IF NOT EXISTS memories (
		doll_id        TEXT NOT NULL,
		seq            INTEGER NOT NULL,
		mem_id         TEXT NOT NULL,
		interaction_id TEXT NOT NULL DEFAULT '',
		kind           TEXT NOT NULL,
		content        TEXT NOT NULL,
		timestamp      TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (doll_id, seq),
		FOREIGN KEY (doll_id) REFERENCES dolls(doll_id)
	);`
	if _, err := raw.Exec(prePhase2); err != nil {
		raw.Close()
		t.Fatalf("create pre-Phase-2 schema: %v", err)
	}

	// Insert a doll with known state and two memories.
	ctx := context.Background()
	spark := decodeSpark(t)
	identityJSON := `{"doll_id":"3f3cd340-cac2-4674-b1ac-36c5510096eb","canonical_name":"Spark","display_name":"Spark","tags":null}`
	soulJSON := `{"content":"I am a test doll with an ancient soul.","revision":42}`
	ownerJSON := `{"content":"Master Zero"}`
	_, err = raw.Exec(
		`INSERT INTO dolls (doll_id, version, identity_json, soul_json, owner_json) VALUES (?, ?, ?, ?, ?)`,
		spark.Identity.DollID, spark.Version, identityJSON, soulJSON, ownerJSON,
	)
	if err != nil {
		raw.Close()
		t.Fatalf("insert doll: %v", err)
	}

	ms := []struct {
		seq  int
		kid  string
		id   string
		iact string
		ct   string
		ts   string
	}{
		{1, dollstate.KindHumanMessage, "mem-1", "iact-0", "Hello Spark", "2026-09-19T10:00:00Z"},
		{2, dollstate.KindDollResponse, "mem-2", "iact-0", "Greetings Master", "2026-09-19T10:00:05Z"},
	}
	for _, m := range ms {
		_, err = raw.Exec(
			`INSERT INTO memories (doll_id, seq, mem_id, interaction_id, kind, content, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			spark.Identity.DollID, m.seq, m.id, m.iact, m.kid, m.ct, m.ts,
		)
		if err != nil {
			raw.Close()
			t.Fatalf("insert memory seq=%d: %v", m.seq, err)
		}
	}
	raw.Close()

	// Re-open through NewStore — this runs the Phase-2 migration.
	s, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore (migration): %v", err)
	}
	defer s.Close()

	// Load the doll — all pre-existing state must survive.
	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll after migration: %v", err)
	}

	// Core identity fields preserved.
	if loaded.Identity.DollID != spark.Identity.DollID {
		t.Errorf("DollID = %q, want %q", loaded.Identity.DollID, spark.Identity.DollID)
	}
	if loaded.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName = %q, want %q", loaded.Identity.CanonicalName, "Spark")
	}
	if loaded.Version != spark.Version {
		t.Errorf("Version = %d, want %d", loaded.Version, spark.Version)
	}
	if loaded.Soul.Revision != 42 {
		t.Errorf("Soul.Revision = %d, want 42", loaded.Soul.Revision)
	}
	if loaded.Soul.Content != "I am a test doll with an ancient soul." {
		t.Errorf("Soul.Content mismatch")
	}
	if loaded.Owner.Content != "Master Zero" {
		t.Errorf("Owner.Content = %q, want %q", loaded.Owner.Content, "Master Zero")
	}

	// Memories preserved.
	if len(loaded.Memories.Items) != 2 {
		t.Fatalf("Memories.Items = %d entries, want 2", len(loaded.Memories.Items))
	}
	if loaded.Memories.Items[0].ID != "mem-1" {
		t.Errorf("Memory[0].ID = %q, want %q", loaded.Memories.Items[0].ID, "mem-1")
	}
	if loaded.Memories.Items[1].ID != "mem-2" {
		t.Errorf("Memory[1].ID = %q, want %q", loaded.Memories.Items[1].ID, "mem-2")
	}

	// Drives and goals must be nil (migrated from DEFAULT '[]' → LoadDoll normalization).
	if loaded.Drives.Items != nil {
		t.Errorf("Drives.Items = %v, want nil (migrated from empty DB)", loaded.Drives.Items)
	}
	if loaded.Goals.Items != nil {
		t.Errorf("Goals.Items = %v, want nil (migrated from empty DB)", loaded.Goals.Items)
	}

	// Verify the migration is idempotent: close and re-open.
	s.Close()
	s2, err := persistence.NewStore(path)
	if err != nil {
		t.Fatalf("NewStore (second open): %v", err)
	}
	defer s2.Close()

	loaded2, err := s2.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll after second open: %v", err)
	}
	if loaded2.Identity.DollID != spark.Identity.DollID {
		t.Errorf("Second open: DollID = %q, want %q", loaded2.Identity.DollID, spark.Identity.DollID)
	}
	if len(loaded2.Memories.Items) != 2 {
		t.Errorf("Second open: Memories.Items = %d, want 2", len(loaded2.Memories.Items))
	}
	if loaded2.Drives.Items != nil {
		t.Errorf("Second open: Drives.Items = %v, want nil", loaded2.Drives.Items)
	}
	if loaded2.Goals.Items != nil {
		t.Errorf("Second open: Goals.Items = %v, want nil", loaded2.Goals.Items)
	}
}

// TestSparkGoalContinuity is the Phase 3 acceptance test.
//
//	Phase A: decode spark.dollcard → establish continuity Drive and test Goal
//	         → SaveDoll → close Store A
//	Phase B: new Store B (same DB, no card re-read) → LoadDoll(SparkID)
//	         → continuity Drive and test Goal survive with 10+ assertions
//
// After the restart boundary the test MUST NOT reread the Doll Card.
func TestSparkGoalContinuity(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()

	// === Phase A: Decode, set drives/goals, persist, close ===
	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		{
			ID:          "drive-continuity-001",
			Name:        "maintain continuity",
			Description: "Spark must maintain her identity, drives, and goals across persistence restarts to prove the NeonDoll architecture works correctly.",
		},
	}
	spark.Goals.Items = []dollstate.GoalItem{
		{
			ID:          "goal-continuity-001",
			Name:        "prove goal continuity across persistence restart",
			Description: "A persistence restart must not lose Spark's goals.",
			State:       dollstate.GoalStateActive,
			DriveID:     "drive-continuity-001",
		},
	}

	s1 := openStore(t, path)
	if err := s1.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (Phase A): %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close (Phase A): %v", err)
	}

	// === Phase B: New store on the same DB — NO doll card re-read ===
	s2 := openStore(t, path)
	defer s2.Close()

	loaded, err := s2.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll (Phase B): %v", err)
	}

	// ---- 11 Assertions ----

	// 1. Stable DollID preserved.
	if loaded.Identity.DollID != spark.Identity.DollID {
		t.Errorf("DollID = %q, want %q", loaded.Identity.DollID, spark.Identity.DollID)
	}

	// 2. Drives.Items is non-nil (continuity Drive exists).
	if loaded.Drives.Items == nil {
		t.Fatal("Drives.Items is nil — continuity Drive lost")
	}

	// 3. Exactly 1 DriveItem.
	if len(loaded.Drives.Items) != 1 {
		t.Fatalf("Drives.Items = %d entries, want 1", len(loaded.Drives.Items))
	}

	// 4. Drive.ID matches the same semantic ID as before restart.
	if loaded.Drives.Items[0].ID != "drive-continuity-001" {
		t.Errorf("Drive.ID = %q, want %q", loaded.Drives.Items[0].ID, "drive-continuity-001")
	}

	// 5. Every canonical Drive field has the same semantic value.
	if loaded.Drives.Items[0].Name != "maintain continuity" {
		t.Errorf("Drive.Name = %q, want %q", loaded.Drives.Items[0].Name, "maintain continuity")
	}
	if loaded.Drives.Items[0].Description != spark.Drives.Items[0].Description {
		t.Errorf("Drive.Description mismatch")
	}

	// 6. Goals.Items is non-nil (test Goal exists).
	if loaded.Goals.Items == nil {
		t.Fatal("Goals.Items is nil — test Goal lost")
	}

	// 7. Exactly 1 GoalItem.
	if len(loaded.Goals.Items) != 1 {
		t.Fatalf("Goals.Items = %d entries, want 1", len(loaded.Goals.Items))
	}

	// 8. Goal.ID matches the same semantic ID as before restart.
	if loaded.Goals.Items[0].ID != "goal-continuity-001" {
		t.Errorf("Goal.ID = %q, want %q", loaded.Goals.Items[0].ID, "goal-continuity-001")
	}

	// 9. Every canonical Goal field preserved (Name, Description, State).
	if loaded.Goals.Items[0].Name != "prove goal continuity across persistence restart" {
		t.Errorf("Goal.Name = %q, want %q", loaded.Goals.Items[0].Name, "prove goal continuity across persistence restart")
	}
	if loaded.Goals.Items[0].Description != "A persistence restart must not lose Spark's goals." {
		t.Errorf("Goal.Description = %q, want %q", loaded.Goals.Items[0].Description, "A persistence restart must not lose Spark's goals.")
	}
	if loaded.Goals.Items[0].State != dollstate.GoalStateActive {
		t.Errorf("Goal.State = %q, want %q", loaded.Goals.Items[0].State, dollstate.GoalStateActive)
	}

	// 10. Goal-to-Drive relationship preserved.
	if loaded.Goals.Items[0].DriveID != "drive-continuity-001" {
		t.Errorf("Goal.DriveID = %q, want %q", loaded.Goals.Items[0].DriveID, "drive-continuity-001")
	}

	// 11. Core state also survived alongside drives/goals (supplementary check).
	if loaded.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName = %q, want %q", loaded.Identity.CanonicalName, "Spark")
	}
	if loaded.Soul.Content != spark.Soul.Content {
		t.Errorf("Soul.Content mismatch")
	}
	if loaded.Owner.Content != spark.Owner.Content {
		t.Errorf("Owner.Content mismatch")
	}
}
