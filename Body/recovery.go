// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// RecoverPath stops the active tunnel and runs the M5 path selector to find
// the next viable path, tagging the attempt with a recovery generation.
//
// The epoch argument provides generation-based ownership so that only the
// current recovery cycle may commit a candidate as active.  Superseded
// attempts (those whose generation no longer matches the epoch) clean up
// their candidate and return an error.
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
	gen := epoch.NextGen()
	if gen == 0 {
		return PathSelectionResult{
			Err: errors.New("body: recover path: recovery epoch shut down"),
		}
	}

	if err := oldTunnel.Stop(); err != nil {
		return PathSelectionResult{
			Err: fmt.Errorf("body: recover path: stop old tunnel: %w", err),
		}
	}

	result := SelectInitialPath(ctx, cfg, log)
	if result.Err != nil {
		return result
	}

	if !epoch.TryCommit(gen, result.Tunnel) {
		// Our generation is stale — another recovery started and
		// already committed its tunnel.  Clean up our candidate.
		result.Tunnel.Stop()
		result.BoundBind.Close()
		return PathSelectionResult{
			Err: errors.New("body: recover path: superseded by newer recovery attempt"),
		}
	}

	return result
}
