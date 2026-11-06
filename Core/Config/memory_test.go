package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMemoryProviderSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "neondoll.json")
	data := `{"memory":{"active":"primary","providers":{"primary":{"instance_id":"memory-1","contract_version":1}}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := cfg.Memory.ResolveActive()
	if err != nil || !ok || got.InstanceID != "memory-1" {
		t.Fatalf("resolve: %+v %v %v", got, ok, err)
	}
}

func TestLoadRejectsUnknownMemoryProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "neondoll.json")
	if err := os.WriteFile(path, []byte(`{"memory":{"active":"missing","providers":{}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "config memory") {
		t.Fatalf("load: %v", err)
	}
}
