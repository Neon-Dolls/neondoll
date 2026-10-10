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
// There is NO cancel/coordination machinery — the epoch is purely about
// commit ownership.  In-flight SelectInitialPath calls complete normally
// and discover staleness only when they attempt to commit.
type RecoveryEpoch struct {
	mu        sync.Mutex
	gen       uint64      // current authoritative generation
	active    *BodyTunnel // tunnel committed for this generation (may be nil)
	committed bool        // gen has been committed exactly once
	closed    bool
}

// NewRecoveryEpoch returns an initialised epoch with generation 0 and no
// active tunnel.
func NewRecoveryEpoch() *RecoveryEpoch {
	return &RecoveryEpoch{}
}

// NextGen reserves and returns the next generation number.  The previous
// generation is implicitly stale: any attempt that holds the old number
// will fail TryCommit.  Returns 0 when the epoch is permanently shut down.
//
// The OLD tunnel must be stopped by the caller BEFORE calling NextGen, so
// that any loss callbacks fire before the epoch advances.  This prevents
// stale callbacks from racing with a newer generation.
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

// TryCommit attempts to set candidate as the active tunnel for gen.
// Returns true if gen is still the authoritative generation AND gen has
// not already committed (single-commit guarantee).  When false, the caller
// must close candidate — it belongs to a superseded or duplicate attempt.
//
// The previous generation's tunnel must already be stopped by the caller
// of RecoverPath, so TryCommit does NOT stop any tunnel.
func (e *RecoveryEpoch) TryCommit(gen uint64, candidate *BodyTunnel) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || gen != e.gen || e.committed {
		return false
	}
	e.active = candidate
	e.committed = true
	return true
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

// Shutdown permanently invalidates all generations and closes the active
// tunnel.
func (e *RecoveryEpoch) Shutdown() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.active != nil {
		e.active.Stop()
		e.active = nil
	}
}