package events

import (
	"testing"
	"time"
)

func TestNewEvent(t *testing.T) {
	e := New("evt-1", TypeMessage, "discord", MessagePayload{
		Text: "Hello!", ChannelID: "ch-1", UserID: "usr-1",
	})
	if e.ID != "evt-1" {
		t.Errorf("expected evt-1, got %s", e.ID)
	}
	if e.Type != TypeMessage {
		t.Errorf("expected TypeMessage, got %v", e.Type)
	}
	if e.Source != "discord" {
		t.Errorf("expected discord, got %s", e.Source)
	}
	if e.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	payload, ok := e.Payload.(MessagePayload)
	if !ok {
		t.Fatal("expected MessagePayload")
	}
	if payload.Text != "Hello!" {
		t.Errorf("expected 'Hello!', got %q", payload.Text)
	}
}

func TestParseType(t *testing.T) {
	tests := []struct {
		input string
		want  Type
	}{
		{"message", TypeMessage},
		{"command", TypeCommand},
		{"system", TypeSystem},
		{"presence", TypePresence},
		{"unknown", TypeUnknown},
		{"anything_else", TypeUnknown},
	}
	for _, tt := range tests {
		if got := ParseType(tt.input); got != tt.want {
			t.Errorf("ParseType(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestEventTimestampIsRecent(t *testing.T) {
	before := time.Now().UTC()
	e := New("evt-2", TypeSystem, "core", SystemPayload{Event: "startup"})
	after := time.Now().UTC()
	if e.Timestamp.Before(before) || e.Timestamp.After(after) {
		t.Error("timestamp outside expected range")
	}
}

func TestCommandPayload(t *testing.T) {
	p := CommandPayload{
		Command: "ping",
		Args:    []string{"arg1"},
		Flags:   map[string]string{"verbose": "true"},
	}
	if p.Command != "ping" {
		t.Errorf("expected ping, got %s", p.Command)
	}
}

func TestPresencePayload(t *testing.T) {
	p := PresencePayload{UserID: "usr-1", Status: "online"}
	if p.Status != "online" {
		t.Errorf("expected online, got %s", p.Status)
	}
}