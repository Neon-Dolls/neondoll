// SPDX-License-Identifier: AGPL-3.0-only
package body

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"
)

// MigrationCandidate is the interface implemented by migration source/target
// resolvers.  Validate checks whether the candidate path is usable and
// returns the preferred endpoint for the new WireGuard tunnel.
//
// It MUST NOT initiate a WireGuard handshake or modify the active
// tunnel's state.
type MigrationCandidate interface {
	Validate(ctx context.Context) (netip.AddrPort, error)
}

// MigrationConfig controls the behaviour of MigratePath and CutoverPath.
type MigrationConfig struct {
	// ProbeTimeout limits how long MigratePath waits for candidate
	// validation and CutoverPath waits for the initial WireGuard handshake.
	ProbeTimeout time.Duration
}

// DefaultMigrationTimeout is the default value for ProbeTimeout when zero.
const DefaultMigrationTimeout = 10 * time.Second

// ErrMigrationSuperseded is returned when a CutoverPath attempt discovers
// that the generation it holds is no longer current (another migration
// committed first, or the epoch was shut down).
var ErrMigrationSuperseded = errors.New("migration superseded: generation no longer current")

// CutoverResult holds the outcome of a controlled migration cutover.
type CutoverResult struct {
	// Tunnel is the tunnel that should be used for active traffic after
	// cutover.  On success this is the new, committed tunnel.  On
	// handshake failure it is a rollback tunnel re-established on the
	// original config — the caller MUST take ownership of this tunnel.
	Tunnel *BodyTunnel

	// Err is non-nil when the cutover did not complete normally.  See
	// Tunnel for the fallback tunnel (or nil if rollback also failed).
	Err error
}

// MigratePath initiates a controlled path migration and returns the
// generation ticket the caller must present at cutover time.
//
// It validates the candidate and reserves the generation.  The caller
// must later call CutoverPath with the returned gen.
//
// If the epoch cannot produce a generation (closed or the old tunnel is
// no longer the active tunnel), MigratePath returns 0, ErrMigrationSuperseded.
func MigratePath(
	ctx context.Context,
	epoch *RecoveryEpoch,
	candidate MigrationCandidate,
	cfg MigrationConfig,
	log *slog.Logger,
) (uint64, error) {
	if epoch == nil {
		return 0, fmt.Errorf("migrate: nil epoch")
	}

	active := epoch.Active()
	if active == nil {
		return 0, fmt.Errorf("migrate: no active tunnel")
	}

	// Reserve the next available generation.
	gen := epoch.NextGenForTunnel(active)
	if gen == 0 {
		return 0, fmt.Errorf("migrate: %w: no generation available", ErrMigrationSuperseded)
	}

	timeout := cfg.ProbeTimeout
	if timeout <= 0 {
		timeout = DefaultMigrationTimeout
	}

	// Validate the candidate with a context that respects ProbeTimeout.
	validateCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := candidate.Validate(validateCtx); err != nil {
		epoch.TryCancel(gen)
		return 0, fmt.Errorf("migrate: candidate validate: %w", err)
	}

	// Re-check the generation: if the epoch advanced while we were
	// validating (another NextGen or migration won), this is a supersession.
	if !epoch.TryReserveMigration(gen) {
		epoch.TryCancel(gen)
		return 0, ErrMigrationSuperseded
	}

	log.Debug("migrate: candidate validated, migration reserved",
		"gen", gen,
		"probe_timeout", timeout,
	)

	return gen, nil
}

// CutoverPath completes a controlled path migration by:
//
//  1. Re-validating the candidate to obtain the target endpoint.
//  2. Checking the generation ticket is still current.
//  3. Creating and starting a new WireGuard tunnel that preserves the
//     original identity (private key, overlay address, membership).
//  4. Waiting for the initial WireGuard handshake to complete.
//  5. Committing the new tunnel atomically via the epoch.
//  6. Stopping the old tunnel.
//
// If the handshake fails, CutoverPath creates a rollback tunnel on the
// original endpoint path and returns it (with a non-nil Err).  The caller
// MUST take ownership of the rollback tunnel.
//
// If the generation was consumed by another migration, CutoverPath returns
// ErrMigrationSuperseded (with no rollback — another path already won).
func CutoverPath(
	ctx context.Context,
	oldTunnel *BodyTunnel,
	epoch *RecoveryEpoch,
	candidate MigrationCandidate,
	gen uint64,
	cfg MigrationConfig,
	log *slog.Logger,
) CutoverResult {
	timeout := cfg.ProbeTimeout
	if timeout <= 0 {
		timeout = DefaultMigrationTimeout
	}

	// 1. Re-validate the candidate to get the target endpoint.
	endpoint, err := candidate.Validate(ctx)
	if err != nil {
		return CutoverResult{Err: fmt.Errorf("cutover: candidate validate: %w", err)}
	}

	// 2. Quick guard: is this generation still current?
	if !epoch.TryReserveMigration(gen) {
		log.Debug("cutover: generation stale, migration superseded", "gen", gen)
		return CutoverResult{Err: ErrMigrationSuperseded}
	}

	// 3. Build the new tunnel config — preserve identity, change endpoint.
	oldCfg := oldTunnel.cfg
	newCfg := oldCfg
	newCfg.CoreEndpoint = endpoint.String()
	newCfg.Bind = nil // let Start allocate a fresh UDP bind
	newTunnel := NewBodyTunnel(newCfg, log)

	// 4. Start the new tunnel.
	if err := newTunnel.Start(ctx); err != nil {
		newTunnel.Stop()
		epoch.TryCancel(gen)
		log.Warn("cutover: new tunnel start failed, rolling back", "err", err)
		rollback := buildRollbackTunnel(oldCfg, log)
		if rbErr := rollback.Start(ctx); rbErr != nil {
			return CutoverResult{Err: fmt.Errorf("cutover: start %w, rollback: %v", err, rbErr)}
		}
		return CutoverResult{Tunnel: rollback, Err: fmt.Errorf("cutover: start: %w", err)}
	}

	// 5. Wait for the initial WireGuard handshake.
	hsCtx, hsCancel := context.WithTimeout(ctx, timeout)
	defer hsCancel()
	if err := newTunnel.WaitHandshake(hsCtx, timeout); err != nil {
		newTunnel.Stop()
		epoch.TryCancel(gen)
		log.Warn("cutover: handshake failed, rolling back", "err", err)
		rollback := buildRollbackTunnel(oldCfg, log)
		if rbErr := rollback.Start(ctx); rbErr != nil {
			return CutoverResult{Err: fmt.Errorf("cutover: handshake %w, rollback: %v", err, rbErr)}
		}
		return CutoverResult{Tunnel: rollback, Err: fmt.Errorf("cutover: handshake: %w", err)}
	}

	// 6. Commit atomically — make this tunnel the epoch's active tunnel.
	if !epoch.TryCommit(gen, newTunnel) {
		newTunnel.Stop()
		epoch.TryCancel(gen)
		log.Warn("cutover: commit race — generation claimed by another migration", "gen", gen)
		rollback := buildRollbackTunnel(oldCfg, log)
		if rbErr := rollback.Start(ctx); rbErr != nil {
			return CutoverResult{Err: fmt.Errorf("cutover: commit %w, rollback: %v", ErrMigrationSuperseded, rbErr)}
		}
		return CutoverResult{Tunnel: rollback, Err: fmt.Errorf("cutover: %w", ErrMigrationSuperseded)}
	}

	// 7. Cutover complete — stop the old path.
	oldTunnel.Stop()
	log.Debug("cutover: completed successfully", "gen", gen, "endpoint", endpoint)

	return CutoverResult{Tunnel: newTunnel, Err: nil}
}

// buildRollbackTunnel creates a *BodyTunnel from oldCfg that will be wired
// back to the original endpoint.  It does NOT start it — the caller must
// call Start.
func buildRollbackTunnel(oldCfg BodyTunnelConfig, log *slog.Logger) *BodyTunnel {
	rbCfg := oldCfg
	rbCfg.Bind = nil // fresh UDP bind, never reuse a closed one
	return NewBodyTunnel(rbCfg, log)
}