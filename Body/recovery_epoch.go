// SPDX-License-Identifier: AGPL-3.0-only
package body

import "sync"

// RecoveryEpoch provides generation-based ownership for path lifecycle
// operations so that only the current recovery cycle may commit a candidate
// as the active WireGuard path.
//
// It answers: "Does this completion still belong to the current recovery
// decision?"  Superseded attempts discover staleness and close their
// candidate tunnels.
//
// Cleanup (stop, close) is unconditional — epoch authority gates ONLY
// whether a candidate may become the active tunnel.  A stale attempt
// may always release its own resources.
//
// There is NO cancel/coordination machinery — the epoch is purely about
// commit ownership.  In-flight SelectInitialPath calls complete normally
// and discover staleness only when they attempt to commit.
//
// Duplicate loss callbacks for the same tunnel are rejected while a
// recovery for that tunnel is in-flight.  The first caller to reserve
// a generation for a given tunnel becomes the sole recovery owner;
// subsequent callers for the same tunnel get generation 0 and must
// not start recovery work.
type RecoveryEpoch struct {
	mu              sync.Mutex
	gen             uint64      // current authoritative generation
	active          *BodyTunnel // tunnel committed for this generation (may be nil)
	committed       bool        // gen has been committed exactly once
	pendingRecovery *BodyTunnel // tunnel whose recovery is in-flight (duplicate guard)
	closed          bool
}

// NewRecoveryEpoch returns an initialised epoch with generation 0.
// If initialTunnel is non-nil, it is set as the active tunnel immediately,
// making initial ownership explicit rather than implicit.
func NewRecoveryEpoch(initialTunnel *BodyTunnel) *RecoveryEpoch {
	return &RecoveryEpoch{
		active: initialTunnel,
	}
}

// NextGen reserves and returns the next generation number.  The previous
// generation is implicitly stale: any attempt that holds the old number
// will fail TryCommit.  Returns 0 when the epoch is permanently shut down.
func (e *RecoveryEpoch) NextGen() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return 0
	}
	e.gen++
	e.committed = false
	return e.gen
}

// NextGenForTunnel atomically checks that oldTunnel is still the epoch's
// active tunnel AND advances the generation.  This prevents stale loss
// callbacks (for a tunnel that was already replaced) from bumping the
// generation or triggering a spurious recovery.
//
// If a recovery for oldTunnel is already in-flight (pendingRecovery is set
// and the commit has not resolved yet), returns 0 — duplicate loss
// callbacks for the same tunnel cannot independently advance generations.
//
// Returns 0 and does NOT advance when the epoch is closed OR when a
// newer tunnel has already been committed (the loss is stale).
func (e *RecoveryEpoch) NextGenForTunnel(oldTunnel *BodyTunnel) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return 0
	}
	if e.active != nil && e.active != oldTunnel {
		return 0
	}
	// Duplicate guard: if a recovery for oldTunnel is already in-flight
	// (the commit hasn't resolved yet), reject the duplicate claim.
	if e.pendingRecovery != nil && e.pendingRecovery == oldTunnel {
		return 0
	}
	e.gen++
	e.committed = false
	e.pendingRecovery = oldTunnel
	return e.gen
}

// TryCommit attempts to set candidate as the active tunnel for gen.
// Returns true if gen is still the authoritative generation AND gen has
// not already committed (single-commit guarantee).  When false, the caller
// must close candidate — it belongs to a superseded or duplicate attempt.
// In either case, the in-flight recovery marker is cleared so that a fresh
// loss notification can start a new recovery.
//
// TryCommit does NOT stop tunnels — the caller of RecoverPath already
// owns that responsibility.  If the caller decides to abandon recovery
// without committing, call TryCancel to release the in-flight marker.
func (e *RecoveryEpoch) TryCommit(gen uint64, candidate *BodyTunnel) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || gen != e.gen || e.committed {
		e.pendingRecovery = nil
		return false
	}
	e.active = candidate
	e.committed = true
	e.pendingRecovery = nil
	return true
}

// TryCancel releases the in-flight recovery marker for gen without
// committing a candidate.  Call this when abandoning a recovery attempt
// before or after it reserved a generation — e.g. when SelectInitialPath
// fails or the caller decides not to proceed.  Does nothing if gen is no
// longer current (a superseding recovery already resolved) or the epoch
// is closed.
func (e *RecoveryEpoch) TryCancel(gen uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed && gen == e.gen {
		e.pendingRecovery = nil
	}
}

// IsCurrent returns true if gen is still the authoritative generation.
// The caller should NOT tear down tunnels or trigger recovery when this
// returns false — the notification is stale.
func (e *RecoveryEpoch) IsCurrent(gen uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.closed && gen == e.gen
}

// Active returns the currently committed tunnel, or nil.
func (e *RecoveryEpoch) Active() *BodyTunnel {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}

// Shutdown permanently closes the epoch.  Active tunnels are stopped.
// After Shutdown, NextGenForTunnel and TryCommit return failure, so
// in-flight recovery attempts discover closure through their normal
// stale-cleanup path.
func (e *RecoveryEpoch) Shutdown() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.active != nil {
		e.active.Stop()
		e.active = nil
	}
}