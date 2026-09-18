package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Core.Profile != "default" {
		t.Errorf("expected default profile, got %s", cfg.Core.Profile)
	}
	if cfg.Console.Prompt != "> " {
		t.Errorf("expected prompt '> ', got %q", cfg.Console.Prompt)
	}
	if cfg.HTTP.Listen != "127.0.0.1:8080" {
		t.Errorf("expected 127.0.0.1:8080, got %s", cfg.HTTP.Listen)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "neondoll.json")
	cfg := Defaults()
	cfg.Core.Profile = "testing"

	if err := Save(path, &cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("saved file doesn't exist")
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Core.Profile != "testing" {
		t.Errorf("expected profile 'testing', got %q", loaded.Core.Profile)
	}
}

func TestLoadNonexistent(t *testing.T) {
	_, err := Load("/nonexistent/neondoll.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestFindConfigNotFound(t *testing.T) {
	_, err := FindConfig()
	if err == nil {
		t.Fatal("expected error when no config exists")
	}
}

func TestFindConfigFound(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	cfg := Defaults()
	if err := Save("neondoll.json", &cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := FindConfig()
	if err != nil {
		t.Fatalf("FindConfig: %v", err)
	}
	if path != filepath.Join(dir, "neondoll.json") {
		t.Errorf("unexpected path: %s", path)
	}
}