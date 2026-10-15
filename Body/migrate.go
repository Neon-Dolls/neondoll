// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// DefaultMigrationTimeout bounds how long a migration may spend proving the
// candidate path before giving up and keeping the old path authoritative.
const DefaultMigrationTimeout = 10 * time.Second

// MigrationConfig controls a single MigratePath operation.
type MigrationConfig struct {
	// ProbeTimeout bounds candidate validation (candidate must complete a
	// real WireGuard handshake with Core within this window). Zero uses
	// DefaultMigrationTimeout.
	ProbeTimeout time.Duration
}

func (c MigrationConfig) probeTimeout() time.Duration {
	if c.ProbeTimeout <= 0 {
		return DefaultMigrationTimeout
	}
	return c.ProbeTimeout
}

// ErrMigrationSuperseded indicates the migration lost the epoch's ownership
// race: a newer recovery or migration already committed a replacement while
// this candidate was being validated. The old tunnel was left untouched.
var ErrMigrationSuperseded = errors.New("body: migrate path: superseded by newer recovery attempt")

// MigratePath performs a controlled migration from the currently active
// fallback path to a proven better path.
//
// Unlike RecoverPath, which assumes the old path is already lost and stops the
// old tunnel before selecting a replacement, MigratePath keeps the existing
// path authoritative until the candidate has proven itself:
//
//  1. Reserve a generation. NextGenForTunnel atomically rejects stale
//     candidates (a candidate targeting an old tunnel that is no longer the
//     epoch's active tunnel) and concurrent commits (only one reconciliation
//     decision may own the epoch at a time).
//  2. Validate the candidate *without touching the old tunnel*. The candidate
//     must complete a real WireGuard handshake within the bounded window.
//     The operation is cancellable via ctx.
//  3. Commit the candidate only after validation succeeds. TryCommit fails if
//     the epoch was superseded or shut down while we were validating.
//  4. Only after a successful commit is the old tunnel stopped. The old path
//     then becomes non-authoritative and is closed; stale frames or callbacks
//     from its transport can no longer affect the new path.
//
// On any failure before commit, the epoch marker is cancelled and the old
// tunnel remains authoritative and untouched. MigratePath never commits a
// candidate that has not proven a real handshake. If cfg.ProbeTimeout is zero,
// DefaultMigrationTimeout is used.
func MigratePath(
	ctx context.Context,
	oldTunnel *BodyTunnel,
	candidate *BodyTunnel,
	cfg MigrationConfig,
	log *slog.Logger,
	epoch *RecoveryEpoch,
) PathSelectionResult {
	// 1. Reserve a generation.  NextGenForTunnel atomically checks that
	//    oldTunnel is still the epoch's active tunnel, rejecting stale
	//    candidates and concurrent recovery/migration attempts.
	gen := epoch.NextGenForTunnel(oldTunnel)
	if gen == 0 {
		return PathSelectionResult{
			Err: errors.New("body: migrate path: epoch closed or superseded"),
		}
	}

	// 2. Validate the candidate without touching the old tunnel. The old
	//    path stays authoritative for the entire validation window.
	probeTimeout := cfg.probeTimeout()
	canCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if err := candidate.WaitHandshake(canCtx, probeTimeout); err != nil {
		// Candidate failed to prove itself (or validation was
		// cancelled). Release the epoch marker; the old tunnel remains
		// untouched and authoritative.
		epoch.TryCancel(gen)
		candidate.Stop()
		return PathSelectionResult{
			Err: fmt.Errorf("body: migrate path: candidate validation: %w", err),
		}
	}

	// 3. Commit.  If stale, the new path must not be used, and the old
	//    path stays authoritative.
	if !epoch.TryCommit(gen, candidate) {
		// Our generation is stale — another recovery or migration
		// committed its tunnel while we were validating. Clean up our
		// candidate and leave the old tunnel untouched.
		candidate.Stop()
		return PathSelectionResult{Err: ErrMigrationSuperseded}
	}

	// 4. Commit succeeded: the candidate is now the epoch's active tunnel.
	//    The old path is no longer authoritative; close it so stale frames
	//    or callbacks from its transport cannot affect the new path.
	if err := oldTunnel.Stop(); err != nil {
		log.Warn("body: migrate path: stop old tunnel", "error", err)
	}

	return PathSelectionResult{Tunnel: candidate}
}