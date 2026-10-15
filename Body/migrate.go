// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrMigrationSuperseded is returned when the epoch generation was
// concurrently advanced (by another migration or recovery cycle)
// before the commit phase completed.
var ErrMigrationSuperseded = errors.New("body: migration superseded by newer cycle")

// MigrationCandidate describes a potential replacement path.
// Validate must NOT start a competing WireGuard tunnel or otherwise
// interfere with the active path — it is called during preparation,
// before any epoch ownership commitment. Only bound to candidate
// descriptors: M6.4 probe results, endpoint reachability checks, etc.
//
// Full WireGuard handshake validation is deferred to the cutover
// slice and does NOT belong here.
type MigrationCandidate interface {
	// Validate checks whether the candidate path is viable.
	// It MUST NOT initiate a WireGuard handshake or modify
	// the active tunnel's state.
	Validate(ctx context.Context) error
}

// MigrationConfig controls the migration lifecycle.
type MigrationConfig struct {
	// ProbeTimeout bounds how long candidate validation may run.
	// Zero value uses DefaultMigrationProbeTimeout.
	ProbeTimeout time.Duration
}

// ProbeTimeoutOrDefault returns ProbeTimeout, falling back to
// DefaultMigrationProbeTimeout when zero.
func (c MigrationConfig) ProbeTimeoutOrDefault() time.Duration {
	if c.ProbeTimeout <= 0 {
		return DefaultMigrationProbeTimeout
	}
	return c.ProbeTimeout
}

// DefaultMigrationProbeTimeout is the default timeout for candidate
// validation when no explicit timeout is configured.
const DefaultMigrationProbeTimeout = 10 * time.Second

// MigratePath prepares a controlled path migration by reserving epoch
// ownership and validating the candidate descriptor. It does NOT start
// a competing WireGuard tunnel — only the active path carries encrypted
// traffic throughout preparation.
//
// Lifecycle (this slice only — WG cutover is deferred to the next):
//
//  1. Reserve — epoch.NextGenForTunnel(epoch.Active()) atomically claims
//     a generation ticket, rejecting stale targets or concurrent cycles.
//  2. Validate — candidate.Validate(ctx) checks viability using the
//     M6.4 probe result, without any WG handshake initiation.
//  3. Commit — epoch.TryReserveMigration(gen) confirms the generation
//     was not bumped during validation. If bumped, returns
//     ErrMigrationSuperseded.
//
// On any failure, TryCancel(gen) is called and the epoch.Active()
// tunnel is NEVER stopped or modified. The caller receives the
// committed generation on success; this gen must be passed to the
// cutover operation (next slice) to complete the actual WG device
// and Relay return-path transition.
func MigratePath(
	ctx context.Context,
	epoch *RecoveryEpoch,
	candidate MigrationCandidate,
	cfg MigrationConfig,
	log *slog.Logger,
) (committedGen uint64, err error) {
	// 1. Reserve a generation. Read the active tunnel from the epoch
	//    — no external old-tunnel argument needed.
	oldTunnel := epoch.Active()
	if oldTunnel == nil {
		return 0, errors.New("body: migrate path: no active tunnel")
	}

	gen := epoch.NextGenForTunnel(oldTunnel)
	if gen == 0 {
		return 0, errors.New("body: migrate path: epoch closed or superseded")
	}

	// 2. Validate candidate without starting any WG device.
	validateCtx, validateCancel := context.WithTimeout(ctx, cfg.ProbeTimeoutOrDefault())
	defer validateCancel()

	if err := candidate.Validate(validateCtx); err != nil {
		epoch.TryCancel(gen)
		return 0, fmt.Errorf("body: migrate path: candidate validation: %w", err)
	}

	// 3. Commit the generation. If the gen was bumped concurrently
	//    (by recovery or another migration), TryReserveMigration
	//    returns false.
	if !epoch.TryReserveMigration(gen) {
		return 0, ErrMigrationSuperseded
	}

	return gen, nil
}