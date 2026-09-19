package dollcard

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeSpark(t *testing.T) {
	state, err := Decode(filepath.Join("..", "testdata", "dolls", "spark.dollcard"))
	if err != nil {
		t.Fatalf("Decode(spark.dollcard) failed: %v", err)
	}

	// 1. Version is accepted.
	if state.Version != 1 {
		t.Errorf("version = %d, want 1", state.Version)
	}

	// 2. Doll ID is present and stable.
	const wantID = "3f3cd340-cac2-4674-b1ac-36c5510096eb"
	if state.Identity.DollID != wantID {
		t.Errorf("DollID = %q, want %q", state.Identity.DollID, wantID)
	}

	// 3. Canonical name is Spark.
	if state.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName = %q, want %q", state.Identity.CanonicalName, "Spark")
	}

	// 4. Soul content is loaded.
	if state.Soul.Content == "" {
		t.Error("Soul content is empty")
	}
	if !strings.Contains(state.Soul.Content, "Spark") {
		t.Error("Soul content does not mention Spark")
	}
	if state.Soul.Revision != 1 {
		t.Errorf("Soul.Revision = %d, want 1", state.Soul.Revision)
	}

	// 5. Owner information is loaded.
	if state.Owner.Content == "" {
		t.Error("Owner content is empty")
	}
	if !strings.Contains(state.Owner.Content, "Zero") {
		t.Error("Owner content does not mention Zero")
	}
}

func TestDecodeSparkFromReader(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "dolls", "spark.dollcard"))
	if err != nil {
		t.Fatalf("ReadFile(spark.dollcard): %v", err)
	}
	state, err := DecodeFromReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("DecodeFromReader(spark.dollcard) failed: %v", err)
	}
	if state.Identity.DollID != "3f3cd340-cac2-4674-b1ac-36c5510096eb" {
		t.Errorf("DollID = %q", state.Identity.DollID)
	}
	if state.Identity.CanonicalName != "Spark" {
		t.Errorf("CanonicalName = %q", state.Identity.CanonicalName)
	}
}

func TestDecodeMissingCard(t *testing.T) {
	_, err := Decode(filepath.Join("..", "testdata", "dolls", "missing_card.dollcard"))
	if err == nil {
		t.Fatal("expected error for missing card.json, got nil")
	}
	if !strings.Contains(err.Error(), "missing required file") {
		t.Errorf("error = %q, want 'missing required file'", err)
	}
}

func TestDecodeMissingIdentity(t *testing.T) {
	_, err := Decode(filepath.Join("..", "testdata", "dolls", "missing_identity.dollcard"))
	if err == nil {
		t.Fatal("expected error for missing identity.json, got nil")
	}
	if !strings.Contains(err.Error(), "missing required file") {
		t.Errorf("error = %q, want 'missing required file'", err)
	}
}

func TestDecodeBadVersion(t *testing.T) {
	_, err := Decode(filepath.Join("..", "testdata", "dolls", "bad_version.dollcard"))
	if err == nil {
		t.Fatal("expected error for bad version, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported card version") {
		t.Errorf("error = %q, want 'unsupported card version'", err)
	}
}

func TestDecodeEmptyID(t *testing.T) {
	_, err := Decode(filepath.Join("..", "testdata", "dolls", "empty_id.dollcard"))
	if err == nil {
		t.Fatal("expected error for empty doll_id, got nil")
	}
	if !strings.Contains(err.Error(), "missing doll_id") {
		t.Errorf("error = %q, want 'missing doll_id'", err)
	}
}

func TestDecodeMalformedJSON(t *testing.T) {
	_, err := Decode(filepath.Join("..", "testdata", "dolls", "malformed_json.dollcard"))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestDecodeNotAZIP(t *testing.T) {
	_, err := Decode(filepath.Join("..", "testdata", "dolls", "not_a_zip.dollcard"))
	if err == nil {
		t.Fatal("expected error for non-zip, got nil")
	}
}

func TestDecodeNonexistentFile(t *testing.T) {
	_, err := Decode(filepath.Join("..", "testdata", "dolls", "does_not_exist.dollcard"))
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

// TestSparkIsARealZIP verifies the card is a normal, inspectable ZIP archive.
func TestSparkIsARealZIP(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "dolls", "spark.dollcard"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	names := make(map[string]bool, len(r.File))
	for _, f := range r.File {
		names[f.Name] = true
	}
	for _, want := range []string{"card.json", "identity.json", "soul.md", "owner/owner.md"} {
		if !names[want] {
			t.Errorf("missing entry %q in ZIP", want)
		}
	}
}

// TestSparkSoulSize checks soul is ~20 lines, not oversized.
func TestSparkSoulSize(t *testing.T) {
	state, err := Decode(filepath.Join("..", "testdata", "dolls", "spark.dollcard"))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	lines := strings.Count(state.Soul.Content, "\n") + 1
	if lines > 30 {
		t.Errorf("Soul is %d lines, expected ~20", lines)
	}
}