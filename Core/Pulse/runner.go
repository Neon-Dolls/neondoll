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
	rng   RNG
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
	// lastOpportunitySnapshot is the most recently evaluated opportunity snapshot.
	lastOpportunitySnapshot OpportunitySnapshot
	// cognitionRun is the atomic occupancy flag for Cognition Run execution.
	// 0 = free, 1 = claimed. TryClaim checks CAS 0->1; Release stores 0.
	// This is runtime concurrency, NOT autonomy control.
	cognitionRun int32
	// mindEntry is the interface to DollMind for spontaneous Pulse cognition.
	mindEntry MindEntrance

	// Test injection: when non-nil, replaces the production ticker channel.
	// Tests send on this channel to drive evaluations deterministically.
	tickTestCh chan time.Time
	// Test injection: when non-nil, closed/drained after each evaluate() call
	// completes. Tests read from this to synchronise with
	// goroutine evaluation without time.Sleep.
	tickAckCh chan struct{}
}

// NewRunner creates a Pulse runner. It does not start the evaluation loop;
// call Start after construction. mindEntry may be nil; Pulse runs without
// spontaneous wake admission until a MindEntrance is set.
func NewRunner(cfg config.PulseConfig, clock Clock, rng RNG, log *logger.Logger, mindEntry MindEntrance) *Runner {
	return &Runner{
		cfg:       cfg,
		clock:     clock,
		rng:       rng,
		log:       log,
		stopCh:    make(chan struct{}),
		mindEntry: mindEntry,
	}
}

// SetMindEntrance sets or replaces the MindEntrance for spontaneous cognition
// admission. Safe to call before Start or between ticks.
func (r *Runner) SetMindEntrance(entry MindEntrance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mindEntry = entry
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

// OpportunitySnapshot returns a race-safe read of the runner's most recently
// evaluated opportunity snapshot.
func (r *Runner) OpportunitySnapshot() OpportunitySnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastOpportunitySnapshot
}

// SetCognitionRunActive sets the cognition run occupancy flag atomically.
// When true, Pulse does not sample optional spontaneous opportunity.
func (r *Runner) SetCognitionRunActive(active bool) {
	if active {
		atomic.StoreInt32(&r.cognitionRun, 1)
	} else {
		atomic.StoreInt32(&r.cognitionRun, 0)
	}
}

// CognitionRunActive returns whether a Cognition Run is currently executing.
func (r *Runner) CognitionRunActive() bool {
	return atomic.LoadInt32(&r.cognitionRun) == 1
}

// TryClaimCognitionRun attempts to atomically claim the Cognition Run
// occupancy. Returns true if the claim succeeded (was free and is now
// claimed), false if already claimed. Safe to call without holding mu.
func (r *Runner) TryClaimCognitionRun() bool {
	return atomic.CompareAndSwapInt32(&r.cognitionRun, 0, 1)
}

// ReleaseCognitionRun atomically releases Cognition Run occupancy.
// Safe to call without holding mu.
func (r *Runner) ReleaseCognitionRun() {
	atomic.StoreInt32(&r.cognitionRun, 0)
}

// SetRNG swaps the RNG source used for stochastic opportunity sampling.
// Intended for testing.
func (r *Runner) SetRNG(rng RNG) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rng = rng
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

// evaluate performs one Pulse evaluation and, if an opportunity is detected,
// attempts to admit a spontaneous cognition run through the MindEntrance.
//
// State mutation (tick, signals, opportunity) happens under mu. The mu is
// released before any Mind entry to satisfy the invariant that inference
// never runs under the Pulse state mutex. The Cognition Run occupancy is
// managed atomically (not under mu).
func (r *Runner) evaluate() {
	r.mu.Lock()

	prev := PulseSnapshot{
		TickCount:       r.tickCount,
		LastTickAt:      r.lastTickAt,
		LastCognitionAt: r.lastCognitionAt,
	}

	result := Evaluate(r.clock, prev)

	if result.BackwardsTime {
		r.mu.Unlock()
		r.log.Warn("pulse backwards time", map[string]any{
			"last_tick_at": r.lastTickAt,
			"now":          result.At,
		})
		r.sendTickAck()
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

	// Evaluate opportunity: read the atomic cognition flag (no mu needed).
	cognActive := atomic.LoadInt32(&r.cognitionRun) == 1
	opp := EvaluateOpportunity(now, sigSnap, sigPulseState, r.cfg, cognActive, r.rng)
	r.lastOpportunitySnapshot = opp

	// State updates complete. Release mu before any Mind entry.
	r.mu.Unlock()

	if opp.Opportunity {
		r.admitPulseWake(now, opp)
	}

	r.sendTickAck()
}

// admitPulseWake claims cognition run occupancy and, if successful, enters
// the Mind via the MindEntrance interface. Occupancy is released on every
// exit path (success, error, nil mindEntry).
func (r *Runner) admitPulseWake(now time.Time, opp OpportunitySnapshot) {
	if !opp.Opportunity {
		r.log.Debug("pulse wake not admitted: opportunity is false", nil)
		return
	}

	if r.mindEntry == nil {
		r.log.Warn("pulse spontaneous opportunity but no MindEntrance set", nil)
		return
	}

	if !r.TryClaimCognitionRun() {
		r.log.Debug("pulse spontaneous opportunity skipped: cognition run already active", nil)
		return
	}

	var sample float64
	if opp.RandomSample != nil {
		sample = *opp.RandomSample
	}

	wake := PulseWake{
		AdmittedAt:        now,
		Pressure:          opp.Pressure,
		EffectivePressure: opp.EffectivePressure,
		ActivationSignals: opp.ActivationSignals,
		Inhibition:        opp.Inhibition,
		Subjects:          ensureSubjects(opp.Subjects),
		RandomSample:      sample,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := r.mindEntry.EnterPulseWake(ctx, wake)
	if err != nil {
		r.log.Error("pulse wake cognition failed", map[string]any{"error": err})
	}

	r.ReleaseCognitionRun()
}

func (r *Runner) sendTickAck() {
	if r.tickAckCh == nil {
		return
	}
	select {
	case r.tickAckCh <- struct{}{}:
	default:
	}
}
