package pulse

import (
	"testing"
	"time"
)

func TestPulseSnapshot_ZeroValue(t *testing.T) {
	var s PulseSnapshot
	if s.TickCount != 0 {
		t.Errorf("expected 0, got %d", s.TickCount)
	}
	if !s.LastTickAt.IsZero() {
		t.Errorf("expected zero LastTickAt")
	}
	if !s.LastCognitionAt.IsZero() {
		t.Errorf("expected zero LastCognitionAt")
	}
	if !s.LastSpontaneousWakeAt.IsZero() {
		t.Errorf("expected zero LastSpontaneousWakeAt")
	}
}

func TestPulseSnapshot_Bookkeeping(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	s := PulseSnapshot{
		TickCount:             42,
		LastTickAt:            now,
		LastCognitionAt:       time.Time{},
		LastSpontaneousWakeAt: time.Time{},
	}
	if s.TickCount != 42 {
		t.Errorf("expected 42, got %d", s.TickCount)
	}
	if !s.LastTickAt.Equal(now) {
		t.Errorf("time mismatch")
	}
}

func TestPulseResult_Defaults(t *testing.T) {
	r := PulseResult{}
	if r.BackwardsTime {
		t.Error("expected BackwardsTime false")
	}
	if r.TickCount != 0 {
		t.Errorf("expected 0, got %d", r.TickCount)
	}
	if !r.At.IsZero() {
		t.Error("expected zero At")
	}
}

func TestPulseResult_BackwardsTime(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	r := PulseResult{
		TickCount:     5,
		At:            now,
		BackwardsTime: true,
	}
	if !r.BackwardsTime {
		t.Error("expected BackwardsTime true")
	}
	if r.TickCount != 5 {
		t.Errorf("expected 5, got %d", r.TickCount)
	}
}
