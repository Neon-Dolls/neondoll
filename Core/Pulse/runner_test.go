package pulse

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type lifecycleMind struct {
	mu       sync.Mutex
	dueCalls int
	count    int
	err      error
}

type blockingLifecycleMind struct {
	started chan struct{}
}

func (*blockingLifecycleMind) EnterPulseWake(context.Context, PulseWake) error { return nil }
func (m *blockingLifecycleMind) EnterDueIntentions(ctx context.Context) (int, error) {
	select {
	case <-m.started:
	default:
		close(m.started)
	}
	<-ctx.Done()
	return 1, ctx.Err()
}

func (*lifecycleMind) EnterPulseWake(context.Context, PulseWake) error { return nil }
func (m *lifecycleMind) EnterDueIntentions(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dueCalls++
	return m.count, m.err
}

// newTestRunner creates a Pulse runner configured for deterministic testing.
// It returns the runner with its injectable tick and ack channels set up,
// and a *fakeClock that tests drive manually.
func newTestRunner(t *testing.T, cfg config.PulseConfig, startAt time.Time) (*Runner, *fakeClock, chan time.Time, chan struct{}) {
	t.Helper()
	clock := NewFakeClock(startAt).(*fakeClock)
	tickCh := make(chan time.Time, 10)
	ackCh := make(chan struct{}, 10)
	log := logger.New(logger.ErrorLevel, nil)
	r := NewRunner(cfg, clock, nil, log, nil)
	r.tickTestCh = tickCh
	r.tickAckCh = ackCh
	return r, clock, tickCh, ackCh
}

// triggerTick sends one tick and waits for the runner to process it.
func triggerTick(t *testing.T, tickCh chan time.Time, ackCh chan struct{}) {
	t.Helper()
	tickCh <- time.Time{}
	select {
	case <-ackCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tick ack")
	}
}

func TestRunner_StartStop(t *testing.T) {
	r, _, _, _ := newTestRunner(t, config.PulseConfig{Enabled: true},
		time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	r.Stop()

	// Verify goroutine exited
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.wg.Wait()
	}()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// goroutine exited
	case <-time.After(5 * time.Second):
		t.Fatal("runner goroutine did not exit after Stop")
	}

	// Second stop is safe
	r.Stop()
}

func TestRunner_DuplicateStart(t *testing.T) {
	r, _, _, _ := newTestRunner(t, config.PulseConfig{Enabled: true},
		time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	if err := r.Start(ctx); err == nil {
		t.Fatal("expected error on duplicate Start")
	}

	r.Stop()
}

func TestRunnerDispatchesDueIntentionsWhenSpontaneousPulseDisabled(t *testing.T) {
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ticks, acknowledgements := make(chan time.Time, 2), make(chan struct{}, 2)
	mind := &lifecycleMind{count: 1}
	runner := NewTestRunner(config.PulseConfig{Enabled: false}, NewFakeClock(start), nil,
		logger.New(logger.ErrorLevel, nil), mind, ticks, acknowledgements)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	triggerTick(t, ticks, acknowledgements)
	runner.Stop()
	mind.mu.Lock()
	defer mind.mu.Unlock()
	if mind.dueCalls != 1 {
		t.Fatalf("due lifecycle calls=%d, want 1", mind.dueCalls)
	}
	if runner.Snapshot().TickCount != 0 {
		t.Fatal("disabled spontaneous Pulse bookkeeping advanced")
	}
}

func TestRunnerRetriesFailedDueIntentionOnLaterTick(t *testing.T) {
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ticks, acknowledgements := make(chan time.Time, 2), make(chan struct{}, 2)
	mind := &lifecycleMind{count: 1, err: errors.New("inference unavailable")}
	runner := NewTestRunner(config.PulseConfig{Enabled: false}, NewFakeClock(start), nil,
		logger.New(logger.ErrorLevel, nil), mind, ticks, acknowledgements)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	triggerTick(t, ticks, acknowledgements)
	mind.mu.Lock()
	mind.err = nil
	mind.mu.Unlock()
	triggerTick(t, ticks, acknowledgements)
	runner.Stop()
	mind.mu.Lock()
	defer mind.mu.Unlock()
	if mind.dueCalls != 2 {
		t.Fatalf("due lifecycle calls=%d, want retry", mind.dueCalls)
	}
}

func TestRunnerStopCancelsActiveDueIntention(t *testing.T) {
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ticks, acknowledgements := make(chan time.Time, 1), make(chan struct{}, 1)
	mind := &blockingLifecycleMind{started: make(chan struct{})}
	runner := NewTestRunner(config.PulseConfig{Enabled: false}, NewFakeClock(start), nil,
		logger.New(logger.ErrorLevel, nil), mind, ticks, acknowledgements)
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks <- start
	select {
	case <-mind.started:
	case <-time.After(5 * time.Second):
		t.Fatal("due intention did not start")
	}
	done := make(chan struct{})
	go func() {
		runner.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel active intention cognition")
	}
}

func TestRunner_Disabled(t *testing.T) {
	r, _, _, _ := newTestRunner(t, config.PulseConfig{Enabled: false},
		time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Snapshot should be zero even if we trigger ticks via the channel
	snap := r.Snapshot()
	if snap.TickCount != 0 {
		t.Errorf("expected TickCount=0 for disabled runner, got %d", snap.TickCount)
	}
}

func TestRunner_Snapshot(t *testing.T) {
	startTime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	r, clock, tickCh, ackCh := newTestRunner(t, config.PulseConfig{Enabled: true}, startTime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Initial snapshot
	snap0 := r.Snapshot()
	if snap0.TickCount != 0 {
		t.Errorf("expected TickCount=0 initially, got %d", snap0.TickCount)
	}
	if !snap0.LastTickAt.IsZero() {
		t.Errorf("expected zero LastTickAt initially, got %v", snap0.LastTickAt)
	}
	if !snap0.LastCognitionAt.IsZero() {
		t.Errorf("expected zero LastCognitionAt in M1")
	}
	if !snap0.LastSpontaneousWakeAt.IsZero() {
		t.Errorf("expected zero LastSpontaneousWakeAt in M1")
	}

	// Tick once
	clock.Advance(2 * time.Second)
	triggerTick(t, tickCh, ackCh)

	snap1 := r.Snapshot()
	if snap1.TickCount != 1 {
		t.Errorf("expected TickCount=1 after 1 tick, got %d", snap1.TickCount)
	}
	if !snap1.LastTickAt.Equal(startTime.Add(2 * time.Second)) {
		t.Errorf("expected LastTickAt=%v, got %v",
			startTime.Add(2*time.Second), snap1.LastTickAt)
	}

	// Tick again
	clock.Advance(3 * time.Second)
	triggerTick(t, tickCh, ackCh)

	snap2 := r.Snapshot()
	if snap2.TickCount != 2 {
		t.Errorf("expected TickCount=2 after 2 ticks, got %d", snap2.TickCount)
	}
	if !snap2.LastTickAt.Equal(startTime.Add(5 * time.Second)) {
		t.Errorf("expected LastTickAt=%v, got %v",
			startTime.Add(5*time.Second), snap2.LastTickAt)
	}
}

func TestRunner_NoTicksAfterStop(t *testing.T) {
	startTime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	r, clock, tickCh, ackCh := newTestRunner(t, config.PulseConfig{Enabled: true}, startTime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Tick once to verify runner is live
	clock.Advance(time.Second)
	triggerTick(t, tickCh, ackCh)
	snap1 := r.Snapshot()
	if snap1.TickCount != 1 {
		t.Fatalf("expected TickCount=1, got %d", snap1.TickCount)
	}

	// Stop
	r.Stop()

	// Wait for goroutine to exit
	r.wg.Wait()

	// Tick after stop — channel is buffered; send succeeds but nobody reads
	clock.Advance(time.Second)
	tickCh <- time.Time{}

	// Verify no change
	snapFinal := r.Snapshot()
	if snapFinal.TickCount != snap1.TickCount {
		t.Errorf("TickCount changed after Stop: %d → %d", snap1.TickCount, snapFinal.TickCount)
	}
	if !snapFinal.LastTickAt.Equal(snap1.LastTickAt) {
		t.Errorf("LastTickAt changed after Stop")
	}
}

func TestRunner_ContextCancellation(t *testing.T) {
	startTime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	r, clock, tickCh, ackCh := newTestRunner(t, config.PulseConfig{Enabled: true}, startTime)

	ctx, cancel := context.WithCancel(context.Background())

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Tick once
	clock.Advance(time.Second)
	triggerTick(t, tickCh, ackCh)
	snap1 := r.Snapshot()

	// Cancel context
	cancel()

	// Wait for goroutine to exit
	r.wg.Wait()

	// Tick after cancellation
	tickCh <- time.Time{}

	snapFinal := r.Snapshot()
	if snapFinal.TickCount != snap1.TickCount {
		t.Errorf("TickCount changed after context cancellation")
	}

	// Stop is still safe
	r.Stop()
}

func TestRunner_BackwardsTime(t *testing.T) {
	startTime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	r, clock, tickCh, ackCh := newTestRunner(t, config.PulseConfig{
		Enabled:        true,
		MinWakeSpacing: 0,
	}, startTime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// Tick forward
	clock.Advance(5 * time.Second)
	triggerTick(t, tickCh, ackCh)

	snap1 := r.Snapshot()
	if snap1.TickCount != 1 {
		t.Fatalf("expected TickCount=1, got %d", snap1.TickCount)
	}
	if !snap1.LastTickAt.Equal(startTime.Add(5 * time.Second)) {
		t.Fatalf("expected LastTickAt=%v, got %v",
			startTime.Add(5*time.Second), snap1.LastTickAt)
	}

	// Tick forward a second time to have a meaningful signal snapshot
	clock.Advance(5 * time.Second)
	triggerTick(t, tickCh, ackCh)

	snap2 := r.Snapshot()
	sigSnap2 := r.SignalSnapshot()
	if snap2.TickCount != 2 {
		t.Fatalf("expected TickCount=2, got %d", snap2.TickCount)
	}

	// Set clock backwards
	clock.Set(startTime)
	triggerTick(t, tickCh, ackCh)

	// Backwards tick must be rejected — no state change, no signal change
	snap3 := r.Snapshot()
	sigSnap3 := r.SignalSnapshot()

	if snap3.TickCount != 2 {
		t.Errorf("TickCount changed from 2 to %d after backwards time (should stay 2)", snap3.TickCount)
	}
	if !snap3.LastTickAt.Equal(startTime.Add(10 * time.Second)) {
		t.Errorf("LastTickAt changed after backwards time (should stay at %v, got %v)",
			startTime.Add(10*time.Second), snap3.LastTickAt)
	}

	// SignalSnapshot must also be preserved — not recomputed with regressed time
	if sigSnap3.Idle != sigSnap2.Idle ||
		sigSnap3.Cooldown != sigSnap2.Cooldown {
		t.Error("SignalSnapshot changed after backwards time (must be preserved)")
	}

	// Verify the signal snapshot didn't get zeroed either
	if sigSnap3.Cooldown != sigSnap2.Cooldown {
		t.Errorf("SignalSnapshot.Cooldown changed from %.4f to %.4f after backwards time",
			sigSnap2.Cooldown, sigSnap3.Cooldown)
	}

	// LastSpontaneousWakeAt in bookkeeping is always zero in M2
	if !snap3.LastSpontaneousWakeAt.IsZero() {
		t.Errorf("expected zero LastSpontaneousWakeAt in M2, got %v", snap3.LastSpontaneousWakeAt)
	}
}

func TestRunner_RaceSafety(t *testing.T) {
	startTime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fc := NewFakeClock(startTime).(*fakeClock)
	tickCh := make(chan time.Time, 100)
	ackCh := make(chan struct{}, 100)
	log := logger.New(logger.ErrorLevel, nil)
	r := NewRunner(config.PulseConfig{Enabled: true}, fc, nil, log, nil)
	r.tickTestCh = tickCh
	r.tickAckCh = ackCh

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const N = 50

	// Concurrently drive ticks and read snapshots
	var wg sync.WaitGroup
	wg.Add(2)

	// Tick producer
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			fc.Advance(time.Millisecond)
			tickCh <- time.Time{}
		}
	}()

	// Snapshot reader
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = r.Snapshot()
		}
	}()

	wg.Wait()
	r.Stop()
}

func TestRunner_M1Conformance_FullLifecycle(t *testing.T) {
	// Proves: Core starts → one Pulse runner starts → injected time advances
	// → Pulse deterministically records temporal evaluation → Core stops →
	// Pulse stops.
	startTime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	r, clock, tickCh, ackCh := newTestRunner(t, config.PulseConfig{Enabled: true}, startTime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Core starts → one Pulse runner starts
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Pulse starts with zero state
	snap0 := r.Snapshot()
	if snap0.TickCount != 0 {
		t.Fatalf("expected zero state on start")
	}

	// Injected time advances → Pulse records
	clock.Advance(10 * time.Second)
	triggerTick(t, tickCh, ackCh)

	snap1 := r.Snapshot()
	if snap1.TickCount != 1 {
		t.Errorf("expected TickCount=1, got %d", snap1.TickCount)
	}
	expectedTime := startTime.Add(10 * time.Second)
	if !snap1.LastTickAt.Equal(expectedTime) {
		t.Errorf("expected LastTickAt=%v, got %v", expectedTime, snap1.LastTickAt)
	}

	// More time advances
	clock.Advance(5 * time.Second)
	triggerTick(t, tickCh, ackCh)

	snap2 := r.Snapshot()
	if snap2.TickCount != 2 {
		t.Errorf("expected TickCount=2, got %d", snap2.TickCount)
	}
	expectedTime2 := startTime.Add(15 * time.Second)
	if !snap2.LastTickAt.Equal(expectedTime2) {
		t.Errorf("expected LastTickAt=%v, got %v", expectedTime2, snap2.LastTickAt)
	}

	// Core stops → Pulse stops
	cancel()
	r.Stop()
	r.wg.Wait()

	// No more evaluations
	clock.Advance(time.Hour)
	tickCh <- time.Time{}

	snapFinal := r.Snapshot()
	if snapFinal.TickCount != snap2.TickCount {
		t.Errorf("TickCount changed after stop: %d → %d", snap2.TickCount, snapFinal.TickCount)
	}

	// No Mind or inference was called (verified by M1 not importing those packages)
}
