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

// TestReplacement verifies saving updated state for the same DollID replaces
// the existing record without creating a second Doll.
func TestReplacement(t *testing.T) {
	path, cleanup := tempDB(t)
	defer cleanup()

	ctx := context.Background()
	s := openStore(t, path)

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

	// Verify only one Doll exists by checking that loading a different ID fails.
	_, err = s.LoadDoll(ctx, "some-other-id")
	if err == nil {
		t.Error("expected error for non-existent doll after replacement")
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
