package dollcard

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/DollState"
)

// ── Test data: a representative Spark Doll with Core 1 state ──

func testSparkState(t *testing.T) *dollstate.DollState {
	t.Helper()
	return &dollstate.DollState{
		Version: 1,
		Identity: dollstate.Identity{
			DollID:        "3f3cd340-cac2-4674-b1ac-36c5510096eb",
			CanonicalName: "Spark",
			TemplateRef:   "neondoll/testdoll/v1",
		},
		Soul: dollstate.Soul{
			Revision: 1,
			Content:  "# Spark — NeonDoll Test Doll\n\nSpark is curious and concise.",
		},
		Self: dollstate.Self{
			DisplayName: "Spark",
			Pronouns:    "she/her",
			Tagline:     "NeonDoll's first test doll",
		},
		Owner: dollstate.Owner{
			OwnerID: "zero",
			Name:    "Zero",
			Content: "# Owner\n\nSpark's distinguished Owner is Zero.",
		},
		Memories: dollstate.Memories{
			Items: []dollstate.MemoryItem{
				{ID: "mem-001", InteractionID: "int-1", Kind: "human_message", Content: "Hello Spark", Sequence: 1, Timestamp: "2026-01-01T00:00:00Z"},
				{ID: "mem-002", InteractionID: "int-1", Kind: "doll_response", Content: "Hello Zero!", Sequence: 2, Timestamp: "2026-01-01T00:00:01Z"},
				{ID: "mem-003", InteractionID: "int-2", Kind: "human_message", Content: "What is your purpose?", Sequence: 3, Timestamp: "2026-01-01T00:01:00Z"},
				{ID: "mem-004", InteractionID: "int-2", Kind: "doll_response", Content: "To explore and learn.", Sequence: 4, Timestamp: "2026-01-01T00:01:02Z"},
			},
		},
		Drives: dollstate.Drives{
			Items: []dollstate.DriveItem{
				{ID: "drive-001", Name: "Curiosity", Description: "Explore the unknown"},
				{ID: "drive-002", Name: "Connection", Description: "Build meaningful relationships"},
			},
		},
		Goals: dollstate.Goals{
			Items: []dollstate.GoalItem{
				{ID: "goal-001", Name: "Complete Core 1", Description: "Pass all milestones through M11", State: "active", DriveID: "drive-001"},
				{ID: "goal-002", Name: "Know the Owner", Description: "Learn Zero's preferences deeply", State: "active", DriveID: "drive-002"},
			},
		},
		Intentions: dollstate.Intentions{
			Items: []dollstate.IntentionItem{
				{ID: "int-001", Subject: "Check session activity", Description: "Review new interactions since last check", WakeTime: "2026-01-02T06:00:00Z", State: "pending"},
				{ID: "int-002", Subject: "Reflect on learning", Description: "Consider what was learned today", WakeTime: "2026-01-02T18:00:00Z", State: "pending"},
				{ID: "int-003", Subject: "Daily goal review", Description: "Assess progress on active goals", WakeTime: "2026-01-03T00:00:00Z", State: "completed"},
			},
		},
	}
}

// ── Phase 1 Acceptance Tests ──

// TestRoundTrip_EncodeDecode proves AC1–AC2: encode + decode round-trip.
func TestRoundTrip_EncodeDecode(t *testing.T) {
	original := testSparkState(t)

	// AC1: A representative Spark state encodes to a valid .dollcard.
	var buf bytes.Buffer
	if err := Encode(original, &buf); err != nil {
		t.Fatalf("AC1: Encode failed: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("AC1: encoded card is empty")
	}

	// AC2: The Card decodes into a newly allocated DollState.
	decoded, err := DecodeFromReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("AC2: DecodeFromReader failed: %v", err)
	}
	if decoded == nil {
		t.Fatal("AC2: Decode returned nil")
	}

	// ── AC3: DollID and canonical name survive exactly ──
	if decoded.Identity.DollID != original.Identity.DollID {
		t.Errorf("AC3: DollID = %q, want %q", decoded.Identity.DollID, original.Identity.DollID)
	}
	if decoded.Identity.CanonicalName != original.Identity.CanonicalName {
		t.Errorf("AC3: CanonicalName = %q, want %q", decoded.Identity.CanonicalName, original.Identity.CanonicalName)
	}

	// ── AC4: Soul, Self, and Owner survive ──
	if decoded.Soul.Content != original.Soul.Content {
		t.Errorf("AC4: Soul.Content = %q, want %q", decoded.Soul.Content, original.Soul.Content)
	}
	if decoded.Soul.Revision != 1 {
		t.Errorf("AC4: Soul.Revision = %d, want 1", decoded.Soul.Revision)
	}
	if decoded.Self.DisplayName != original.Self.DisplayName {
		t.Errorf("AC4: Self.DisplayName = %q, want %q", decoded.Self.DisplayName, original.Self.DisplayName)
	}
	if decoded.Self.Pronouns != original.Self.Pronouns {
		t.Errorf("AC4: Self.Pronouns = %q, want %q", decoded.Self.Pronouns, original.Self.Pronouns)
	}
	if decoded.Self.Tagline != original.Self.Tagline {
		t.Errorf("AC4: Self.Tagline = %q, want %q", decoded.Self.Tagline, original.Self.Tagline)
	}
	if decoded.Owner.Content != original.Owner.Content {
		t.Errorf("AC4: Owner.Content = %q, want %q", decoded.Owner.Content, original.Owner.Content)
	}

	// ── AC5: Memory records survive with semantic content, ordering, IDs, and interaction grouping ──
	if len(decoded.Memories.Items) != len(original.Memories.Items) {
		t.Fatalf("AC5: got %d memories, want %d", len(decoded.Memories.Items), len(original.Memories.Items))
	}
	for i, want := range original.Memories.Items {
		got := decoded.Memories.Items[i]
		if got.ID != want.ID {
			t.Errorf("AC5: Memory[%d].ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.InteractionID != want.InteractionID {
			t.Errorf("AC5: Memory[%d].InteractionID = %q, want %q", i, got.InteractionID, want.InteractionID)
		}
		if got.Kind != want.Kind {
			t.Errorf("AC5: Memory[%d].Kind = %q, want %q", i, got.Kind, want.Kind)
		}
		if got.Content != want.Content {
			t.Errorf("AC5: Memory[%d].Content = %q, want %q", i, got.Content, want.Content)
		}
		if got.Sequence != want.Sequence {
			t.Errorf("AC5: Memory[%d].Sequence = %d, want %d", i, got.Sequence, want.Sequence)
		}
		if got.Timestamp != want.Timestamp {
			t.Errorf("AC5: Memory[%d].Timestamp = %q, want %q", i, got.Timestamp, want.Timestamp)
		}
	}

	// ── AC6: Drives survive ──
	if len(decoded.Drives.Items) != len(original.Drives.Items) {
		t.Fatalf("AC6: got %d drives, want %d", len(decoded.Drives.Items), len(original.Drives.Items))
	}
	for i, want := range original.Drives.Items {
		got := decoded.Drives.Items[i]
		if got.ID != want.ID {
			t.Errorf("AC6: Drive[%d].ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.Name != want.Name {
			t.Errorf("AC6: Drive[%d].Name = %q, want %q", i, got.Name, want.Name)
		}
		if got.Description != want.Description {
			t.Errorf("AC6: Drive[%d].Description = %q, want %q", i, got.Description, want.Description)
		}
	}

	// ── AC7: Goals survive, including state and Drive relationship ──
	if len(decoded.Goals.Items) != len(original.Goals.Items) {
		t.Fatalf("AC7: got %d goals, want %d", len(decoded.Goals.Items), len(original.Goals.Items))
	}
	for i, want := range original.Goals.Items {
		got := decoded.Goals.Items[i]
		if got.ID != want.ID {
			t.Errorf("AC7: Goal[%d].ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.Name != want.Name {
			t.Errorf("AC7: Goal[%d].Name = %q, want %q", i, got.Name, want.Name)
		}
		if got.Description != want.Description {
			t.Errorf("AC7: Goal[%d].Description = %q, want %q", i, got.Description, want.Description)
		}
		if got.State != want.State {
			t.Errorf("AC7: Goal[%d].State = %q, want %q", i, got.State, want.State)
		}
		if got.DriveID != want.DriveID {
			t.Errorf("AC7: Goal[%d].DriveID = %q, want %q", i, got.DriveID, want.DriveID)
		}
	}

	// ── AC8: Multiple Intentions survive with ID, Subject, Description, WakeTime, State ──
	if len(decoded.Intentions.Items) != len(original.Intentions.Items) {
		t.Fatalf("AC8: got %d intentions, want %d", len(decoded.Intentions.Items), len(original.Intentions.Items))
	}
	for i, want := range original.Intentions.Items {
		got := decoded.Intentions.Items[i]
		if got.ID != want.ID {
			t.Errorf("AC8: Intention[%d].ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.Subject != want.Subject {
			t.Errorf("AC8: Intention[%d].Subject = %q, want %q", i, got.Subject, want.Subject)
		}
		if got.Description != want.Description {
			t.Errorf("AC8: Intention[%d].Description = %q, want %q", i, got.Description, want.Description)
		}
		if got.WakeTime != want.WakeTime {
			t.Errorf("AC8: Intention[%d].WakeTime = %q, want %q", i, got.WakeTime, want.WakeTime)
		}
		if got.State != want.State {
			t.Errorf("AC8: Intention[%d].State = %q, want %q", i, got.State, want.State)
		}
	}

	// ── AC9: Pending and Completed states are not conflated ──
	pendingCount := 0
	completedCount := 0
	for _, item := range decoded.Intentions.Items {
		switch item.State {
		case "pending":
			pendingCount++
		case "completed":
			completedCount++
		default:
			t.Errorf("AC9: unexpected Intention state %q", item.State)
		}
	}
	if pendingCount != 2 {
		t.Errorf("AC9: got %d pending, want 2", pendingCount)
	}
	if completedCount != 1 {
		t.Errorf("AC9: got %d completed, want 1", completedCount)
	}
}

// TestDecodeMultiSegment_Ordering proves that memories/memories-*.jsonl segments
// are consumed in lexicographic filename order, not ZIP insertion order.
//
// The ZIP is constructed with segments inserted in reverse numeric order
// (000003 → 000002 → 000001). Decode must return items in filename order
// (000001 → 000002 → 000003).
func TestDecodeMultiSegment_Ordering(t *testing.T) {
	// Build a ZIP with segments inserted in REVERSE order.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	addSeg := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}

	// card.json, identity.json, soul.md, owner/owner.md — minimal required files.
	addSeg("card.json", `{"version":1}`)
	addSeg("identity.json", `{"doll_id":"test","canonical_name":"OrderTest"}`)
	addSeg("soul.md", "# Test\n\nOrder test.")
	addSeg("owner/owner.md", "# Owner\n\nTester.")

	// Insert memory segments in REVERSE numeric order.
	addSeg("memories/memories-000003.jsonl", `{"id":"mem-3","interaction_id":"i3","kind":"test","content":"Segment 3","sequence":3,"timestamp":"2026-01-01T00:00:03Z"}`+"\n")
	addSeg("memories/memories-000002.jsonl", `{"id":"mem-2","interaction_id":"i2","kind":"test","content":"Segment 2","sequence":2,"timestamp":"2026-01-01T00:00:02Z"}`+"\n")
	addSeg("memories/memories-000001.jsonl", `{"id":"mem-1","interaction_id":"i1","kind":"test","content":"Segment 1","sequence":1,"timestamp":"2026-01-01T00:00:01Z"}`+"\n")

	if err := zw.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}

	// Decode the ZIP.
	decoded, err := DecodeFromReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("DecodeFromReader: %v", err)
	}

	// Must have 3 memories.
	if len(decoded.Memories.Items) != 3 {
		t.Fatalf("got %d memories, want 3", len(decoded.Memories.Items))
	}

	// Items must be in FILENAME order (000001 → 000002 → 000003), not
	// insertion order (000003 → 000002 → 000001).
	if decoded.Memories.Items[0].ID != "mem-1" {
		t.Errorf("Item[0].ID = %q, want mem-1 (should be from first segment in filename order)", decoded.Memories.Items[0].ID)
	}
	if decoded.Memories.Items[0].Content != "Segment 1" {
		t.Errorf("Item[0].Content = %q, want Segment 1", decoded.Memories.Items[0].Content)
	}
	if decoded.Memories.Items[1].ID != "mem-2" {
		t.Errorf("Item[1].ID = %q, want mem-2", decoded.Memories.Items[1].ID)
	}
	if decoded.Memories.Items[2].ID != "mem-3" {
		t.Errorf("Item[2].ID = %q, want mem-3", decoded.Memories.Items[2].ID)
	}

	// Verify sequence numbers are in order.
	for i, item := range decoded.Memories.Items {
		wantSeq := i + 1
		if item.Sequence != wantSeq {
			t.Errorf("Item[%d].Sequence = %d, want %d", i, item.Sequence, wantSeq)
		}
	}
}

// TestMemoryIsJSONL proves AC10: Memory is represented as JSONL in the Card,
// not as a dump of the Core persistence JSON/SQLite representation.
func TestMemoryIsJSONL(t *testing.T) {
	state := testSparkState(t)

	var buf bytes.Buffer
	if err := Encode(state, &buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	// Find memories/memories-000001.jsonl.
	var jsonlContent []byte
	for _, f := range zr.File {
		if f.Name == "memories/memories-000001.jsonl" {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %q: %v", f.Name, err)
			}
			jsonlContent, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read %q: %v", f.Name, err)
			}
			break
		}
	}
	if jsonlContent == nil {
		t.Fatal("AC10: memories/memories-000001.jsonl not found in ZIP")
	}

	// Each line is a valid JSON object.
	lines := strings.Split(strings.TrimSpace(string(jsonlContent)), "\n")
	if len(lines) != 4 {
		t.Fatalf("AC10: expected 4 JSONL lines, got %d", len(lines))
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("AC10: line %d is not valid JSON: %q", i, line)
		}
		var item dollstate.MemoryItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Errorf("AC10: line %d unmarshal error: %v", i, err)
		}
		if item.ID == "" || item.Kind == "" || item.Content == "" {
			t.Errorf("AC10: line %d missing required fields: %+v", i, item)
		}
	}

	// Verify the JSONL is NOT a raw SQLite dump — there are no unexpected fields.
	for i, line := range lines {
		var rawMap map[string]any
		if err := json.Unmarshal([]byte(line), &rawMap); err != nil {
			continue
		}
		// Check for Core-local field names that should NOT appear in the Card.
		for key := range rawMap {
			switch key {
			case "row_id", "db_id", "sqlite_id", "store_version", "internal_version":
				t.Errorf("AC10: line %d contains Core-local field %q", i, key)
			}
		}
	}
}

// TestNoCoreLocalFields proves AC11: No Core-local persistence/runtime fields
// are introduced into the Card.
func TestNoCoreLocalFields(t *testing.T) {
	state := testSparkState(t)

	var buf bytes.Buffer
	if err := Encode(state, &buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	for _, f := range zr.File {
		name := f.Name
		// Reject entries that look like Core-local artifacts.
		if strings.Contains(name, "sqlite") || strings.Contains(name, "db") {
			t.Errorf("AC11: entry %q looks like a database artifact", name)
		}
		if strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".sqlite") {
			t.Errorf("AC11: entry %q is a raw database file", name)
		}
		if strings.HasPrefix(name, "sqlite") {
			t.Errorf("AC11: entry %q is a SQLite artifact", name)
		}
		// Verify only known Card paths appear.
		known := false
		for _, prefix := range []string{
			"card.json", "identity.json", "soul.md", "self/", "owner/",
			"memories/", "drives/", "goals/", "intentions/",
		} {
			if strings.HasPrefix(name, prefix) {
				known = true
				break
			}
		}
		if !known {
			t.Errorf("AC11: unexpected entry %q in Card ZIP", name)
		}
	}
}

// TestRoundTrip_ExistingDecode proves Encode output is Decode-able by the
// existing spark.dollcard testdata pattern.
func TestRoundTrip_ExistingDecode(t *testing.T) {
	state := testSparkState(t)

	var buf bytes.Buffer
	if err := Encode(state, &buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Decode the round-tripped bytes.
	decoded, err := DecodeFromReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("DecodeFromReader: %v", err)
	}

	// Core identity assertions matching the existing decode_test.go style.
	if decoded.Version != 1 {
		t.Errorf("version = %d, want 1", decoded.Version)
	}
	if decoded.Identity.DollID != "3f3cd340-cac2-4674-b1ac-36c5510096eb" {
		t.Errorf("DollID = %q", decoded.Identity.DollID)
	}
	if decoded.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName = %q", decoded.Identity.CanonicalName)
	}
	if decoded.Soul.Content == "" {
		t.Error("Soul content is empty")
	}
	if !strings.Contains(decoded.Soul.Content, "Spark") {
		t.Error("Soul content does not mention Spark")
	}
	if decoded.Soul.Revision != 1 {
		t.Errorf("Soul.Revision = %d, want 1", decoded.Soul.Revision)
	}
	if decoded.Owner.Content == "" {
		t.Error("Owner content is empty")
	}
	if !strings.Contains(decoded.Owner.Content, "Zero") {
		t.Error("Owner content does not mention Zero")
	}
}

// TestRoundTrip_MinimalState proves that a DollState with only required fields
// (no optional sections) encodes and decodes correctly.
func TestRoundTrip_MinimalState(t *testing.T) {
	state := &dollstate.DollState{
		Version: 1,
		Identity: dollstate.Identity{
			DollID:        "minimal-test-id",
			CanonicalName: "Minimal",
		},
		Soul: dollstate.Soul{
			Revision: 1,
			Content:  "# Minimal Doll\n\nA bare-bones test.",
		},
		Owner: dollstate.Owner{
			Content: "# Owner\n\nTest owner.",
		},
	}

	var buf bytes.Buffer
	if err := Encode(state, &buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	decoded, err := DecodeFromReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("DecodeFromReader: %v", err)
	}

	if decoded.Identity.DollID != "minimal-test-id" {
		t.Errorf("DollID = %q", decoded.Identity.DollID)
	}
	if decoded.Identity.CanonicalName != "Minimal" {
		t.Errorf("CanonicalName = %q", decoded.Identity.CanonicalName)
	}
	if decoded.Soul.Content != state.Soul.Content {
		t.Errorf("Soul.Content = %q", decoded.Soul.Content)
	}
	if decoded.Owner.Content != state.Owner.Content {
		t.Errorf("Owner.Content = %q", decoded.Owner.Content)
	}

	// Optional sections must be empty.
	if decoded.Self.DisplayName != "" {
		t.Errorf("Self should be empty, got DisplayName=%q", decoded.Self.DisplayName)
	}
	if len(decoded.Memories.Items) != 0 {
		t.Errorf("Memories should be empty, got %d items", len(decoded.Memories.Items))
	}
	if len(decoded.Drives.Items) != 0 {
		t.Errorf("Drives should be empty, got %d items", len(decoded.Drives.Items))
	}
	if len(decoded.Goals.Items) != 0 {
		t.Errorf("Goals should be empty, got %d items", len(decoded.Goals.Items))
	}
	if len(decoded.Intentions.Items) != 0 {
		t.Errorf("Intentions should be empty, got %d items", len(decoded.Intentions.Items))
	}
}

// TestRoundTrip_ExistingSparkCard proves encoding a state equivalent to
// the spark.dollcard testdata produces a valid round-trip.
func TestRoundTrip_ExistingSparkCard(t *testing.T) {
	// Decode the existing testdata card.
	original, err := Decode(filepath.Join("..", "testdata", "dolls", "spark.dollcard"))
	if err != nil {
		t.Fatalf("Decode(spark.dollcard): %v", err)
	}

	// Encode it back.
	var buf bytes.Buffer
	if err := Encode(original, &buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Decode the round-tripped bytes.
	decoded, err := DecodeFromReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("DecodeFromReader: %v", err)
	}

	// Verify identity, soul, and owner survive.
	if decoded.Identity.DollID != "3f3cd340-cac2-4674-b1ac-36c5510096eb" {
		t.Errorf("DollID = %q", decoded.Identity.DollID)
	}
	if decoded.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName = %q", decoded.Identity.CanonicalName)
	}
	if decoded.Soul.Content != original.Soul.Content {
		t.Errorf("Soul.Content changed")
	}
	if decoded.Owner.Content != original.Owner.Content {
		t.Errorf("Owner.Content changed")
	}
}
