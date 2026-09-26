package pulse

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// Interface assertions (compile-time checks)
var _ Clock = (*realClock)(nil)
var _ Clock = (*fakeClock)(nil)

// defaultTickInterval is the runner's internal evaluation cadence in production.
// It is implementation machinery, not a canonical Pulse contract field;
// later milestones may replace it with adaptive/event-driven scheduling.
const defaultTickInterval = 1 * time.Second

// Runner owns one Pulse evaluation loop per Core runtime.
// It is single-runner only — duplicate Start is rejected.
type Runner struct {
	cfg   config.PulseConfig
	clock Clock
	log   *logger.Logger

	mu      sync.Mutex
	started atomic.Bool
	stopCh  chan struct{}
	wg      sync.WaitGroup

	tickCount       int64
	lastTickAt      time.Time
	lastCognitionAt time.Time

	// Subjects observed by Pulse. Core calls UpdateSubjects to push changes;
	// evaluate() reads the latest snapshot under the lock.
	subjects []PulseSubjectState
	// currentBudget is the narrow inhibition input supplied by Core.
	currentBudget float64
	// lastSignalSnapshot is the most recently evaluated signal snapshot.
	lastSignalSnapshot SignalSnapshot

	// Test injection: when non-nil, replaces the production ticker channel.
	// Tests send on this channel to drive evaluations deterministically.
	tickTestCh chan time.Time
	// Test injection: when non-nil, closed/drained after each evaluate() call
	// completes under the lock. Tests read from this to synchronise with
	// goroutine evaluation without time.Sleep.
	tickAckCh chan struct{}
}

// NewRunner creates a Pulse runner. It does not start the evaluation loop;
// call Start after construction.
func NewRunner(cfg config.PulseConfig, clock Clock, log *logger.Logger) *Runner {
	return &Runner{
		cfg:    cfg,
		clock:  clock,
		log:    log,
		stopCh: make(chan struct{}),
	}
}

// Start begins the Pulse evaluation loop. Only one evaluation loop may run;
// a second call returns an error.
func (r *Runner) Start(ctx context.Context) error {
	if !r.started.CompareAndSwap(false, true) {
		return errors.New("pulse runner already started")
	}
	r.wg.Add(1)
	go r.run(ctx)
	return nil
}

// Stop signals the evaluation loop to exit and waits for it to finish.
// Safe to call multiple times; subsequent calls are no-ops.
func (r *Runner) Stop() {
	if !r.started.Load() {
		return
	}
	select {
	case <-r.stopCh:
		// already closed
	default:
		close(r.stopCh)
	}
	r.wg.Wait() // only the first close triggers stop
}

// Snapshot returns a race-safe read of the runner's current bookkeeping.
// LastSpontaneousWakeAt is always zero in M2 (no spontaneous-wake mutation path).
func (r *Runner) Snapshot() PulseSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return PulseSnapshot{
		TickCount:       r.tickCount,
		LastTickAt:      r.lastTickAt,
		LastCognitionAt: r.lastCognitionAt,
	}
}

// SignalSnapshot returns a race-safe read of the runner's most recently
// evaluated signal snapshot. Returns a zero-value (all signals = 0) if
// no evaluation has occurred yet.
func (r *Runner) SignalSnapshot() SignalSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSignalSnapshot
}

// UpdateSubjects replaces the set of subjects Pulse observes.
// Core calls this to push subject bookkeeping changes. Under the lock so
// evaluate() sees a consistent view. A nil or empty slice clears subjects.
func (r *Runner) UpdateSubjects(subjects []PulseSubjectState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if subjects == nil {
		r.subjects = nil
		return
	}
	r.subjects = make([]PulseSubjectState, len(subjects))
	copy(r.subjects, subjects)
}

// SetBudget sets the narrow inhibition budget input from Core.
// Values outside [0, 1] are clamped by EvaluateSignals; Pulse does not
// reject them here.
func (r *Runner) SetBudget(budget float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentBudget = budget
}

// RecordCognition records a cognition event timestamp.
// Used by Core to set LastCognitionAt, which drives the idle signal.
// Only forward-time updates are accepted.
func (r *Runner) RecordCognition(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if at.After(r.lastCognitionAt) {
		r.lastCognitionAt = at
	}
}

func (r *Runner) run(ctx context.Context) {
	defer r.wg.Done()

	if !r.cfg.Enabled {
		<-r.stopCh
		return
	}

	var tickCh <-chan time.Time
	if r.tickTestCh != nil {
		tickCh = r.tickTestCh
	} else {
		ticker := time.NewTicker(defaultTickInterval)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-tickCh:
			r.evaluate()
		}
	}
}

// evaluate performs one Pulse evaluation and updates runner state.
// It calls the pure Evaluate function and applies its result.
// Backwards-time results are rejected: state is not updated, a warning
// is logged, and the previous LastTickAt, TickCount, and SignalSnapshot
// are all preserved.
func (r *Runner) evaluate() {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev := PulseSnapshot{
		TickCount:       r.tickCount,
		LastTickAt:      r.lastTickAt,
		LastCognitionAt: r.lastCognitionAt,
	}

	result := Evaluate(r.clock, prev)

	if result.BackwardsTime {
		r.log.Warn("pulse backwards time", map[string]any{
			"last_tick_at": r.lastTickAt,
			"now":          result.At,
		})
		// Preserve the previous valid SignalSnapshot; do not recompute
		// signals using the regressed time.
		if r.tickAckCh != nil {
			select {
			case r.tickAckCh <- struct{}{}:
			default:
			}
		}
		return
	}

	r.tickCount = result.TickCount
	r.lastTickAt = result.At

	// Evaluate signals at the current time with current bookkeeping.
	now := result.At
	subjects := make([]PulseSubjectState, len(r.subjects))
	copy(subjects, r.subjects)
	inhibition := InhibitionInputs{Budget: r.currentBudget}

	sigPulseState := PulseSnapshot{
		TickCount:       r.tickCount,
		LastTickAt:      r.lastTickAt,
		LastCognitionAt: r.lastCognitionAt,
	}

	sigSnap := EvaluateSignals(now, r.cfg, sigPulseState, subjects, inhibition)
	r.lastSignalSnapshot = sigSnap

	if r.tickAckCh != nil {
		select {
		case r.tickAckCh <- struct{}{}:
		default:
		}
	}
}
