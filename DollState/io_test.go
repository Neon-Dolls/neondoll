package dollstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadDollState(t *testing.T) {
	dir := t.TempDir()
	s := NewDollState()
	s.Identity = Identity{DollID: "doll-001", CanonicalName: "TestDoll"}
	s.Soul = Soul{Revision: 1, Content: "A test doll."}
	s.Owner = Owner{OwnerID: "user-42", Name: "Zero"}

	path, err := SaveState(dir, &s)
	if err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if loaded.Identity.DollID != "doll-001" {
		t.Errorf("expected doll-001, got %s", loaded.Identity.DollID)
	}
	if loaded.Version != CurrentStateVersion {
		t.Errorf("expected version %d, got %d", CurrentStateVersion, loaded.Version)
	}
}

func TestSaveLoadSecrets(t *testing.T) {
	dir := t.TempDir()
	secrets := &Secrets{
		Items: []SecretItem{
			{Key: "api_key", Value: "sk-123", Source: "env"},
			{Key: "password", Value: "hunter2", Source: "user"},
		},
	}

	path, err := SaveSecrets(dir, secrets)
	if err != nil {
		t.Fatalf("SaveSecrets: %v", err)
	}

	loaded, err := LoadSecrets(path)
	if err != nil {
		t.Fatalf("LoadSecrets: %v", err)
	}

	if len(loaded.Items) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(loaded.Items))
	}
	if loaded.Items[0].Key != "api_key" {
		t.Errorf("expected api_key, got %s", loaded.Items[0].Key)
	}
}

func TestSaveStateInvalidDir(t *testing.T) {
	_, err := SaveState("/nonexistent/path", &DollState{Version: 1})
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

func TestLoadStateInvalidPath(t *testing.T) {
	_, err := LoadState("/nonexistent/dollstate.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadSecretsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	os.WriteFile(path, []byte{}, 0644)
	s, err := LoadSecrets(path)
	if err != nil {
		t.Fatalf("LoadSecrets on empty file: %v", err)
	}
	if len(s.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(s.Items))
	}
}
