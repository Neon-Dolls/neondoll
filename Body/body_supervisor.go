// SPDX-License-Identifier: AGPL-3.0-only

package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// SupervisorConfig configures the Body Path Supervisor.
type SupervisorConfig struct {
	// PathSelector is the configuration for selecting candidate paths.
	PathSelector PathSelectorConfig

	// LossObserver configures loss detection timing.
	LossObserver LossObserverConfig

	// RecoverTimeout is the deadline for each individual recovery attempt.
	// If zero, each recovery blocks indefinitely (not recommended).
	RecoverTimeout time.Duration

	// SharedEpoch is an optional externally-owned RecoveryEpoch.  When
	// non-nil the supervisor uses this shared epoch instead of creating a
	// private one, allowing callers (and future coordinators) to observe
	// the authoritative active-path state via epoch.Active().  The initial
	// tunnel passed to StartPathSupervisor must match epoch.Active().
	//
	// When nil the supervisor creates a private epoch as before.
	SharedEpoch *RecoveryEpoch

	// Discovery configures automatic better-path discovery and migration
	// after the supervisor recovers onto a fallback path.  When enabled,
	// the supervisor probes the direct endpoint after recovery and
	// performs controlled migration when the preferred path is reachable.
	// Zero DiscoveryConfig or DiscoveryConfig.Enabled == false disables
	// automatic discovery (the default).
	Discovery DiscoveryConfig
}

// DiscoveryConfig controls automatic better-path discovery after recovery.
type DiscoveryConfig struct {
	// Enabled controls whether the supervisor automatically runs
	// better-path discovery after recovery onto a fallback path.
	Enabled bool

	// ProbeTarget is the UDP address to probe for direct-path
	// reachability.  When empty, PathSelector.DirectEndpoint is used.
	ProbeTarget string

	// MinInterval is the minimum interval between discovery probes.
	// Default: 500ms.
	MinInterval time.Duration

	// MaxBackoff caps the exponential backoff between probe attempts.
	// Default: 10s.
	MaxBackoff time.Duration

	// MaxAttempts limits the number of probe attempts before giving up.
	// Default: 10.
	MaxAttempts int

	// ProbeTimeout is the per-probe timeout for ReachProbe.
	// Default: 3s.
	ProbeTimeout time.Duration

	// HandshakeTimeout is the deadline for each migration handshake attempt.
	// Default: 10s.
	HandshakeTimeout time.Duration
}

// directCandidate is a MigrationCandidate that targets a specific UDP endpoint.
type directCandidate struct {
	endpoint string
}

func (c *directCandidate) Validate(ctx context.Context) (*MigrationTarget, error) {
	return &MigrationTarget{CoreEndpoint: c.endpoint}, nil
}

func (c *directCandidate) Stop() {}

// SupervisorHandle controls a running supervisor goroutine.
type SupervisorHandle struct {
	done       chan struct{}
	stop       context.CancelFunc
	epoch      *RecoveryEpoch
	lossConfig LossObserverConfig
}

// Done returns a channel that closes when the supervisor has shut down.
func (h *SupervisorHandle) Done() <-chan struct{} {
	return h.done
}

// Stop initiates a clean shutdown of the supervisor. It cancels the
// supervisor's internal context, which causes the supervisor to
// release its loss observer and abort any in-progress recovery.
// Stop returns immediately — it does NOT wait for the supervisor
// goroutine to exit. Use Done() if you need to synchronize.
//
// After Stop():
//   - The supervisor goroutine exits asynchronously.
//   - The tunnel being observed (the initial tunnel or the last
//     committed tunnel) is left in its current state.  If recovery
//     had not started, the tunnel may still be usable.  If recovery
//     was in progress, the old tunnel has been stopped.
//   - The epoch remains accessible via ActiveTunnel().
//   - Subsequent calls to Stop are no-ops.
func (h *SupervisorHandle) Stop() {
	h.stop()
}

// ActiveTunnel returns the currently-active tunnel according to the
// supervisor's epoch.  It is safe for concurrent use.  Returns nil when
// the supervisor has not yet started or has shut down without a committed
// tunnel.
func (h *SupervisorHandle) ActiveTunnel() *BodyTunnel {
	if h.epoch == nil {
		return nil
	}
	return h.epoch.Active()
}

// Epoch returns the supervisor's epoch.  Safe for concurrent use.
// Returns nil when the supervisor has not yet initialised.
func (h *SupervisorHandle) Epoch() *RecoveryEpoch {
	return h.epoch
}

// StartPathSupervisor launches a supervisor goroutine that monitors the
// provided tunnel for connectivity loss.  When loss is detected it
// triggers a recovery attempt via RecoverPath.  Retryable failures are
// retried with exponential backoff; terminal errors and context
// cancellation cause the supervisor to shut down.
//
// cfg.SharedEpoch, when non-nil, is used instead of an internally-created
// epoch.  In that case initial must match epoch.Active().
func StartPathSupervisor(
	ctx context.Context,
	initial *BodyTunnel,
	cfg SupervisorConfig,
	log *slog.Logger,
) (*SupervisorHandle, error) {
	if initial == nil {
		return nil, fmt.Errorf("body: start supervisor: initial tunnel is nil")
	}

	// Determine the epoch: shared or private.
	var epoch *RecoveryEpoch
	if cfg.SharedEpoch != nil {
		epoch = cfg.SharedEpoch
		if active := epoch.Active(); active != initial {
			return nil, fmt.Errorf(
				"body: start supervisor: initial tunnel does not match shared epoch active tunnel",
			)
		}
	} else {
		epoch = NewRecoveryEpoch(initial)
	}

	recoveryCtx, cancel := context.WithCancel(ctx)
	h := &SupervisorHandle{
		done:       make(chan struct{}),
		stop:       cancel,
		epoch:      epoch,
		lossConfig: cfg.LossObserver,
	}
	go runSupervisor(recoveryCtx, initial, cfg, epoch, h, log)
	return h, nil
}

// isRecoveryTerminal returns true when a recovery error should stop the
// supervisor permanently.
func isRecoveryTerminal(err error) bool {
	if errors.Is(err, ErrPathCancelled) {
		return false // clean shutdown, not terminal
	}
	if errors.Is(err, ErrAllPathsFailed) {
		return false // retryable
	}
	return errors.Is(err, ErrEpochClosed) || errors.Is(err, ErrRecoverySuperseded)
}

// getDiscoveryConfig returns a filled DiscoveryConfig, replacing zero-valued
// Duration or int fields with sensible defaults.
func getDiscoveryConfig(dc DiscoveryConfig) DiscoveryConfig {
	if dc.MinInterval <= 0 {
		dc.MinInterval = 500 * time.Millisecond
	}
	if dc.MaxBackoff <= 0 {
		dc.MaxBackoff = 10 * time.Second
	}
	if dc.MaxAttempts <= 0 {
		dc.MaxAttempts = 10
	}
	if dc.ProbeTimeout <= 0 {
		dc.ProbeTimeout = 3 * time.Second
	}
	if dc.HandshakeTimeout <= 0 {
		dc.HandshakeTimeout = 10 * time.Second
	}
	return dc
}

// runSupervisor implements the core supervisory loop.  See StartPathSupervisor.
func runSupervisor(
	ctx context.Context,
	initial *BodyTunnel,
	cfg SupervisorConfig,
	epoch *RecoveryEpoch,
	h *SupervisorHandle,
	log *slog.Logger,
) {
	defer close(h.done)

	current := initial
	for {
		select {
		case <-ctx.Done():
			log.Warn("supervisor: context cancelled, shutting down")
			return
		default:
		}

		// 1. Observe the current tunnel for connectivity loss.
		log.Debug("supervisor: observing tunnel", "tunnel_addr", fmt.Sprintf("%p", current))
		lossCh := make(chan PathLossEvent, 1)
		observer, err := StartLossObserver(current, ctx, cfg.LossObserver, lossCh, log)
		if err != nil {
			log.Error("supervisor: failed to start loss observer, shutting down", "error", err)
			return
		}

		select {
		case <-lossCh:
			// Loss detected — proceed to recovery.
			observer.Cancel()
			<-observer.Done
			log.Warn("supervisor: loss detected, triggering recovery")

		case <-ctx.Done():
			observer.Cancel()
			<-observer.Done
			log.Warn("supervisor: context cancelled, shutting down")
			return
		}

		// 2. Recovery loop with retry.
		//
		// When RecoverPath returns a retryable error the old tunnel is
		// already stopped and can't be restarted, so we re-enter recovery
		// from scratch.  Terminal errors shut down the supervisor.
	recoveryLoop:
		for retry := 0; ; retry++ {
			select {
			case <-ctx.Done():
				log.Warn("supervisor: recovery cancelled, shutting down")
				return
			default:
			}

			// Create a context with the per-attempt timeout.
			attemptCtx := ctx
			var attemptCancel context.CancelFunc
			if cfg.RecoverTimeout > 0 {
				attemptCtx, attemptCancel = context.WithTimeout(ctx, cfg.RecoverTimeout)
			}

			result := RecoverPath(attemptCtx, current, cfg.PathSelector, log, epoch)

			if attemptCancel != nil {
				attemptCancel()
			}

			if result.Err == nil {
				// Recovery succeeded.  Commit was already done by
				// RecoverPath.  Start observing the new tunnel.
				current = epoch.Active()
				log.Info("supervisor: recovery complete, now observing new tunnel",
					"path", result.Path,
				)

				// 3. M6.7 slice 2: automatic better-path discovery.
				//
				// If recovered onto a fallback path and discovery is
				// enabled, probe the preferred path.  On success, perform
				// controlled migration to restore the preferred path.
				if cfg.Discovery.Enabled && result.Path != "direct" {
					log.Info("supervisor: recovered onto fallback, starting better-path discovery",
						"path", result.Path,
					)
					discCfg := getDiscoveryConfig(cfg.Discovery)

					probeTarget := discCfg.ProbeTarget
					if probeTarget == "" {
						probeTarget = cfg.PathSelector.DirectEndpoint
					}

					newTunnel := runDiscovery(ctx, current, probeTarget, cfg.PathSelector, discCfg, epoch, log)
					if newTunnel != nil {
						current = newTunnel
						log.Info("supervisor: discovery+migration complete, now on preferred path",
							"path", "direct",
						)
					} else {
						log.Warn("supervisor: discovery did not produce a migration, staying on fallback")
					}
				}

				break recoveryLoop
			}

			log.Warn("supervisor: recovery attempt failed",
				"error", result.Err, "retry", retry)

			if isRecoveryTerminal(result.Err) {
				log.Error("supervisor: terminal recovery error, shutting down",
					"error", result.Err)
				return
			}

			// Retryable — wait with exponential backoff capped at 30s.
			backoff := time.Duration(1<<min(retry, 5)) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}

			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				log.Warn("supervisor: recovery cancelled during backoff, shutting down")
				return
			case <-timer.C:
			}
		}
	}
}

// runDiscovery performs bounded better-path discovery.  It probes the
// preferred endpoint using a ProbeScheduler and, on reachability, performs
// controlled migration to transfer the active tunnel to the preferred path.
//
// Returns the new tunnel (after migration) on success, or nil if discovery
// was cancelled or exhausted.
func runDiscovery(
	ctx context.Context,
	current *BodyTunnel,
	probeTarget string,
	selector PathSelectorConfig,
	discCfg DiscoveryConfig,
	epoch *RecoveryEpoch,
	log *slog.Logger,
) *BodyTunnel {
	if probeTarget == "" {
		log.Debug("supervisor: no probe target configured, skipping discovery")
		return nil
	}

	scheduler := NewProbeScheduler(ProbeSchedulerConfig{
		MinInterval:    discCfg.MinInterval,
		MaxBackoff:     discCfg.MaxBackoff,
		MaxAttempts:    discCfg.MaxAttempts,
		InitialBackoff: discCfg.MinInterval,
	}, epoch)

	for {
		select {
		case <-ctx.Done():
			log.Info("supervisor: discovery cancelled")
			return nil
		default:
		}

		now := time.Now()
		if !scheduler.TryAcquireSlot(now) {
			log.Warn("supervisor: discovery exhausted attempts, giving up")
			return nil
		}

		log.Debug("supervisor: probing preferred path", "target", probeTarget)

		result := ReachProbe(ctx, ReachProbeConfig{
			Target:     probeTarget,
			RouteID:    uint64(selector.RouteID),
			Credential: selector.RouteCredential,
			Timeout:    discCfg.ProbeTimeout,
		})

		scheduler.RecordResult(result.Reachable)

		if !result.Reachable {
			log.Debug("supervisor: preferred path unreachable", "error", result.Error)
			continue
		}

		log.Debug("supervisor: preferred path reachable, beginning migration")

		candidate := &directCandidate{endpoint: selector.DirectEndpoint}
		migrateCfg := MigrationConfig{ProbeTimeout: discCfg.HandshakeTimeout}

		plan, err := MigratePath(ctx, epoch, candidate, migrateCfg, log)
		if err != nil {
			log.Error("supervisor: migration plan failed", "error", err)
			continue
		}

		active := epoch.Active()
		cutoverResult := CutoverPath(ctx, active, epoch, candidate, plan, migrateCfg, log)
		if cutoverResult.Err != nil {
			log.Error("supervisor: cutover failed", "error", cutoverResult.Err)
			continue
		}

		newTunnel := epoch.Active()
		log.Info("supervisor: migration to preferred path complete")
		return newTunnel
	}
}