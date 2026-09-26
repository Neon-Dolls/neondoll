package pulse

import (
	"testing"
	"time"
)

func TestEvaluate_First(t *testing.T) {
	fc := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))

	prev := PulseSnapshot{}
	result := Evaluate(fc, prev)

	if result.TickCount != 1 {
		t.Errorf("expected TickCount=1, got %d", result.TickCount)
	}
	if result.BackwardsTime {
		t.Errorf("expected BackwardsTime=false on first eval")
	}
}

func TestEvaluate_Repeated(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fc := NewFakeClock(now)

	// First eval
	prev := PulseSnapshot{}
	r1 := Evaluate(fc, prev)

	// Advance clock
	fc.(*fakeClock).Advance(5 * time.Second)

	// Second eval
	prev2 := PulseSnapshot{
		TickCount:  r1.TickCount,
		LastTickAt: r1.At,
	}
	r2 := Evaluate(fc, prev2)

	if r2.TickCount != 2 {
		t.Errorf("expected TickCount=2, got %d", r2.TickCount)
	}
	if r2.BackwardsTime {
		t.Errorf("expected BackwardsTime=false")
	}
}

func TestEvaluate_SameTime(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fc := NewFakeClock(now)

	prev := PulseSnapshot{}
	r1 := Evaluate(fc, prev)

	// Same time — no clock advance
	prev2 := PulseSnapshot{
		TickCount:  r1.TickCount,
		LastTickAt: r1.At,
	}
	r2 := Evaluate(fc, prev2)

	if r2.TickCount != 2 {
		t.Errorf("expected TickCount=2 (same-time valid), got %d", r2.TickCount)
	}
	if r2.BackwardsTime {
		t.Errorf("expected BackwardsTime=false for same-time")
	}
}

func TestEvaluate_BackwardsTime(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fc := NewFakeClock(now)

	// First eval at T
	prev := PulseSnapshot{}
	r1 := Evaluate(fc, prev)

	// Advance
	fc.(*fakeClock).Advance(10 * time.Second)
	prev2 := PulseSnapshot{
		TickCount:  r1.TickCount,
		LastTickAt: r1.At,
	}
	r2 := Evaluate(fc, prev2)

	// Now go BACKWARDS
	fc.(*fakeClock).Set(now)
	prev3 := PulseSnapshot{
		TickCount:  r2.TickCount,
		LastTickAt: r2.At,
	}
	r3 := Evaluate(fc, prev3)

	if r3.TickCount != r2.TickCount {
		t.Errorf("expected TickCount to stay at %d (not incremented), got %d",
			r2.TickCount, r3.TickCount)
	}
	if !r3.BackwardsTime {
		t.Errorf("expected BackwardsTime=true")
	}
}

func TestEvaluate_BackwardsTimeFromZero(t *testing.T) {
	// No previous tick — first eval is never backwards
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fc := NewFakeClock(now)

	prev := PulseSnapshot{}
	result := Evaluate(fc, prev)

	if result.BackwardsTime {
		t.Errorf("first eval from zero must not be backwards")
	}
	if result.TickCount != 1 {
		t.Errorf("expected TickCount=1, got %d", result.TickCount)
	}
}

func TestEvaluate_DeterministicTimestamps(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fc := NewFakeClock(now)

	result := Evaluate(fc, PulseSnapshot{})
	if !result.At.Equal(now) {
		t.Errorf("expected exact timestamp: %v, got %v", now, result.At)
	}

	fc.(*fakeClock).Advance(1234 * time.Millisecond)
	result2 := Evaluate(fc, PulseSnapshot{
		TickCount:  result.TickCount,
		LastTickAt: result.At,
	})
	expected := now.Add(1234 * time.Millisecond)
	if !result2.At.Equal(expected) {
		t.Errorf("expected exact timestamp: %v, got %v", expected, result2.At)
	}
}
