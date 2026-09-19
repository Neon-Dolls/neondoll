package persistence_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollCard"
	"github.com/Neon-Dolls/neondoll/DollState"
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
