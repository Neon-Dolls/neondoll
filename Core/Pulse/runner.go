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

	tickCount             int64
	lastTickAt            time.Time
	lastCognitionAt       time.Time
	lastSpontaneousWakeAt time.Time

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
// Backwards-time results are rejected: state is not updated, and a warning
// is logged. The previous LastTickAt and TickCount are preserved.
func (r *Runner) evaluate() {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev := PulseSnapshot{
		TickCount:             r.tickCount,
		LastTickAt:            r.lastTickAt,
		LastCognitionAt:       r.lastCognitionAt,
		LastSpontaneousWakeAt: r.lastSpontaneousWakeAt,
	}

	result := Evaluate(r.clock, prev)

	if result.BackwardsTime {
		r.log.Warn("pulse backwards time", map[string]any{
			"last_tick_at": r.lastTickAt,
			"now":          result.At,
		})
	} else {
		r.tickCount = result.TickCount
		r.lastTickAt = result.At
		// lastCognitionAt and lastSpontaneousWakeAt stay zero in M1
	}

	if r.tickAckCh != nil {
		// Non-blocking send; drained by tests. If nobody is listening
		// (production path), the send is dropped harmlessly.
		select {
		case r.tickAckCh <- struct{}{}:
		default:
		}
	}
}
