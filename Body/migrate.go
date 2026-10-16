package body

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// ---------------------------------------------------------------------------
// MigrationCandidate
// ---------------------------------------------------------------------------

// MigrationTarget describes the path to establish during cutover. The
// candidate creates the MigrationTarget during Validate; the caller owns
// it and must eventually call Stop to release candidate resources.
type MigrationTarget struct {
	// CoreEndpoint is the core's reachable address for the new path in
	// "ip:port" format.
	CoreEndpoint string

	// Bind is an optional non-default WireGuard transport bind. When nil
	// the new tunnel uses a default UDP bind. Non-nil supports alternative
	// transports such as Relay or WSS. The candidate retains ownership of
	// the bind until its Stop method is called.
	Bind conn.Bind
}

// MigrationCandidate describes a migration target path. Validate checks
// viability and returns a MigrationTarget on success. Stop releases any
// resources the candidate allocated.
type MigrationCandidate interface {
	Validate(ctx context.Context) (*MigrationTarget, error)
	Stop()
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var ErrMigrationSuperseded = fmt.Errorf("migration: superseded by newer generation")

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// MigrationConfig controls migration timeouts.
type MigrationConfig struct {
	ProbeTimeout time.Duration
}

// DefaultMigrationTimeout is used when ProbeTimeout is zero.
const DefaultMigrationTimeout = 10 * time.Second

// ---------------------------------------------------------------------------
// MigratePath — reserve-only phase
// ---------------------------------------------------------------------------

// MigratePath validates a migration candidate and reserves a generation
// ticket on the provided epoch. The returned generation must be passed to
// CutoverPath or TryCancel.
func MigratePath(
	ctx context.Context,
	epoch *RecoveryEpoch,
	candidate MigrationCandidate,
	cfg MigrationConfig,
	log *slog.Logger,
) (uint64, error) {
	timeout := cfg.ProbeTimeout
	if timeout <= 0 {
		timeout = DefaultMigrationTimeout
	}

	validateCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if _, err := candidate.Validate(validateCtx); err != nil {
		return 0, fmt.Errorf("migrate: candidate validate: %w", err)
	}

	gen := epoch.NextGenForTunnel(nil) // TODO: pass actual tunnel once epoch supports it
	if !epoch.TryReserveMigration(gen) {
		candidate.Stop()
		return 0, ErrMigrationSuperseded
	}

	log.Debug("migrate: candidate validated, migration reserved",
		"gen", gen)
	return gen, nil
}

// ---------------------------------------------------------------------------
// CutoverPath — break-before-make cutover
// ---------------------------------------------------------------------------

// CutoverResult holds the outcome of a cutover attempt.
type CutoverResult struct {
	Tunnel *BodyTunnel // active tunnel after cutover; non-nil on success or successful rollback
	Err    error       // nil on success; non-nil describes the failure
}

// CutoverPath completes a controlled break-before-make path migration:
//
//  1. Re-validate the candidate to obtain the target endpoint.
//  2. Check the generation ticket is still current (TryReserveMigration).
//     Stale generations are rejected before any state change.
//  3. BREAK: stop the old tunnel's packet transport. Traffic pauses here.
//  4. MAKE: build a new tunnel from the preserved identity and the
//     candidate's MigrationTarget (endpoint + optional transport bind).
//  5. Start the new tunnel and wait for the initial WireGuard handshake.
//  6. COMMIT: atomically register the new tunnel with the epoch.
//
// If any step after BREAK fails, CutoverPath attempts a rollback: it
// constructs a tunnel on the original endpoint path and commits it to
// the epoch so epoch.Active() stays consistent. If rollback also fails,
// no tunnel remains active and the caller must trigger recovery.
//
// Temporary interruption during cutover (between BREAK and COMMIT or
// ROLLBACK) is expected and acceptable.
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

	// ------------------------------------------------------------------
	// 1. Re-validate candidate, obtain target.
	// ------------------------------------------------------------------
	target, err := candidate.Validate(ctx)
	if err != nil {
		return CutoverResult{Err: fmt.Errorf("cutover: candidate validate: %w", err)}
	}
	log.Debug("cutover: candidate re-validated",
		"endpoint", target.CoreEndpoint,
		"nonUDPBind", target.Bind != nil)

	// ------------------------------------------------------------------
	// 2. Fast generation guard. Read-only — no epoch mutation.
	//    If the gen is already stale we abort before touching anything.
	// ------------------------------------------------------------------
	if !epoch.TryReserveMigration(gen) {
		candidate.Stop()
		return CutoverResult{Err: ErrMigrationSuperseded}
	}

	// ------------------------------------------------------------------
	// 3. BREAK — stop the old tunnel's packet transport.
	//    epoch.Active() still returns the old (stopped) tunnel until
	//    commit. Traffic is interrupted from here until the new tunnel
	//    or rollback completes.
	// ------------------------------------------------------------------
	oldCfg := oldTunnel.Config()
	log.Debug("cutover: braking old path", "endpoint", oldCfg.CoreEndpoint)
	if stopErr := oldTunnel.Stop(); stopErr != nil {
		log.Error("cutover: break failed", "err", stopErr)
		candidate.Stop()
		return buildAndCommitRollback(ctx, oldCfg, epoch, gen, timeout, log)
	}

	// ------------------------------------------------------------------
	// 4. MAKE — build new tunnel from identity + candidate target.
	// ------------------------------------------------------------------
	newCfg := oldCfg
	newCfg.CoreEndpoint = target.CoreEndpoint
	newCfg.Bind = target.Bind
	newTunnel := NewBodyTunnel(newCfg, log)
	candidate.Stop() // candidate resources no longer needed

	// ------------------------------------------------------------------
	// 5. Start + wait for WG handshake.
	// ------------------------------------------------------------------
	startCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if startErr := newTunnel.Start(startCtx); startErr != nil {
		newTunnel.Stop()
		log.Warn("cutover: new tunnel start failed, rolling back", "err", startErr)
		return buildAndCommitRollback(ctx, oldCfg, epoch, gen, timeout, log)
	}

	hsCtx, hsCancel := context.WithTimeout(ctx, timeout)
	defer hsCancel()
	if hsErr := newTunnel.WaitHandshake(hsCtx, timeout); hsErr != nil {
		newTunnel.Stop()
		log.Warn("cutover: handshake failed, rolling back", "err", hsErr)
		return buildAndCommitRollback(ctx, oldCfg, epoch, gen, timeout, log)
	}

	// ------------------------------------------------------------------
	// 6. COMMIT — atomically register as epoch's active tunnel.
	// ------------------------------------------------------------------
	if !epoch.TryCommit(gen, newTunnel) {
		newTunnel.Stop()
		log.Warn("cutover: commit race — generation superseded, rolling back",
			"gen", gen)
		return buildAndCommitRollback(ctx, oldCfg, epoch, gen, timeout, log)
	}

	log.Debug("cutover: completed successfully",
		"gen", gen,
		"endpoint", target.CoreEndpoint)
	return CutoverResult{Tunnel: newTunnel}
}

// ---------------------------------------------------------------------------
// Rollback helper
// ---------------------------------------------------------------------------

// buildAndCommitRollback constructs a rollback tunnel from the original
// config (preserved before BREAK), starts it, waits for the WG handshake,
// and commits it to the epoch. If the rollback also fails the epoch is
// left with no functional active tunnel — the caller must trigger
// recovery.
//
// On success the rollback is registered with the epoch so
// epoch.Active() is consistent with the running tunnel.
func buildAndCommitRollback(
	ctx context.Context,
	oldCfg BodyTunnelConfig,
	epoch *RecoveryEpoch,
	gen uint64,
	timeout time.Duration,
	log *slog.Logger,
) CutoverResult {
	rbCfg := oldCfg
	rbCfg.Bind = nil // fresh UDP bind — the old bind was closed during BREAK
	rbTunnel := NewBodyTunnel(rbCfg, log)
	log.Debug("cutover: constructing rollback tunnel",
		"endpoint", rbCfg.CoreEndpoint)

	if startErr := rbTunnel.Start(ctx); startErr != nil {
		rbTunnel.Stop()
		return CutoverResult{Err: fmt.Errorf("cutover: rollback start: %w", startErr)}
	}

	hsCtx, hsCancel := context.WithTimeout(ctx, timeout)
	defer hsCancel()
	if hsErr := rbTunnel.WaitHandshake(hsCtx, timeout); hsErr != nil {
		rbTunnel.Stop()
		return CutoverResult{Err: fmt.Errorf("cutover: rollback handshake: %w", hsErr)}
	}

	if !epoch.TryCommit(gen, rbTunnel) {
		rbTunnel.Stop()
		return CutoverResult{Err: fmt.Errorf("cutover: rollback commit: %w", ErrMigrationSuperseded)}
	}

	log.Warn("cutover: rolled back to original path", "endpoint", rbCfg.CoreEndpoint)
	return CutoverResult{
		Tunnel: rbTunnel,
		Err:    fmt.Errorf("cutover: migration failed, rolled back to original endpoint"),
	}
}
