package pulse

import (
	"context"
	"errors"
	"sync"
	"time"

	coreconfig "github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// Runner manages the Pulse evaluation loop.
// It enforces at-most-one runner per runtime and ensures goroutine lifecycle
// is properly managed with synchronous Stop().
type Runner struct {
	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
	config  coreconfig.PulseConfig
	clock   Clock
	log     *logger.Logger

	// mutable state protected by mu
	tickCount      int64
	lastTickTime   time.Time
	evaluatedCount int64

	// last result for backwards-time detection
	lastResult PulseResult
}

// NewRunner creates a Pulse runner. It does not start it.
func NewRunner(cfg coreconfig.PulseConfig, clk Clock, log *logger.Logger) *Runner {
	return &Runner{
		config: cfg,
		clock:  clk,
		log:    log,
	}
}

// Start begins the Pulse evaluation loop in a new goroutine.
// Returns an error if the runner is already running.
// When MinWakeSpacing is 0, the runner starts but never ticks (disabled mode).
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return errors.New("pulse runner already running")
	}

	r.running = true
	r.stopCh = make(chan struct{})

	r.wg.Add(1)
	go r.run(ctx)
	return nil
}

// Stop signals the runner to stop and waits for the goroutine to exit.
// Safe to call multiple times; subsequent calls are no-ops.
func (r *Runner) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	close(r.stopCh)
	r.mu.Unlock()
	r.wg.Wait()
}

// Snapshot returns a read-only copy of the current Pulse state.
// This is the only way to observe Pulse state from outside the runner.
func (r *Runner) Snapshot() PulseSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return PulseSnapshot{
		TickCount:      r.tickCount,
		LastTickTime:   r.lastTickTime,
		EvaluatedCount: r.evaluatedCount,
	}
}

func (r *Runner) run(ctx context.Context) {
	defer r.wg.Done()

	// If Pulse is not enabled or MinWakeSpacing is 0, just wait for stop.
	if !r.config.Enabled || r.config.MinWakeSpacing <= 0 {
		<-r.stopCh
		return
	}

	ticker := time.NewTicker(r.config.MinWakeSpacing)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.tick()
		}
	}
}

func (r *Runner) tick() {
	result := Evaluate(r.clock, r.lastResult)

	r.mu.Lock()
	r.tickCount++
	r.lastTickTime = result.At
	r.evaluatedCount += int64(len(result.Subjects))
	if result.BackwardsTime {
		r.log.Warn("Backwards time detected", map[string]any{
			"now":  result.At.Format(time.RFC3339),
			"prev": r.lastResult.At.Format(time.RFC3339),
		})
	}
	r.lastResult = result
	r.mu.Unlock()
}
