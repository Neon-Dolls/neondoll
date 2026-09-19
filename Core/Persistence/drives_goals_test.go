package persistence_test

import (
	"context"
	"testing"

	"github.com/Neon-Dolls/neondoll/DollState"
)

// ---------------------------------------------------------------------------
// Phase 2: Drive and Goal persistence tests
// ---------------------------------------------------------------------------

func makeTestDrive(id, name string) dollstate.DriveItem {
	return dollstate.DriveItem{
		ID:   id,
		Name: name,
	}
}

func makeTestDriveWithDesc(id, name, desc string) dollstate.DriveItem {
	return dollstate.DriveItem{
		ID:          id,
		Name:        name,
		Description: desc,
	}
}

func makeTestGoal(id, name, state, driveID string) dollstate.GoalItem {
	return dollstate.GoalItem{
		ID:      id,
		Name:    name,
		State:   state,
		DriveID: driveID,
	}
}

// TestDriveZero verifies a doll saved with no Drives loads with nil Items.
func TestDriveZero(t *testing.T) {
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
	if loaded.Drives.Items == nil {
		return // nil is the expected zero-drive state from NewDollState
	}
	if len(loaded.Drives.Items) != 0 {
		t.Errorf("Drives.Items = %d entries, want 0", len(loaded.Drives.Items))
	}
}

// TestDriveOne verifies saving and loading a single Drive.
func TestDriveOne(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("drive-1", "Explore the Grid"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Drives.Items) != 1 {
		t.Fatalf("Drives.Items = %d entries, want 1", len(loaded.Drives.Items))
	}
	d := loaded.Drives.Items[0]
	if d.ID != "drive-1" {
		t.Errorf("Drive.ID = %q, want %q", d.ID, "drive-1")
	}
	if d.Name != "Explore the Grid" {
		t.Errorf("Drive.Name = %q, want %q", d.Name, "Explore the Grid")
	}
}

// TestDriveMultiple verifies multiple Drives round-trip in order.
func TestDriveMultiple(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("drive-1", "First Drive"),
		makeTestDrive("drive-2", "Second Drive"),
		makeTestDrive("drive-3", "Third Drive"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Drives.Items) != 3 {
		t.Fatalf("Drives.Items = %d entries, want 3", len(loaded.Drives.Items))
	}
	if loaded.Drives.Items[0].ID != "drive-1" {
		t.Errorf("Item[0].ID = %q, want %q", loaded.Drives.Items[0].ID, "drive-1")
	}
	if loaded.Drives.Items[1].ID != "drive-2" {
		t.Errorf("Item[1].ID = %q, want %q", loaded.Drives.Items[1].ID, "drive-2")
	}
	if loaded.Drives.Items[2].ID != "drive-3" {
		t.Errorf("Item[2].ID = %q, want %q", loaded.Drives.Items[2].ID, "drive-3")
	}
}

// TestDriveCanonicalRoundTrip verifies every field of a DriveItem survives.
func TestDriveCanonicalRoundTrip(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	store := openStore(t, path)
	defer store.Close()

	state := dollstate.NewDollState()
	state.Identity.DollID = "canonical-drive-doll"
	state.Version = dollstate.CurrentStateVersion
	state.Identity.CanonicalName = "CanonicalDriveDoll"
	state.Soul.Content = "test soul"
	state.Owner.Content = "test owner"

	state.Drives.Items = []dollstate.DriveItem{
		makeTestDriveWithDesc("canon-drive-1", "Primary Drive", "The main driving force of this doll"),
	}

	if err := store.SaveDoll(ctx, &state); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := store.LoadDoll(ctx, "canonical-drive-doll")
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Drives.Items) != 1 {
		t.Fatalf("Drives.Items = %d entries, want 1", len(loaded.Drives.Items))
	}

	d := loaded.Drives.Items[0]
	if d.ID != "canon-drive-1" {
		t.Errorf("ID = %q, want %q", d.ID, "canon-drive-1")
	}
	if d.Name != "Primary Drive" {
		t.Errorf("Name = %q, want %q", d.Name, "Primary Drive")
	}
	if d.Description != "The main driving force of this doll" {
		t.Errorf("Description = %q, want %q", d.Description, "The main driving force of this doll")
	}
}

// TestGoalZero verifies a doll saved with no Goals loads with nil Items.
func TestGoalZero(t *testing.T) {
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
	if loaded.Goals.Items == nil {
		return // nil is the expected zero-goal state from NewDollState
	}
	if len(loaded.Goals.Items) != 0 {
		t.Errorf("Goals.Items = %d entries, want 0", len(loaded.Goals.Items))
	}
}

// TestGoalOne verifies saving and loading a single Goal.
func TestGoalOne(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("goal-1", "Reach the terminal", dollstate.GoalStateActive, ""),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Goals.Items) != 1 {
		t.Fatalf("Goals.Items = %d entries, want 1", len(loaded.Goals.Items))
	}
	g := loaded.Goals.Items[0]
	if g.ID != "goal-1" {
		t.Errorf("Goal.ID = %q, want %q", g.ID, "goal-1")
	}
	if g.Name != "Reach the terminal" {
		t.Errorf("Goal.Name = %q, want %q", g.Name, "Reach the terminal")
	}
	if g.State != dollstate.GoalStateActive {
		t.Errorf("Goal.State = %q, want %q", g.State, dollstate.GoalStateActive)
	}
}

// TestGoalMultiple verifies multiple Goals round-trip in order.
func TestGoalMultiple(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("goal-1", "First Goal", dollstate.GoalStateActive, ""),
		makeTestGoal("goal-2", "Second Goal", dollstate.GoalStateActive, ""),
		makeTestGoal("goal-3", "Third Goal", dollstate.GoalStateCompleted, ""),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Goals.Items) != 3 {
		t.Fatalf("Goals.Items = %d entries, want 3", len(loaded.Goals.Items))
	}
	if loaded.Goals.Items[0].ID != "goal-1" {
		t.Errorf("Item[0].ID = %q, want %q", loaded.Goals.Items[0].ID, "goal-1")
	}
	if loaded.Goals.Items[1].ID != "goal-2" {
		t.Errorf("Item[1].ID = %q, want %q", loaded.Goals.Items[1].ID, "goal-2")
	}
	if loaded.Goals.Items[2].ID != "goal-3" {
		t.Errorf("Item[2].ID = %q, want %q", loaded.Goals.Items[2].ID, "goal-3")
	}
}

// TestGoalDriveIDRoundTrip verifies Goal.DriveID survives persistence.
func TestGoalDriveIDRoundTrip(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("parent-drive", "Parent Drive"),
	}
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("child-goal", "Child Goal", dollstate.GoalStateActive, "parent-drive"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}

	loaded, err := s.LoadDoll(ctx, spark.Identity.DollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Goals.Items) != 1 {
		t.Fatalf("Goals.Items = %d entries, want 1", len(loaded.Goals.Items))
	}
	g := loaded.Goals.Items[0]
	if g.DriveID != "parent-drive" {
		t.Errorf("Goal.DriveID = %q, want %q", g.DriveID, "parent-drive")
	}
	if g.ID != "child-goal" {
		t.Errorf("Goal.ID = %q, want %q", g.ID, "child-goal")
	}
}

// TestDriveGoalCloseReopen verifies Drives and Goals survive store close/reopen.
func TestDriveGoalCloseReopen(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()

	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("survive-drive", "Drive that survives close"),
	}
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("survive-goal", "Goal that survives close", dollstate.GoalStateActive, "survive-drive"),
	}

	s1 := openStore(t, path)
	if err := s1.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close (s1): %v", err)
	}

	s2 := openStore(t, path)
	defer s2.Close()

	loaded, err := s2.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll from second store: %v", err)
	}

	if len(loaded.Drives.Items) != 1 {
		t.Fatalf("Drives after reopen = %d entries, want 1", len(loaded.Drives.Items))
	}
	if loaded.Drives.Items[0].ID != "survive-drive" {
		t.Errorf("Drive.ID after reopen = %q, want %q", loaded.Drives.Items[0].ID, "survive-drive")
	}

	if len(loaded.Goals.Items) != 1 {
		t.Fatalf("Goals after reopen = %d entries, want 1", len(loaded.Goals.Items))
	}
	if loaded.Goals.Items[0].ID != "survive-goal" {
		t.Errorf("Goal.ID after reopen = %q, want %q", loaded.Goals.Items[0].ID, "survive-goal")
	}
	if loaded.Goals.Items[0].DriveID != "survive-drive" {
		t.Errorf("Goal.DriveID after reopen = %q, want %q", loaded.Goals.Items[0].DriveID, "survive-drive")
	}

	// Verify core state also survives.
	if loaded.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName after reopen = %q, want %q", loaded.Identity.CanonicalName, "Spark")
	}
}

// TestDriveGoalIsolation verifies each doll has its own Drives and Goals.
func TestDriveGoalIsolation(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Doll A: Spark with one Drive and one Goal.
	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("spark-drive", "Spark's Drive"),
	}
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("spark-goal", "Spark's Goal", dollstate.GoalStateActive, "spark-drive"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (spark): %v", err)
	}

	// Doll B: a second doll with different Drive and Goal.
	dollB := dollstate.NewDollState()
	dollB.Identity = dollstate.Identity{DollID: "doll-b", CanonicalName: "DollB"}
	dollB.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("b-drive", "DollB's Drive"),
	}
	dollB.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("b-goal", "DollB's Goal", dollstate.GoalStateCompleted, "b-drive"),
	}
	if err := s.SaveDoll(ctx, &dollB); err != nil {
		t.Fatalf("SaveDoll (dollB): %v", err)
	}

	// Load Spark — should only have Spark's data.
	loadedSpark, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll(spark): %v", err)
	}
	if len(loadedSpark.Drives.Items) != 1 {
		t.Fatalf("Spark has %d Drives, want 1", len(loadedSpark.Drives.Items))
	}
	if loadedSpark.Drives.Items[0].ID != "spark-drive" {
		t.Errorf("Spark Drive = %q, want %q", loadedSpark.Drives.Items[0].ID, "spark-drive")
	}
	if len(loadedSpark.Goals.Items) != 1 {
		t.Fatalf("Spark has %d Goals, want 1", len(loadedSpark.Goals.Items))
	}
	if loadedSpark.Goals.Items[0].ID != "spark-goal" {
		t.Errorf("Spark Goal = %q, want %q", loadedSpark.Goals.Items[0].ID, "spark-goal")
	}

	// Load DollB — should only have DollB's data.
	loadedB, err := s.LoadDoll(ctx, "doll-b")
	if err != nil {
		t.Fatalf("LoadDoll(dollB): %v", err)
	}
	if len(loadedB.Drives.Items) != 1 {
		t.Fatalf("DollB has %d Drives, want 1", len(loadedB.Drives.Items))
	}
	if loadedB.Drives.Items[0].ID != "b-drive" {
		t.Errorf("DollB Drive = %q, want %q", loadedB.Drives.Items[0].ID, "b-drive")
	}
	if len(loadedB.Goals.Items) != 1 {
		t.Fatalf("DollB has %d Goals, want 1", len(loadedB.Goals.Items))
	}
	if loadedB.Goals.Items[0].ID != "b-goal" {
		t.Errorf("DollB Goal = %q, want %q", loadedB.Goals.Items[0].ID, "b-goal")
	}
}

// TestDriveExistingStateSurvives verifies that adding Drives/Goals to a doll
// does not corrupt or replace the existing core state (identity, soul, owner).
func TestDriveExistingStateSurvives(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (no drives/goals): %v", err)
	}

	// Load, add Drives and Goals, save again.
	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	loaded.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("after-drive", "Drive added after first save"),
	}
	loaded.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("after-goal", "Goal added after first save", dollstate.GoalStateActive, "after-drive"),
	}
	if err := s.SaveDoll(ctx, loaded); err != nil {
		t.Fatalf("SaveDoll (with drives/goals): %v", err)
	}

	// Load again — should have both core state AND drives/goals.
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

	// Drives should be present.
	if len(final.Drives.Items) != 1 {
		t.Fatalf("Drives = %d entries, want 1", len(final.Drives.Items))
	}
	if final.Drives.Items[0].ID != "after-drive" {
		t.Errorf("Drive.ID = %q, want %q", final.Drives.Items[0].ID, "after-drive")
	}

	// Goals should be present.
	if len(final.Goals.Items) != 1 {
		t.Fatalf("Goals = %d entries, want 1", len(final.Goals.Items))
	}
	if final.Goals.Items[0].ID != "after-goal" {
		t.Errorf("Goal.ID = %q, want %q", final.Goals.Items[0].ID, "after-goal")
	}
}

// TestDriveNilPreserves verifies that saving a state with nil Drives.Items
// does NOT erase existing Drives (nil = "do not touch").
func TestDriveNilPreserves(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Save Spark with one Drive.
	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("preserve-drive", "This Drive must survive"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (with drives): %v", err)
	}

	// Construct a fresh state with nil Drives.Items (zero value) and save.
	fresh := &dollstate.DollState{
		Version:  dollstate.CurrentStateVersion,
		Identity: spark.Identity,
		Soul:     spark.Soul,
		Owner:    spark.Owner,
		// Drives left as zero value → nil Items → preserve
	}
	if err := s.SaveDoll(ctx, fresh); err != nil {
		t.Fatalf("SaveDoll (nil-drives state): %v", err)
	}

	// Verify Drives survived despite nil Items in the saved state.
	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Drives.Items) != 1 {
		t.Fatalf("Drives = %d entries, want 1 (nil-Items save should preserve)", len(loaded.Drives.Items))
	}
	if loaded.Drives.Items[0].ID != "preserve-drive" {
		t.Errorf("Drive.ID = %q, want %q", loaded.Drives.Items[0].ID, "preserve-drive")
	}
}

// TestDriveEmptyClears verifies that saving with an explicit empty slice
// clears Drives (non-nil but zero-length = deliberate delete).
func TestDriveEmptyClears(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Save Spark with one Drive.
	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("doomed-drive", "I shall be deleted"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (with drives): %v", err)
	}

	// Save with explicit empty slice (deliberate deletion).
	spark.Drives.Items = []dollstate.DriveItem{}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (empty slice): %v", err)
	}

	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Drives.Items) != 0 {
		t.Errorf("Drives after empty-slice save = %d entries, want 0", len(loaded.Drives.Items))
	}
}

// TestDriveReplaces verifies that saving with a new set of Drives fully
// replaces the previous set.
func TestDriveReplaces(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("old-drive", "Old Drive"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (first): %v", err)
	}

	// Replace with new Drives.
	spark.Drives.Items = []dollstate.DriveItem{
		makeTestDrive("new-drive", "New Drive"),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (replace): %v", err)
	}

	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Drives.Items) != 1 {
		t.Fatalf("Drives = %d entries, want 1", len(loaded.Drives.Items))
	}
	if loaded.Drives.Items[0].ID != "new-drive" {
		t.Errorf("Drive.ID = %q, want %q", loaded.Drives.Items[0].ID, "new-drive")
	}
}

// TestGoalNilPreserves verifies that saving a state with nil Goals.Items
// does NOT erase existing Goals (nil = "do not touch").
func TestGoalNilPreserves(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Save Spark with one Goal.
	spark := decodeSpark(t)
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("preserve-goal", "This Goal must survive", dollstate.GoalStateActive, ""),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (with goals): %v", err)
	}

	// Construct a fresh state with nil Goals.Items (zero value) and save.
	fresh := &dollstate.DollState{
		Version:  dollstate.CurrentStateVersion,
		Identity: spark.Identity,
		Soul:     spark.Soul,
		Owner:    spark.Owner,
		// Goals left as zero value → nil Items → preserve
	}
	if err := s.SaveDoll(ctx, fresh); err != nil {
		t.Fatalf("SaveDoll (nil-goals state): %v", err)
	}

	// Verify Goals survived despite nil Items in the saved state.
	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Goals.Items) != 1 {
		t.Fatalf("Goals = %d entries, want 1 (nil-Items save should preserve)", len(loaded.Goals.Items))
	}
	if loaded.Goals.Items[0].ID != "preserve-goal" {
		t.Errorf("Goal.ID = %q, want %q", loaded.Goals.Items[0].ID, "preserve-goal")
	}
}

// TestGoalEmptyClears verifies that saving with an explicit empty slice
// clears Goals (non-nil but zero-length = deliberate delete).
func TestGoalEmptyClears(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	// Save Spark with one Goal.
	spark := decodeSpark(t)
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("doomed-goal", "I shall be deleted", dollstate.GoalStateActive, ""),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (with goals): %v", err)
	}

	// Save with explicit empty slice (deliberate deletion).
	spark.Goals.Items = []dollstate.GoalItem{}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (empty slice): %v", err)
	}

	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Goals.Items) != 0 {
		t.Errorf("Goals after empty-slice save = %d entries, want 0", len(loaded.Goals.Items))
	}
}

// TestGoalReplaces verifies that saving with a new set of Goals fully
// replaces the previous set.
func TestGoalReplaces(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)
	defer s.Close()

	spark := decodeSpark(t)
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("old-goal", "Old Goal", dollstate.GoalStateActive, ""),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (first): %v", err)
	}

	// Replace with new Goals.
	spark.Goals.Items = []dollstate.GoalItem{
		makeTestGoal("new-goal", "New Goal", dollstate.GoalStateCompleted, ""),
	}
	if err := s.SaveDoll(ctx, spark); err != nil {
		t.Fatalf("SaveDoll (replace): %v", err)
	}

	loaded, err := s.LoadDoll(ctx, testDollID)
	if err != nil {
		t.Fatalf("LoadDoll: %v", err)
	}
	if len(loaded.Goals.Items) != 1 {
		t.Fatalf("Goals = %d entries, want 1", len(loaded.Goals.Items))
	}
	if loaded.Goals.Items[0].ID != "new-goal" {
		t.Errorf("Goal.ID = %q, want %q", loaded.Goals.Items[0].ID, "new-goal")
	}
	if loaded.Goals.Items[0].State != dollstate.GoalStateCompleted {
		t.Errorf("Goal.State = %q, want %q", loaded.Goals.Items[0].State, dollstate.GoalStateCompleted)
	}
}