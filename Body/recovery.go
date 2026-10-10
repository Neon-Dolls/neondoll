// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// RecoverPath replaces the active tunnel with a freshly selected path,
// using epoch-based generation ownership so that only the current
// recovery cycle may commit a candidate as active.
//
// Ownership check (via NextGenForTunnel) runs atomically with generation
// advancement, preventing a stale loss callback (for an already-replaced
// tunnel) from bumping the epoch and invalidating a healthy recovery.
//
// The old tunnel is stopped AFTER the genen claim, not before.  Neither
// epoch state nor tunnel liveness gate resource cleanup — a stale attempt
// may always release its own candidate.
//
// Superseded attempts clean up their candidate and return an error.
//
// Identity, pairing, membership, and credentials are preserved without
// re-pairing.  Background probing, preferred-path return, and migration
// are M6.4+ scope and are NOT covered here.
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
			Err: errors.New("body: recover path: epoch closed or superseded"),
		}
	}

	// 2. Stop the old tunnel unconditionally.  Resource cleanup is always
	//    permitted — epoch authority gates commit, not resource release.
	if err := oldTunnel.Stop(); err != nil {
		return PathSelectionResult{
			Err: fmt.Errorf("body: recover path: stop old tunnel: %w", err),
		}
	}

	// 3. Select a new path.
	result := SelectInitialPath(ctx, cfg, log)
	if result.Err != nil {
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
			Err: errors.New("body: recover path: superseded by newer recovery attempt"),
		}
	}

	return result
}