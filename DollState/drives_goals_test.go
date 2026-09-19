package dollstate

import (
	"encoding/json"
	"testing"
)

// Semantic identifiers for Spark's test Drive and Goal.
const (
	sparkDriveID = "drive-continuity"
	sparkGoalID  = "goal-test-continuity"
)

func TestDriveAllCanonicalFields(t *testing.T) {
	d := DriveItem{
		ID:          sparkDriveID,
		Name:        "Maintain Continuity",
		Description: "Spark is driven to preserve her state, memories, and identity across restarts and sessions.",
	}
	if d.ID != sparkDriveID {
		t.Errorf("expected ID %q, got %q", sparkDriveID, d.ID)
	}
	if d.Name != "Maintain Continuity" {
		t.Errorf("expected Name %q, got %q", "Maintain Continuity", d.Name)
	}
	if d.Description == "" {
		t.Error("expected non-empty Description")
	}
}

func TestGoalAllCanonicalFields(t *testing.T) {
	g := GoalItem{
		ID:          sparkGoalID,
		Name:        "Prove Memory Survives",
		Description: "Verify that Spark's memories persist after store close and reopen.",
		State:       GoalStateActive,
		DriveID:     sparkDriveID,
	}
	if g.ID != sparkGoalID {
		t.Errorf("expected ID %q, got %q", sparkGoalID, g.ID)
	}
	if g.Name != "Prove Memory Survives" {
		t.Errorf("expected Name %q, got %q", "Prove Memory Survives", g.Name)
	}
	if g.Description == "" {
		t.Error("expected non-empty Description")
	}
	if g.State != GoalStateActive {
		t.Errorf("expected State %q, got %q", GoalStateActive, g.State)
	}
	if g.DriveID != sparkDriveID {
		t.Errorf("expected DriveID %q, got %q", sparkDriveID, g.DriveID)
	}
}

func TestDriveJSONRoundTrip(t *testing.T) {
	original := DriveItem{
		ID:          sparkDriveID,
		Name:        "Maintain Continuity",
		Description: "Spark is driven to preserve her state across restarts.",
	}

	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var restored DriveItem
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if restored.ID != original.ID {
		t.Errorf("ID lost in round-trip: expected %q, got %q", original.ID, restored.ID)
	}
	if restored.Name != original.Name {
		t.Errorf("Name lost in round-trip: expected %q, got %q", original.Name, restored.Name)
	}
	if restored.Description != original.Description {
		t.Errorf("Description lost in round-trip: expected %q, got %q", original.Description, restored.Description)
	}
}

func TestGoalJSONRoundTrip(t *testing.T) {
	original := GoalItem{
		ID:          sparkGoalID,
		Name:        "Prove Memory Survives",
		Description: "Memories must survive store close and reopen.",
		State:       GoalStateActive,
		DriveID:     sparkDriveID,
	}

	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var restored GoalItem
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if restored.ID != original.ID {
		t.Errorf("ID lost in round-trip: expected %q, got %q", original.ID, restored.ID)
	}
	if restored.Name != original.Name {
		t.Errorf("Name lost in round-trip: expected %q, got %q", original.Name, restored.Name)
	}
	if restored.Description != original.Description {
		t.Errorf("Description lost in round-trip: expected %q, got %q", original.Description, restored.Description)
	}
	if restored.State != original.State {
		t.Errorf("State changed in round-trip: expected %q, got %q", original.State, restored.State)
	}
	if restored.DriveID != original.DriveID {
		t.Errorf("DriveID lost in round-trip: expected %q, got %q", original.DriveID, restored.DriveID)
	}
}

func TestGoalActiveExplicitlySerialized(t *testing.T) {
	// Active must appear explicitly in JSON, not be inferred from a missing boolean.
	g := GoalItem{
		ID:    sparkGoalID,
		Name:  "Prove Memory Survives",
		State: GoalStateActive,
	}

	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("json.Unmarshal to map: %v", err)
	}

	if raw["state"] != "active" {
		t.Errorf("expected state=\"active\" in JSON, got %v", raw["state"])
	}

	// Prove there is no 'completed' boolean field — it's State now.
	if _, ok := raw["completed"]; ok {
		t.Error("unexpected 'completed' field in Goal JSON — state replaces completed")
	}
}

func TestGoalCompletedExplicitlySerialized(t *testing.T) {
	g := GoalItem{
		ID:    "goal-completed-test",
		Name:  "Completed Goal",
		State: GoalStateCompleted,
	}

	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("json.Unmarshal to map: %v", err)
	}

	if raw["state"] != "completed" {
		t.Errorf("expected state=\"completed\" in JSON, got %v", raw["state"])
	}

	var restored GoalItem
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if restored.State != GoalStateCompleted {
		t.Errorf("expected State %q, got %q", GoalStateCompleted, restored.State)
	}
}

func TestGoalToDriveRelationshipSurvivesJSON(t *testing.T) {
	drive := DriveItem{
		ID:   sparkDriveID,
		Name: "Maintain Continuity",
	}
	goal := GoalItem{
		ID:      sparkGoalID,
		Name:    "Prove Memory Survives",
		State:   GoalStateActive,
		DriveID: sparkDriveID,
	}

	// Marshal both independently
	db, err := json.Marshal(drive)
	if err != nil {
		t.Fatalf("json.Marshal drive: %v", err)
	}
	gb, err := json.Marshal(goal)
	if err != nil {
		t.Fatalf("json.Marshal goal: %v", err)
	}

	// Deserialize back
	var dBack DriveItem
	if err := json.Unmarshal(db, &dBack); err != nil {
		t.Fatalf("json.Unmarshal drive: %v", err)
	}
	var gBack GoalItem
	if err := json.Unmarshal(gb, &gBack); err != nil {
		t.Fatalf("json.Unmarshal goal: %v", err)
	}

	// The relationship is: goal.DriveID == drive.ID
	if gBack.DriveID != dBack.ID {
		t.Errorf("Goal-to-Drive relationship lost: DriveID %q does not match Drive ID %q",
			gBack.DriveID, dBack.ID)
	}
}

func TestSemanticIDsUnchangedThroughSerialization(t *testing.T) {
	driveID := "drive-maintain-continuity"
	goalID := "goal-prove-persistence-v2"

	drive := DriveItem{ID: driveID, Name: "Test"}
	goal := GoalItem{ID: goalID, Name: "Test", State: GoalStateActive, DriveID: driveID}

	db, _ := json.Marshal(drive)
	gb, _ := json.Marshal(goal)

	var d DriveItem
	var g GoalItem
	json.Unmarshal(db, &d)
	json.Unmarshal(gb, &g)

	if d.ID != driveID {
		t.Errorf("Drive ID changed: expected %q, got %q", driveID, d.ID)
	}
	if g.ID != goalID {
		t.Errorf("Goal ID changed: expected %q, got %q", goalID, g.ID)
	}
	if g.DriveID != driveID {
		t.Errorf("Goal DriveID changed: expected %q, got %q", driveID, g.DriveID)
	}
}

func TestEmptyDrivesValid(t *testing.T) {
	d := Drives{}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal empty Drives: %v", err)
	}

	var restored Drives
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal empty Drives: %v", err)
	}

	if len(restored.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(restored.Items))
	}
}

func TestEmptyGoalsValid(t *testing.T) {
	g := Goals{}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal empty Goals: %v", err)
	}

	var restored Goals
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal empty Goals: %v", err)
	}

	if len(restored.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(restored.Items))
	}
}

func TestDrivesAndGoalsInDollStateRoundTrip(t *testing.T) {
	state := NewDollState()
	state.Identity = Identity{DollID: "test-doll", CanonicalName: "Tester"}
	state.Drives = Drives{
		Items: []DriveItem{
			{
				ID:          sparkDriveID,
				Name:        "Maintain Continuity",
				Description: "Preserve state across restarts.",
			},
		},
	}
	state.Goals = Goals{
		Items: []GoalItem{
			{
				ID:          sparkGoalID,
				Name:        "Prove Memory Survives",
				Description: "Memories survive store close and reopen.",
				State:       GoalStateActive,
				DriveID:     sparkDriveID,
			},
		},
	}

	b, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal DollState: %v", err)
	}

	var restored DollState
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal DollState: %v", err)
	}

	if len(restored.Drives.Items) != 1 {
		t.Fatalf("expected 1 drive, got %d", len(restored.Drives.Items))
	}
	if restored.Drives.Items[0].ID != sparkDriveID {
		t.Errorf("Drive ID: expected %q, got %q", sparkDriveID, restored.Drives.Items[0].ID)
	}
	if restored.Drives.Items[0].Name != "Maintain Continuity" {
		t.Errorf("Drive Name: expected %q, got %q", "Maintain Continuity", restored.Drives.Items[0].Name)
	}
	if restored.Drives.Items[0].Description != "Preserve state across restarts." {
		t.Errorf("Drive Description mismatch")
	}

	if len(restored.Goals.Items) != 1 {
		t.Fatalf("expected 1 goal, got %d", len(restored.Goals.Items))
	}
	if restored.Goals.Items[0].ID != sparkGoalID {
		t.Errorf("Goal ID: expected %q, got %q", sparkGoalID, restored.Goals.Items[0].ID)
	}
	if restored.Goals.Items[0].Name != "Prove Memory Survives" {
		t.Errorf("Goal Name: expected %q, got %q", "Prove Memory Survives", restored.Goals.Items[0].Name)
	}
	if restored.Goals.Items[0].State != GoalStateActive {
		t.Errorf("Goal State: expected %q, got %q", GoalStateActive, restored.Goals.Items[0].State)
	}
	if restored.Goals.Items[0].DriveID != sparkDriveID {
		t.Errorf("Goal DriveID: expected %q, got %q", sparkDriveID, restored.Goals.Items[0].DriveID)
	}

	// Verify the Goal-to-Drive relationship is intact
	if restored.Goals.Items[0].DriveID != restored.Drives.Items[0].ID {
		t.Error("Goal-to-Drive relationship broken: Goal.DriveID does not match Drive.ID")
	}
}

func TestGoalMinimalFields(t *testing.T) {
	// A Goal with only ID, Name, and State should serialize and deserialize cleanly.
	g := GoalItem{
		ID:    "goal-minimal",
		Name:  "Minimal goal",
		State: GoalStateActive,
	}

	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var restored GoalItem
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if restored.ID != g.ID {
		t.Errorf("ID: expected %q, got %q", g.ID, restored.ID)
	}
	if restored.Name != g.Name {
		t.Errorf("Name: expected %q, got %q", g.Name, restored.Name)
	}
	if restored.State != GoalStateActive {
		t.Errorf("State: expected %q, got %q", GoalStateActive, restored.State)
	}
	if restored.Description != "" {
		t.Errorf("expected empty Description, got %q", restored.Description)
	}
	if restored.DriveID != "" {
		t.Errorf("expected empty DriveID, got %q", restored.DriveID)
	}
}