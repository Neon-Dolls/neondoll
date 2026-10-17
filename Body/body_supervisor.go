// M6.7 — Body Path Supervisor.
//
// The Path Supervisor wires existing M6.1 loss observation to M6.2 recovery,
// using M6.3 epoch ownership to prevent concurrent or stale recovery attempts.
//
// Supervisor lifecycle:
//  1. Start observing the active Body tunnel.
//  2. On confirmed path loss, trigger bounded recovery.
//  3. After successful recovery, observe the new active tunnel.
//  4. On failure or cancellation, stop cleanly.
//
// No new transport architecture, no broad refactoring, no UI — this is a
// focused orchestrator that reuses existing primitives.

package body

import (
	"context"
	"log/slog"
	"time"
)

// SupervisorConfig controls the Path Supervisor.
type SupervisorConfig struct {
	// PathSelector configures available endpoints for recovery.
	PathSelector PathSelectorConfig

	// LossObserver configures path loss detection.
	LossObserver LossObserverConfig

	// RecoverTimeout bounds each recovery attempt.
	// Defaults to 30s when zero.
	RecoverTimeout time.Duration
}

// SupervisorHandle controls a running Path Supervisor goroutine.
type SupervisorHandle struct {
	// Done fires when the supervisor finishes — either after a failed recovery
	// attempt, cancellation, or (in future cycles) a terminal condition.
	Done   <-chan struct{}
	cancel context.CancelFunc
}

// Stop cancels the supervisor and waits for it to finish.
func (h *SupervisorHandle) Stop() {
	h.cancel()
	<-h.Done
}

// StartPathSupervisor starts a goroutine that monitors activeTunnel for path
// loss and triggers bounded recovery using the given config.
//
// The supervisor stops on:
//   - Successful recovery (continues observing the new tunnel)
//   - Context cancellation (clean shutdown)
//   - Failed recovery (all paths exhausted)
//   - Stale recovery attempt (epoch ownership lost — a previous recovery
//     already claimed the epoch for this tunnel)
//   - Loss observer setup failure
func StartPathSupervisor(
	ctx context.Context,
	activeTunnel *BodyTunnel,
	cfg SupervisorConfig,
	log *slog.Logger,
) (*SupervisorHandle, error) {
	ctx, cancel := context.WithCancel(ctx)
	h := &SupervisorHandle{cancel: cancel}

	if cfg.RecoverTimeout <= 0 {
		cfg.RecoverTimeout = 30 * time.Second
	}

	done := make(chan struct{})
	h.Done = done

	go func() {
		defer close(done)
		runSupervisor(ctx, activeTunnel, cfg, log)
	}()

	return h, nil
}

func runSupervisor(
	ctx context.Context,
	tunnel *BodyTunnel,
	cfg SupervisorConfig,
	log *slog.Logger,
) {
	epoch := NewRecoveryEpoch(tunnel)
	current := tunnel

	for {
		// ── Phase 1: Observe the current tunnel ──
		onLoss := make(chan PathLossEvent, 1)
		obs, err := StartLossObserver(current, ctx, cfg.LossObserver, onLoss, log)
		if err != nil {
			log.Error("supervisor: failed to start loss observer",
				"error", err,
			)
			return
		}

		// ── Phase 2: Wait for loss or cancellation ──
		select {
		case <-ctx.Done():
			log.Info("supervisor: cancelled, stopping observer")
			obs.Cancel()
			<-obs.Done
			return

		case ev := <-onLoss:
			log.Warn("supervisor: path loss detected",
				"reason", ev.Reason,
				"detail", ev.Detail,
			)
			obs.Cancel()
			<-obs.Done

			// ── Phase 3: Bounded recovery ──
			recoveryCtx, rcancel := context.WithTimeout(ctx, cfg.RecoverTimeout)
			result := RecoverPath(recoveryCtx, current, cfg.PathSelector, log, epoch)
			rcancel()

			if result.Err != nil {
				log.Error("supervisor: recovery failed",
					"error", result.Err,
				)
				return
			}

			log.Info("supervisor: recovery succeeded, observing new tunnel",
				"tunnel_overlay", result.Tunnel.Config().OverlayAddress,
			)
			current = result.Tunnel
			// Loop: observe the new tunnel.
		}
	}
}