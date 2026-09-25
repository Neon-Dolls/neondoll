package pulse

import (
	"testing"
	"time"
)

func TestEvaluate_NormalAdvance(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	prev := time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC)
	prevResult := PulseResult{
		TickIndex: 1,
		At:        prev,
		Subjects:  []PulseSubject{"temporal"},
	}

	result := Evaluate(clock, prevResult)
	if result.TickIndex != 2 {
		t.Errorf("expected TickIndex 2, got %d", result.TickIndex)
	}
	if result.BackwardsTime {
		t.Error("expected no backwards time")
	}
	if len(result.Subjects) != 1 {
		t.Errorf("expected 1 subject, got %d", len(result.Subjects))
	}
}

func TestEvaluate_BackwardsTime(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC))
	prev := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	prevResult := PulseResult{
		TickIndex: 5,
		At:        prev,
	}

	result := Evaluate(clock, prevResult)
	if !result.BackwardsTime {
		t.Fatal("expected backwards time detection")
	}
	if len(result.Observations) < 2 {
		t.Errorf("expected at least 2 observations (normal + warning), got %d", len(result.Observations))
	}
}

func TestEvaluate_FirstTick_NoBackwards(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	result := Evaluate(clock, PulseResult{})
	if result.BackwardsTime {
		t.Error("first tick should not detect backwards time")
	}
	if result.TickIndex != 1 {
		t.Errorf("expected TickIndex 1, got %d", result.TickIndex)
	}
}

func TestEvaluate_Deterministic(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	prev := PulseResult{TickIndex: 3, At: clock.Now().Add(-time.Second)}

	r1 := Evaluate(clock, prev)
	r2 := Evaluate(clock, prev)

	if r1.TickIndex != r2.TickIndex {
		t.Errorf("TickIndex mismatch: %d vs %d", r1.TickIndex, r2.TickIndex)
	}
	if !r1.At.Equal(r2.At) {
		t.Errorf("At mismatch: %v vs %v", r1.At, r2.At)
	}
	if len(r1.Observations) != len(r2.Observations) {
		t.Errorf("Observations length mismatch: %d vs %d", len(r1.Observations), len(r2.Observations))
	}
}
