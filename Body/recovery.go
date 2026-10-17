// SPDX-License-Identifier: AGPL-3.0-only

package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Sentinel errors for recovery outcomes.
var (
	ErrEpochClosed        = errors.New("body: recover path: epoch closed or superseded")
	ErrRecoverySuperseded = errors.New("body: recover path: superseded by newer recovery attempt")
)

// RecoverPath carries out a recovery attempt: it advances the epoch to a new
// generation, stops the old tunnel, selects a new path via SelectInitialPath,
// and commits the result.  If any step fails it returns a PathSelectionResult
// with a non-nil Err.
//
// Errors are classified as:
//   - ErrEpochClosed / ErrRecoverySuperseded — terminal; another owner
//     or a newer attempt has taken over.
//   - ErrPathCancelled — the context was cancelled.  The caller should
//     shut down cleanly.
//   - All other errors (stop failure, path selection failure) are
//     retryable — the old tunnel is gone but a fresh attempt can start.
func RecoverPath(
	ctx context.Context,
	oldTunnel *BodyTunnel,
	cfg PathSelectorConfig,
	log *slog.Logger,
	epoch *RecoveryEpoch,
) PathSelectionResult {
	// 1. Reserve a generation.  NextGenForTunnel atomically checks that
	//    oldTunnel is still the epoch's active tunnel, rejecting stale
	//    loss callbacks without advancing the epoch.
	gen := epoch.NextGenForTunnel(oldTunnel)
	if gen == 0 {
		return PathSelectionResult{
			Err: ErrEpochClosed,
		}
	}

	// 2. Stop the old tunnel unconditionally.  Resource cleanup is always
	//    permitted — epoch authority gates commit, not resource release.
	if err := oldTunnel.Stop(); err != nil {
		// Release the pending-recovery marker so a subsequent recovery
		// attempt can start from a fresh generation.
		epoch.TryCancel(gen)
		return PathSelectionResult{
			Err: fmt.Errorf("body: recover path: stop old tunnel: %w", err),
		}
	}

	// 3. Select a new path.
	result := SelectInitialPath(ctx, cfg, log)
	if result.Err != nil {
		// SelectInitialPath failed.  Release the pending-recovery marker
		// so a subsequent recovery attempt can start fresh.
		epoch.TryCancel(gen)
		return result
	}

	// 4. Commit.  If stale, close candidate.
	if !epoch.TryCommit(gen, result.Tunnel) {
		// Our generation is stale — another recovery started and
		// already committed its tunnel.  Clean up our candidate.
		result.Tunnel.Stop()
		if result.BoundBind != nil {
			result.BoundBind.Close()
		}
		return PathSelectionResult{
			Err: ErrRecoverySuperseded,
		}
	}

	return result
}