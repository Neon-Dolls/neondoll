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
// The old tunnel is STOPPED FIRST, before the epoch advances.  This
// ensures any loss callbacks fire while the epoch still reflects the
// old generation, preventing re-entrant races where a stale callback
// could advance the epoch or retire a newer active path.
//
// Superseded attempts (those whose generation no longer matches the
// epoch) clean up their candidate and return an error.
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
	// 1. Stop the old tunnel FIRST.  Any loss callback fires while the
	//    epoch still reflects the previous generation, so a re-entrant
	//    recovery attempt cannot race our NextGen.
	if err := oldTunnel.Stop(); err != nil {
		return PathSelectionResult{
			Err: fmt.Errorf("body: recover path: stop old tunnel: %w", err),
		}
	}

	// 2. Advance the epoch — old tunnel is already stopped.
	gen := epoch.NextGen()
	if gen == 0 {
		return PathSelectionResult{
			Err: errors.New("body: recover path: recovery epoch shut down"),
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