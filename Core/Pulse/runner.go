package pulse

import (
	"context"
	"errors"
	"fmt"
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

	// CheckpointWriter is called after mutable bookkeeping changes that should
	// be persisted as a PulseCheckpoint. When non-nil, it is invoked after:
	//   1. Wake admission (subjects presented, lastSpontaneousWakeAt updated)
	//   2. Successful cognition settlement (subjects settled, lastCognitionAt
	//      updated)
	// The callback receives the full checkpoint snapshot at that instant.
	// It must NOT hold the Runner mutex through persistence calls.
	// If the callback returns an error, admitPulseWake logs it after admission
	// (the wake is real and cognition proceeds) and returns it after settlement
	// so the caller can observe the durability failure.
	// Production wiring sets this to save via CheckpointStore; tests leave
	// it nil (no persistence setup needed for unit tests).
	CheckpointWriter CheckpointWriter

	// Test injection: when non-nil, replaces the production ticker channel.
	// Tests send on this channel to drive evaluations deterministically.
	tickTestCh chan time.Time
	// Test injection: when non-nil, closed/drained after each evaluate() call
	// completes. Tests read from this to synchronise with
	// goroutine evaluation without time.Sleep.
	tickAckCh chan struct{}
}

// CheckpointWriter is called after mutable bookkeeping changes that should
// be persisted. It replaces the older silent OnCheckpoint callback so that
// durability failures are observable.
type CheckpointWriter func(PulseCheckpoint) error

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
		if err := r.admitPulseWake(now, opp); err != nil {
			r.log.Error("pulse wake error", map[string]any{"error": err})
		}
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
//
// Lifecycle semantics (durable-first invariant):
//
//	ADMISSION (before EnterPulseWake):
//	  - wake subjects marked as presented (admission time)
//	  - last_spontaneous_wake_at updated to admission time
//	  - admission checkpoint persisted BEFORE cognition begins
//	  - checkpoint FAILURE → cognition NOT started, error returned
//	  - on checkpoint failure in-memory admission still stands (no rollback)
//	COGNITION (blocking):
//	  - EnterPulseWake runs the model cognition
//	  - only reached if admission checkpoint succeeded
//	SETTLING (on success only):
//	  - wake subjects marked as settled (completion time)
//	  - last_cognition_at updated to completion time
//	  - settlement checkpoint persisted; failure is observable
//	FAILURE (error or cancel):
//	  - presentation from admission remains (both in-memory and durable)
//	  - last_settled_at and last_cognition_at are NOT updated
func (r *Runner) admitPulseWake(now time.Time, opp OpportunitySnapshot) error {
	if !opp.Opportunity {
		r.log.Debug("pulse wake not admitted: opportunity is false", nil)
		return nil
	}

	if r.mindEntry == nil {
		r.log.Warn("pulse spontaneous opportunity but no MindEntrance set", nil)
		return nil
	}

	if !r.TryClaimCognitionRun() {
		r.log.Debug("pulse spontaneous opportunity skipped: cognition run already active", nil)
		return nil
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

	// === ADMISSION: mark subjects presented + set lastSpontaneousWakeAt ===
	// This runs before EnterPulseWake so that the presentation timestamp
	// reflects the admission time, not the (unknown) completion time.
	r.mu.Lock()
	for _, sa := range wake.Subjects {
		for i := range r.subjects {
			if r.subjects[i].SubjectID == sa.SubjectID {
				r.subjects[i].MarkPresented(now)
				break
			}
		}
	}
	if now.After(r.lastSpontaneousWakeAt) {
		r.lastSpontaneousWakeAt = now
	}
	r.mu.Unlock()

	// Persist checkpoint after admission, before cognition blocks.
	// INVARIANT: if checkpoint write fails, cognition must NOT start.
	// The in-memory admission (subjects presented, lastSpontaneousWakeAt)
	// is NOT rolled back — the wake was truly admitted by Core, and the
	// runtime exposes the persistence failure rather than fabricating a
	// different lifecycle history.
	if r.CheckpointWriter != nil {
		if err := r.CheckpointWriter(r.ToCheckpoint()); err != nil {
			r.log.Error("pulse: admission checkpoint write FAILED — cognition NOT started",
				map[string]any{"error": err})
			return fmt.Errorf("pulse: admission checkpoint: %w", err)
		}
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

	// === SETTLING: on success only ===
	// Mark subjects settled and update last_cognition_at using the actual
	// completion time (clock.Now()), which differs from the admission time
	// when cognition blocks or takes time.
	// On error/cancellation: presentation from admission remains, but
	// last_settled_at and last_cognition_at are NOT updated.
	if err == nil {
		completionTime := r.clock.Now()
		r.mu.Lock()
		for _, sa := range wake.Subjects {
			for i := range r.subjects {
				if r.subjects[i].SubjectID == sa.SubjectID {
					r.subjects[i].MarkSettled(completionTime)
					break
				}
			}
		}
		if completionTime.After(r.lastCognitionAt) {
			r.lastCognitionAt = completionTime
		}
		r.mu.Unlock()

		// Persist checkpoint after successful cognition settlement.
		// Failure is returned so the caller can observe the durability issue.
		if r.CheckpointWriter != nil {
			if err := r.CheckpointWriter(r.ToCheckpoint()); err != nil {
				r.log.Error("pulse: checkpoint write after settlement failed", map[string]any{"error": err})
				return fmt.Errorf("pulse: checkpoint write after settlement: %w", err)
			}
		}
	}

	if err != nil {
		r.log.Error("pulse wake cognition failed", map[string]any{"error": err})
	}
	return nil
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
