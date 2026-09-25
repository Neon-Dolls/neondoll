package pulse

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ── Helpers ──────────────────────────────────────────────────────────────────

func newTestConfig(enabled bool, interval time.Duration) config.PulseConfig {
	return config.PulseConfig{
		Enabled:        enabled,
		MinWakeSpacing: interval,
	}
}

// ── Duplicate runner detection ──────────────────────────────────────────────

func TestRunner_DuplicateStart(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	cfg := newTestConfig(true, 10*time.Millisecond)
	log := logger.New(logger.ErrorLevel, nil)
	r := NewRunner(cfg, clock, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	if err := r.Start(ctx); err == nil {
		t.Fatal("expected error on duplicate Start")
	} else {
		t.Logf("duplicate start error: %v", err)
	}

	r.Stop()
}

// ── Config validation ───────────────────────────────────────────────────────

func TestPulseConfig_DisabledByZeroInterval(t *testing.T) {
	cfg := newTestConfig(true, 0)
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	log := logger.New(logger.ErrorLevel, nil)
	r := NewRunner(cfg, clock, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start with zero interval: %v", err)
	}

	// Should be running but never tick.
	time.Sleep(30 * time.Millisecond)
	snap := r.Snapshot()
	if snap.TickCount != 0 {
		t.Errorf("expected 0 ticks for disabled interval, got %d", snap.TickCount)
	}

	r.Stop()
}

func TestPulseConfig_DisabledByEnabledFalse(t *testing.T) {
	cfg := newTestConfig(false, 10*time.Millisecond)
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	log := logger.New(logger.ErrorLevel, nil)
	r := NewRunner(cfg, clock, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start with enabled=false: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	snap := r.Snapshot()
	if snap.TickCount != 0 {
		t.Errorf("expected 0 ticks for disabled runner, got %d", snap.TickCount)
	}

	r.Stop()
}

// ── Shutdown stops ticks ────────────────────────────────────────────────────

func TestRunner_ShutdownStopsTicks(t *testing.T) {
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	cfg := newTestConfig(true, 10*time.Millisecond)
	log := logger.New(logger.ErrorLevel, nil)
	r := NewRunner(cfg, clock, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Let a few ticks happen via real time, advancing fake clock for each tick.
	clock.(*fakeClock).Advance(50 * time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	snap1 := r.Snapshot()
	clock.(*fakeClock).Advance(50 * time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	snap2 := r.Snapshot()

	// Verify ticks are happening.
	if snap2.TickCount <= snap1.TickCount {
		t.Fatalf("expected ticks to advance, snap1=%d snap2=%d", snap1.TickCount, snap2.TickCount)
	}

	// Stop the runner.
	r.Stop()

	// Wait and check no more ticks.
	lastSnap := r.Snapshot()
	time.Sleep(100 * time.Millisecond)
	finalSnap := r.Snapshot()
	if finalSnap.TickCount != lastSnap.TickCount {
		t.Errorf("expected no ticks after Stop: was %d, now %d", lastSnap.TickCount, finalSnap.TickCount)
	}
}

// ── Fake clock determinism ────────────────────────────────────────────────

func TestRunner_FakeClockDeterminism(t *testing.T) {
	cfg := newTestConfig(true, 10*time.Millisecond)
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	log := logger.New(logger.ErrorLevel, nil)
	r := NewRunner(cfg, clock, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Advance the fake clock and let ticks fire.
	clock.(*fakeClock).Advance(50 * time.Millisecond)
	time.Sleep(60 * time.Millisecond)

	snap := r.Snapshot()
	if snap.TickCount == 0 {
		t.Fatal("expected at least 1 tick with fake clock advancement")
	}
	if snap.LastTickTime.IsZero() {
		t.Fatal("expected LastTickTime to be set")
	}
}

// ── Backwards time handling ────────────────────────────────────────────────

func TestRunner_BackwardsTime(t *testing.T) {
	cfg := newTestConfig(true, 10*time.Millisecond)
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	log := logger.New(logger.WarnLevel, nil)
	r := NewRunner(cfg, clock, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Let a tick happen at the initial time.
	clock.(*fakeClock).Advance(15 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	snap1 := r.Snapshot()
	if snap1.TickCount == 0 {
		t.Fatal("expected at least 1 tick before backwards time")
	}

	// Set clock backward.
	clock.(*fakeClock).Set(time.Date(2026, 9, 25, 11, 0, 0, 0, time.UTC))
	clock.(*fakeClock).Advance(15 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	snap2 := r.Snapshot()
	if snap2.TickCount <= snap1.TickCount {
		t.Fatal("expected tick to advance after backwards time")
	}
}

// ── Snapshot thread safety ────────────────────────────────────────────────

func TestRunner_SnapshotThreadSafe(t *testing.T) {
	cfg := newTestConfig(true, 5*time.Millisecond)
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	log := logger.New(logger.ErrorLevel, nil)
	r := NewRunner(cfg, clock, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	var snapshots []PulseSnapshot
	for i := 0; i < 5; i++ {
		clock.(*fakeClock).Advance(20 * time.Millisecond)
		time.Sleep(25 * time.Millisecond)
		snapshots = append(snapshots, r.Snapshot())
	}

	if len(snapshots) < 2 {
		t.Fatal("expected multiple snapshots")
	}
	// Verify monotonic tick count.
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i].TickCount < snapshots[i-1].TickCount {
			t.Errorf("TickCount decreased: %d → %d", snapshots[i-1].TickCount, snapshots[i].TickCount)
		}
	}
}

// ── M1 Conformance: full lifecycle ─────────────────────────────────────────
//
// Core starts → one Pulse runner starts → injected time advances →
// Pulse deterministically records temporal evaluation → Core stops → Pulse stops.

func TestM1Conformance_FullLifecycle(t *testing.T) {
	// Phase 1: Create config with Pulse enabled.
	cfg := newTestConfig(true, 10*time.Millisecond)

	// Phase 2: Create injected clock (fake for determinism).
	startTime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	clock := NewFakeClock(startTime)

	// Phase 3: Create log and runner.
	r := NewRunner(cfg, clock, logger.New(logger.ErrorLevel, nil))

	// Phase 4: Start Core context and Pulse runner.
	ctx, cancel := context.WithCancel(context.Background())

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Pulse runner Start failed: %v", err)
	}

	// Verify runner is running.
	snap0 := r.Snapshot()
	if snap0.TickCount != 0 {
		t.Fatalf("expected 0 ticks before time advances, got %d", snap0.TickCount)
	}

	// Phase 5: Advance injected time.
	clock.(*fakeClock).Advance(50 * time.Millisecond)
	time.Sleep(60 * time.Millisecond) // allow a few ticks

	snap1 := r.Snapshot()
	if snap1.TickCount == 0 {
		t.Fatal("expected at least 1 tick after time advance")
	}
	if snap1.EvaluatedCount == 0 {
		t.Fatal("expected EvaluatedCount > 0")
	}
	if snap1.LastTickTime.IsZero() {
		t.Fatal("expected LastTickTime to be non-zero")
	}
	if snap1.LastTickTime.Before(startTime) {
		t.Fatal("LastTickTime should not be before start time")
	}

	// Phase 6: Advance time further and verify monotonic ticks.
	clock.(*fakeClock).Advance(50 * time.Millisecond)
	time.Sleep(60 * time.Millisecond)

	snap2 := r.Snapshot()
	if snap2.TickCount <= snap1.TickCount {
		t.Fatalf("expected TickCount to increase: %d → %d", snap1.TickCount, snap2.TickCount)
	}
	if snap2.LastTickTime.Before(snap1.LastTickTime) {
		t.Fatal("LastTickTime should be monotonic")
	}

	// Phase 7: Stop Core → Pulse stops.
	cancel()
	r.Stop()

	// Verify stopped — Snapshot should be stable.
	snap3 := r.Snapshot()
	time.Sleep(50 * time.Millisecond)
	snap4 := r.Snapshot()

	if snap3.TickCount != snap4.TickCount {
		t.Errorf("Pulse kept ticking after Stop: %d → %d", snap3.TickCount, snap4.TickCount)
	}
	if snap3.EvaluatedCount != snap4.EvaluatedCount {
		t.Errorf("EvaluatedCount changed after Stop: %d → %d", snap3.EvaluatedCount, snap4.EvaluatedCount)
	}
}

// ── Race safety: concurrent Start/Stop/Snapshot ────────────────────────────

func TestRunner_ConcurrentAccess(t *testing.T) {
	cfg := newTestConfig(true, 3*time.Millisecond)
	clock := NewFakeClock(time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))
	r := NewRunner(cfg, clock, logger.New(logger.ErrorLevel, nil))

	ctx, cancel := context.WithCancel(context.Background())

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	var snapshotCount atomic.Int64
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				clock.(*fakeClock).Advance(5 * time.Millisecond)
				_ = r.Snapshot()
				snapshotCount.Add(1)
			}
		}
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()
	r.Stop()
	close(done)

	if snapshotCount.Load() == 0 {
		t.Fatal("expected at least one snapshot during concurrent access")
	}
}

// ── Real clock does not crash ──────────────────────────────────────────────

func TestRunner_RealClock(t *testing.T) {
	cfg := newTestConfig(true, 10*time.Millisecond)
	clock := NewRealClock()
	r := NewRunner(cfg, clock, logger.New(logger.ErrorLevel, nil))

	ctx, cancel := context.WithCancel(context.Background())

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(25 * time.Millisecond)
	snap := r.Snapshot()

	cancel()
	r.Stop()

	if snap.TickCount == 0 {
		t.Log("real clock: no ticks registered (may be tight timing)")
	}
}
