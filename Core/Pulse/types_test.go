package pulse

import (
	"testing"
	"time"
)

func TestPulseSubject_IsString(t *testing.T) {
	s := PulseSubject("test-subject")
	if string(s) != "test-subject" {
		t.Errorf("expected 'test-subject', got %q", string(s))
	}
}

func TestPulseSnapshot_ZeroValue(t *testing.T) {
	var s PulseSnapshot
	if s.TickCount != 0 {
		t.Errorf("expected 0, got %d", s.TickCount)
	}
	if s.EvaluatedCount != 0 {
		t.Errorf("expected 0, got %d", s.EvaluatedCount)
	}
}

func TestTemporalObservation_RoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	o := TemporalObservation{
		At:        now,
		Subject:   "temporal",
		TickIndex: 42,
	}
	if o.At != now {
		t.Errorf("time mismatch")
	}
	if o.TickIndex != 42 {
		t.Errorf("expected 42, got %d", o.TickIndex)
	}
}

func TestPulseResult_Defaults(t *testing.T) {
	r := PulseResult{}
	if r.BackwardsTime {
		t.Error("expected BackwardsTime false")
	}
	if r.TickIndex != 0 {
		t.Errorf("expected 0, got %d", r.TickIndex)
	}
	if r.Subjects != nil {
		t.Error("expected nil subjects")
	}
}
