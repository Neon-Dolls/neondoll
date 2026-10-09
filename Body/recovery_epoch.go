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
// Stale loss callbacks (transport failure notifications for a tunnel that
// is no longer the active path) are rejected by checking IsCurrent.  The
// caller checks the generation of the tunnel's owning epoch rather than
// acting on every loss event.
//
// There is NO cancel/coordination machinery — the epoch is purely about
// commit ownership.  In-flight SelectInitialPath calls complete normally
// and discover staleness only when they attempt to commit.
type RecoveryEpoch struct {
	mu     sync.Mutex
	gen    uint64      // current authoritative generation
	active *BodyTunnel // tunnel committed for this generation (may be nil)
	closed bool
}

// NewRecoveryEpoch returns an initialised epoch with generation 0 and no
// active tunnel.
func NewRecoveryEpoch() *RecoveryEpoch {
	return &RecoveryEpoch{}
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
	return e.gen
}

// TryCommit attempts to set candidate as the active tunnel for gen.
// Returns true if gen is still the authoritative generation (commit
// accepted).  When false, the caller must close candidate — it belongs
// to a superseded attempt.
func (e *RecoveryEpoch) TryCommit(gen uint64, candidate *BodyTunnel) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || gen != e.gen {
		return false
	}
	// Close the previous active tunnel if any.
	if e.active != nil {
		e.active.Stop()
	}
	e.active = candidate
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
