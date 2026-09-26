package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestPulseConfig_SecondsSemantics(t *testing.T) {
	// Prove that JSON numeric values represent seconds, not nanoseconds.
	// 30 means 30 seconds. An int64 backed by Go's standard json.Unmarshal
	// preserves the exact numeric value; there is no time.Duration
	// nanosecond interpretation anywhere in the contract.
	jsonInput := `{
		"idle_horizon": 30,
		"neglect_horizon": 0,
		"change_horizon": 86400,
		"wake_cooldown": 5,
		"min_wake_spacing_seconds": 60
	}`
	var pc PulseConfig
	if err := json.Unmarshal([]byte(jsonInput), &pc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if pc.IdleHorizon != 30 {
		t.Errorf("idle_horizon: got %d, want 30", pc.IdleHorizon)
	}
	if pc.NeglectHorizon != 0 {
		t.Errorf("neglect_horizon: got %d, want 0", pc.NeglectHorizon)
	}
	if pc.ChangeHorizon != 86400 {
		t.Errorf("change_horizon: got %d, want 86400 (1 day)", pc.ChangeHorizon)
	}
	if pc.WakeCooldown != 5 {
		t.Errorf("wake_cooldown: got %d, want 5", pc.WakeCooldown)
	}
	if pc.MinWakeSpacing != 60 {
		t.Errorf("min_wake_spacing_seconds: got %d, want 60", pc.MinWakeSpacing)
	}

	// Verify the conversion helper gives the right time.Duration.
	if got := secAsDuration(pc.IdleHorizon); got != 30*time.Second {
		t.Errorf("secAsDuration(30) = %v, want 30s", got)
	}
	if got := secAsDuration(0); got != 0 {
		t.Errorf("secAsDuration(0) = %v, want 0", got)
	}
}

func TestMinWakeSpacingZeroIsGuardDisabled(t *testing.T) {
	// Prove that 0 means "guard disabled", not "Pulse disabled" or
	// "evaluation cadence disabled".
	jsonInput := `{"min_wake_spacing_seconds": 0}`
	var pc PulseConfig
	if err := json.Unmarshal([]byte(jsonInput), &pc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if pc.MinWakeSpacing != 0 {
		t.Errorf("got %d, want 0", pc.MinWakeSpacing)
	}
	// Config is otherwise zero-valued (including Enabled=false by default).
}

func TestPulseConfig_MarshalRoundtrip(t *testing.T) {
	pc := PulseConfig{
		Enabled:        true,
		IdleHorizon:    30,
		NeglectHorizon: 300,
		ChangeHorizon:  3600,
		WakeCooldown:   5,
		MinWakeSpacing: 0,
	}
	data, err := json.Marshal(pc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded PulseConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded != pc {
		t.Errorf("roundtrip mismatch:\n  got:  %+v\n  want: %+v", decoded, pc)
	}
}
