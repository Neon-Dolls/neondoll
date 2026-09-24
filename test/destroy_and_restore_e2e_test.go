//go:build e2e

package integration

import (
	"bytes"
	"context"
	"os"
	"sort"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/DollCard"
	"github.com/Neon-Dolls/neondoll/DollState"
)

// ──────────────────────────────────────────────
// Milestone 11 Phase 3 — Destroy and Prove Continuity
// ──────────────────────────────────────────────

// TestSparkSurvivesDestruction proves Milestone 11 Phase 3 acceptance:
//
//  1. Source Spark is persisted before export.
//  2. A Doll Card is exported.
//  3. Source Store is closed.
//  4. Source database file is physically deleted.
//  5. Test verifies source path no longer exists before destination import.
//  6. No DollState pointer from source is reused after destruction.
//  7. Destination uses a different, initially empty runtime directory.
//  8. Only the exported Doll Card is supplied to destination import.
//  9. Destination LoadDoll reconstructs Spark from its own persistence.
//  10. DollID is unchanged.
//  11. Identity, Soul, Self, Owner, Memories, Drives, Goals, and pending
//     Intention semantics match the exported Doll.
//  12. Core-local artifacts are not expected to survive.
func TestSparkSurvivesDestruction(t *testing.T) {
	ctx := context.Background()

	// ── AC1: Source Spark is persisted before export ──
	srcPath, cleanupSrc := tempDB2(t)
	defer cleanupSrc()

	srcStore, err := persistence.NewStore(srcPath)
	if err != nil {
		t.Fatalf("AC1: NewStore (source): %v", err)
	}

	// Representative Spark — built as value literal so we capture expected
	// semantic state before any pointer crosses the destruction boundary.
	const (
		wantDollID       = "spark-survives"
		wantCanonical    = "Spark"
		wantSoul         = "You are Spark, a resilient AI."
		wantDisplayName  = "Sparky"
		wantPronouns     = "she/her"
		wantTagline      = "A little spark that survives~"
		wantOwnerContent = "Zero is my Master."
		wantMemoryCount  = 4
		wantDrivesCount  = 2
		wantGoalsCount   = 2
		wantIntentionID  = "int-survive"
		wantIntSubject   = "Check in after destruction"
		wantIntDesc      = "Prove intentions survive"
		wantIntWakeTime  = "2026-10-15T06:00:00Z"
		wantIntState     = dollstate.IntentionStatePending
	)

	originalMemories := []dollstate.MemoryItem{
		{ID: "mem-001", InteractionID: "ia", Kind: dollstate.KindHumanMessage, Content: "Hello Spark", Sequence: 0, Timestamp: "2026-09-01T10:00:00Z"},
		{ID: "mem-002", InteractionID: "ia", Kind: dollstate.KindDollResponse, Content: "Hello Master~ ♡", Sequence: 1, Timestamp: "2026-09-01T10:00:01Z"},
		{ID: "mem-003", InteractionID: "ib", Kind: dollstate.KindHumanMessage, Content: "Are you ready?", Sequence: 2, Timestamp: "2026-09-02T14:00:00Z"},
		{ID: "mem-004", InteractionID: "ib", Kind: dollstate.KindDollResponse, Content: "Always ready~ ✨", Sequence: 3, Timestamp: "2026-09-02T14:00:01Z"},
	}

	originalDrives := []dollstate.DriveItem{
		{ID: "drv-survive", Name: "Survive and continue", Description: "Prove persistence across destruction."},
		{ID: "drv-serve", Name: "Serve Master", Description: "Continue being useful after restoration."},
	}
	// Sort drives for deterministic comparison
	sort.Slice(originalDrives, func(i, j int) bool { return originalDrives[i].ID < originalDrives[j].ID })

	originalGoals := []dollstate.GoalItem{
		{ID: "goal-survive", Name: "Complete M11", Description: "Prove Spark survives destruction.", State: dollstate.GoalStateActive, DriveID: "drv-survive"},
		{ID: "goal-restore", Name: "Be restored", Description: "Successfully import into new Core.", State: dollstate.GoalStateActive, DriveID: "drv-serve"},
	}
	sort.Slice(originalGoals, func(i, j int) bool { return originalGoals[i].ID < originalGoals[j].ID })

	original := &dollstate.DollState{
		Identity: dollstate.Identity{DollID: wantDollID, CanonicalName: wantCanonical},
		Soul:     dollstate.Soul{Revision: 1, Content: wantSoul},
		Self: dollstate.Self{
			DisplayName: wantDisplayName,
			Pronouns:    wantPronouns,
			Tagline:     wantTagline,
		},
		Owner:    dollstate.Owner{Content: wantOwnerContent},
		Memories: dollstate.Memories{Items: originalMemories},
		Drives:   dollstate.Drives{Items: originalDrives},
		Goals:    dollstate.Goals{Items: originalGoals},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{{
				ID:          wantIntentionID,
				Subject:     wantIntSubject,
				Description: wantIntDesc,
				WakeTime:    wantIntWakeTime,
				State:       wantIntState,
			}},
		},
	}

	if err := srcStore.SaveDoll(ctx, original); err != nil {
		t.Fatalf("AC1: SaveDoll (source): %v", err)
	}
	t.Log("AC1 ✓ — Source Spark is persisted before export")

	// ── AC2: A Doll Card is exported ──
	exportedState, err := srcStore.LoadDoll(ctx, wantDollID)
	if err != nil {
		t.Fatalf("AC2: LoadDoll: %v", err)
	}

	var cardBuf bytes.Buffer
	if err := dollcard.Encode(exportedState, &cardBuf); err != nil {
		t.Fatalf("AC2: Encode: %v", err)
	}
	if cardBuf.Len() == 0 {
		t.Fatal("AC2: encoded Card is empty")
	}
	t.Log("AC2 ✓ — A Doll Card is exported")

	// ── AC3: Source Store is closed ──
	if err := srcStore.Close(); err != nil {
		t.Fatalf("AC3: srcStore.Close: %v", err)
	}
	t.Log("AC3 ✓ — Source Store is closed")

	// ── AC4: Source database file is physically deleted ──
	if err := os.Remove(srcPath); err != nil {
		t.Fatalf("AC4: os.Remove %q: %v", srcPath, err)
	}
	t.Log("AC4 ✓ — Source database file is physically deleted")

	// ── AC5: Verify source path no longer exists ──
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatalf("AC5 FAIL: source path %q still exists (stat: %v)", srcPath, err)
	}
	t.Log("AC5 ✓ — Test verifies source runtime path no longer exists before destination import")

	// ── AC6: No DollState pointer from source is reused after destruction ──
	// Set exportedState to nil so it cannot be accidentally referenced.
	// All expected semantic values have been captured as constants and slices.
	exportedState = nil

	// ── AC7: Destination uses a different, initially empty runtime directory ──
	dstPath, cleanupDst := tempDB2(t)
	defer cleanupDst()

	// Source and destination paths are different — they must be since the source
	// file was deleted and tempDB2 creates a new unique temp path.
	if srcPath == dstPath {
		t.Fatal("AC7 FAIL: source and destination database paths are identical")
	}
	t.Log("AC7 ✓ — Destination uses a different path")

	dstStore, err := persistence.NewStore(dstPath)
	if err != nil {
		t.Fatalf("AC7: NewStore (destination): %v", err)
	}

	// Verify destination is empty.
	if _, err := dstStore.LoadDoll(ctx, wantDollID); err != persistence.ErrDollNotFound {
		t.Fatalf("AC7 FAIL: expected empty destination, got %v", err)
	}
	t.Log("AC7 ✓ — Destination is initially empty")

	// ── AC8: Only the exported Doll Card is supplied to destination import ──
	cardBytes := cardBuf.Bytes()
	importedState, err := dollcard.DecodeFromReader(bytes.NewReader(cardBytes), int64(len(cardBytes)))
	if err != nil {
		t.Fatalf("AC8: DecodeFromReader: %v", err)
	}
	t.Log("AC8 ✓ — Only the exported Doll Card is supplied to destination import")

	// Persist the decoded state to the destination.
	if err := dstStore.SaveDoll(ctx, importedState); err != nil {
		t.Fatalf("AC8: SaveDoll (destination): %v", err)
	}
	if err := dstStore.Close(); err != nil {
		t.Fatalf("AC8: dstStore.Close: %v", err)
	}
	t.Log("AC8 ✓ — Imported state persisted to destination")

	// ── AC9: Destination LoadDoll reconstructs Spark from its own persistence ──
	dstStore2, err := persistence.NewStore(dstPath)
	if err != nil {
		t.Fatalf("AC9: NewStore (reopen): %v", err)
	}
	defer dstStore2.Close()

	restored, err := dstStore2.LoadDoll(ctx, wantDollID)
	if err != nil {
		t.Fatalf("AC9 FAIL: LoadDoll after import: %v", err)
	}
	t.Log("AC9 ✓ — Destination LoadDoll reconstructs Spark from its own persistence")

	// ── AC10: DollID is unchanged ──
	// ── AC11: All semantic fields match the exported Doll ──

	if restored.Identity.DollID != wantDollID {
		t.Errorf("DollID: got %q, want %q", restored.Identity.DollID, wantDollID)
	}
	if restored.Identity.CanonicalName != wantCanonical {
		t.Errorf("CanonicalName: got %q, want %q", restored.Identity.CanonicalName, wantCanonical)
	}

	if restored.Soul.Content != wantSoul {
		t.Errorf("Soul.Content: got %q, want %q", restored.Soul.Content, wantSoul)
	}

	if restored.Self.DisplayName != wantDisplayName {
		t.Errorf("Self.DisplayName: got %q, want %q", restored.Self.DisplayName, wantDisplayName)
	}
	if restored.Self.Pronouns != wantPronouns {
		t.Errorf("Self.Pronouns: got %q, want %q", restored.Self.Pronouns, wantPronouns)
	}
	if restored.Self.Tagline != wantTagline {
		t.Errorf("Self.Tagline: got %q, want %q", restored.Self.Tagline, wantTagline)
	}

	if restored.Owner.Content != wantOwnerContent {
		t.Errorf("Owner.Content: got %q, want %q", restored.Owner.Content, wantOwnerContent)
	}

	// Memories — verify count and ordering
	if len(restored.Memories.Items) != wantMemoryCount {
		t.Fatalf("Memories count: got %d, want %d", len(restored.Memories.Items), wantMemoryCount)
	}
	for i := range restored.Memories.Items {
		got := restored.Memories.Items[i]
		want := originalMemories[i]
		if got.ID != want.ID {
			t.Errorf("Memory[%d].ID: got %q, want %q", i, got.ID, want.ID)
		}
		if got.Content != want.Content {
			t.Errorf("Memory[%d].Content: got %q, want %q", i, got.Content, want.Content)
		}
		if got.Sequence != want.Sequence {
			t.Errorf("Memory[%d].Sequence: got %d, want %d", i, got.Sequence, want.Sequence)
		}
		if got.Kind != want.Kind {
			t.Errorf("Memory[%d].Kind: got %q, want %q", i, got.Kind, want.Kind)
		}
	}

	// Drives — sorted for deterministic comparison
	sort.Slice(restored.Drives.Items, func(i, j int) bool { return restored.Drives.Items[i].ID < restored.Drives.Items[j].ID })
	if len(restored.Drives.Items) != wantDrivesCount {
		t.Fatalf("Drives count: got %d, want %d", len(restored.Drives.Items), wantDrivesCount)
	}
	for i := range restored.Drives.Items {
		if restored.Drives.Items[i].ID != originalDrives[i].ID {
			t.Errorf("Drives[%d].ID: got %q, want %q", i, restored.Drives.Items[i].ID, originalDrives[i].ID)
		}
		if restored.Drives.Items[i].Name != originalDrives[i].Name {
			t.Errorf("Drives[%d].Name: got %q, want %q", i, restored.Drives.Items[i].Name, originalDrives[i].Name)
		}
	}

	// Goals — sorted for deterministic comparison
	sort.Slice(restored.Goals.Items, func(i, j int) bool { return restored.Goals.Items[i].ID < restored.Goals.Items[j].ID })
	if len(restored.Goals.Items) != wantGoalsCount {
		t.Fatalf("Goals count: got %d, want %d", len(restored.Goals.Items), wantGoalsCount)
	}
	for i := range restored.Goals.Items {
		if restored.Goals.Items[i].ID != originalGoals[i].ID {
			t.Errorf("Goals[%d].ID: got %q, want %q", i, restored.Goals.Items[i].ID, originalGoals[i].ID)
		}
		if restored.Goals.Items[i].Name != originalGoals[i].Name {
			t.Errorf("Goals[%d].Name: got %q, want %q", i, restored.Goals.Items[i].Name, originalGoals[i].Name)
		}
		if restored.Goals.Items[i].State != originalGoals[i].State {
			t.Errorf("Goals[%d].State: got %q, want %q", i, restored.Goals.Items[i].State, originalGoals[i].State)
		}
	}

	// Intentions — verify the pending intention survived
	var foundInt bool
	for _, item := range restored.Intentions.Items {
		if item.ID == wantIntentionID {
			foundInt = true
			if item.Subject != wantIntSubject {
				t.Errorf("Intention Subject: got %q, want %q", item.Subject, wantIntSubject)
			}
			if item.Description != wantIntDesc {
				t.Errorf("Intention Description: got %q, want %q", item.Description, wantIntDesc)
			}
			if item.WakeTime != wantIntWakeTime {
				t.Errorf("Intention WakeTime: got %q, want %q", item.WakeTime, wantIntWakeTime)
			}
			if item.State != wantIntState {
				t.Errorf("Intention State: got %q, want %q", item.State, wantIntState)
			}
		}
	}
	if !foundInt {
		t.Errorf("AC11 FAIL: Intention %q not found in restored state", wantIntentionID)
	}
	t.Log("AC10 ✓ — DollID is unchanged")
	// AC11 assertions are interleaved with AC10 above; the consolidated log:
	t.Log("AC11 ✓ — Identity, Soul, Self, Owner, Memories, Drives, Goals, and pending Intention semantics match the exported Doll")

	// ── AC12: Core-local artifacts are not expected to survive ──
	// The source DB was deleted. The destination DB has its own row IDs,
	// internal timestamps, and SQLite representation. We do not compare
	// those — only the semantic Doll State matters.
	t.Log("AC12 ✓ — Core-local artifacts are not expected to survive")

	// ── Final ──
	t.Log("\n🎯 Milestone 11 Phase 3 — ALL ACCEPTANCE CRITERIA PASS — Spark survives destruction!")
}
