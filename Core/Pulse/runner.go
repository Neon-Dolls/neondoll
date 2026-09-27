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

	// ctx is the runtime context passed to Start. It is stored for derived
	// sub-contexts (cognition timeouts) so that Pulse-originated cognition
	// respects Core shutdown.
	ctx context.Context

	tickCount             int64
	lastTickAt            time.Time
	lastCognitionAt       time.Time
	lastSpontaneousWakeAt time.Time

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

// NewTestRunner creates a Runner with deterministic test channels for
// tick-driven testing from external packages. Tests send on tickCh to
// trigger evaluate() calls and receive on ackCh to synchronize with
// evaluation completion.
//
// Internal tests (package pulse) set tickTestCh/tickAckCh directly;
// external tests (package pulse_test) should use this constructor.
func NewTestRunner(cfg config.PulseConfig, clock Clock, rng RNG, log *logger.Logger, mindEntry MindEntrance, tickCh chan time.Time, ackCh chan struct{}) *Runner {
	return &Runner{
		cfg:        cfg,
		clock:      clock,
		rng:        rng,
		log:        log,
		stopCh:     make(chan struct{}),
		mindEntry:  mindEntry,
		tickTestCh: tickCh,
		tickAckCh:  ackCh,
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
	r.ctx = ctx
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
		TickCount:             r.tickCount,
		LastTickAt:            r.lastTickAt,
		LastCognitionAt:       r.lastCognitionAt,
		LastSpontaneousWakeAt: r.lastSpontaneousWakeAt,
	}
}

// SubjectSnapshots returns a race-safe copy of the subject states currently
// held by the runner. Tests use this to verify that MarkPresented and
// MarkSettled updated the expected subjects after a cognition run.
func (r *Runner) SubjectSnapshots() []PulseSubjectState {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PulseSubjectState, len(r.subjects))
	copy(out, r.subjects)
	return out
}

// RecordCognitionEnded records that a cognition run completed (or was
// abandoned), updating both last_cognition_at and last_spontaneous_wake_at.
// Only forward-time updates are accepted; zero and stale times are ignored.
// This is separate from subject-level settling: a failed cognition still
// advances the global bookkeeping so that idle/neglect signals are
// evaluated from the last actual attempt, not from a stale earlier time.
func (r *Runner) RecordCognitionEnded(at time.Time) {
	if at.IsZero() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if at.After(r.lastCognitionAt) {
		r.lastCognitionAt = at
	}
	if at.After(r.lastSpontaneousWakeAt) {
		r.lastSpontaneousWakeAt = at
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
		TickCount:             r.tickCount,
		LastTickAt:            r.lastTickAt,
		LastCognitionAt:       r.lastCognitionAt,
		LastSpontaneousWakeAt: r.lastSpontaneousWakeAt,
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
// the Mind via the MindEntrance interface. Occupancy is released via defer
// on every path that successfully claims it, regardless of future code
// structure changes.
//
// The cognition context is derived from the runner's stored runtime context
// (r.ctx, set by Start) so that Pulse-originated cognition respects Core
// shutdown. If r.ctx is nil (tests that call admitPulseWake directly
// without Start), fall back to context.Background().
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
	defer r.ReleaseCognitionRun()

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

	// Derive cognition timeout from the runtime context so that Core
	// shutdown cancels an in-flight Pulse cognition. Fall back to
	// context.Background() when r.ctx is nil (direct test invocation).
	parentCtx := r.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	cognCtx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()

	err := r.mindEntry.EnterPulseWake(cognCtx, wake)

	// === Settling: record what happened after cognition completes ===
	//
	// Update subject timestamps for any subjects involved in this wake.
	// On success (err == nil): mark as presented AND settled.
	// On failure/abort: mark as presented only — no successful settling
	// recorded, so neglect signals can still reflect the unresolved subject.
	//
	// Also advance global bookkeeping (lastCognitionAt,
	// lastSpontaneousWakeAt) on every attempted wake so that idle/neglect
	// signals are evaluated from the latest attempt rather than a stale
	// earlier timestamp.
	//
	// The settle step runs under the Pulse state mutex while cognition run
	// occupancy is still held (the defer ReleaseCognitionRun fires after
	// this function returns). This is safe and intentional: settling is
	// a fast local operation on cached subject state, not a Mind entry.
	r.mu.Lock()
	for _, sa := range wake.Subjects {
		for i := range r.subjects {
			if r.subjects[i].SubjectID == sa.SubjectID {
				r.subjects[i].MarkPresented(now)
				if err == nil {
					r.subjects[i].MarkSettled(now)
				}
				break
			}
		}
	}
	if now.After(r.lastCognitionAt) {
		r.lastCognitionAt = now
	}
	if now.After(r.lastSpontaneousWakeAt) {
		r.lastSpontaneousWakeAt = now
	}
	r.mu.Unlock()

	if err != nil {
		r.log.Error("pulse wake cognition failed", map[string]any{"error": err})
	}
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
