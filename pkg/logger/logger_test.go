package logger

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{DebugLevel, "debug"},
		{InfoLevel, "info"},
		{WarnLevel, "warn"},
		{ErrorLevel, "error"},
		{Level(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestLogLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := New(InfoLevel, &buf)

	log.Debug("should be hidden")
	log.Info("visible info")
	log.Warn("visible warn")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d: %s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "visible info") {
		t.Errorf("first line missing 'visible info': %s", lines[0])
	}
}

func TestLogOutput(t *testing.T) {
	var buf bytes.Buffer
	log := New(DebugLevel, &buf)
	log.Info("hello", map[string]any{"key": "value"})

	var entry logEntry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	if entry.Message != "hello" {
		t.Errorf("expected message 'hello', got %q", entry.Message)
	}
	if entry.Level != "info" {
		t.Errorf("expected level 'info', got %q", entry.Level)
	}
	if entry.Fields["key"] != "value" {
		t.Errorf("expected field key=value, got %v", entry.Fields)
	}
	if entry.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestNilOutputDefaultsToStdout(t *testing.T) {
	log := New(DebugLevel, nil)
	if log.output == nil {
		t.Error("expected output to be non-nil default")
	}
}

func TestMergeFields(t *testing.T) {
	result := merge(
		map[string]any{"a": 1},
		map[string]any{"b": 2},
	)
	if result["a"] != 1 || result["b"] != 2 {
		t.Errorf("merge failed: got %v", result)
	}
}

func TestMergeEmpty(t *testing.T) {
	if got := merge(); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}
