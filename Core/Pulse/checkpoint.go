package pulse

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// PulseCheckpoint — durable representation of Pulse runtime bookkeeping.
// Written at lifecycle boundaries (wake admission, cognition completion) and
// restored when a *new* Runner is created in a fresh process.
//
// The struct is JSON-serializable so it can live in any backend that handles
// arbitrary bytes. It does NOT store ephemeral state (tick_count, last_tick_at,
// RNG, signal snapshots, opportunity snapshots, goroutine handles, timers,
// provider connections, interaction state, cognition_run_active).
// ---------------------------------------------------------------------------

// PulseSubjectCheckpoint is the per-subject data that survives a restart.
type PulseSubjectCheckpoint struct {
	SubjectID             string    `json:"subject_id"`
	LastPresentedAt       time.Time `json:"last_presented_at,omitempty"`
	RevisionAtLastPresent int64     `json:"revision_at_last_present"`
	ChangesSincePresent   int64     `json:"changes_since_present"`
	LastSettledAt         time.Time `json:"last_settled_at,omitempty"`
}

// PulseCheckpoint holds the minimal Pulse runtime bookkeeping that must be
// restored after a process restart. DollID identifies which doll this
// checkpoint belongs to; only one checkpoint exists per doll at a time.
type PulseCheckpoint struct {
	DollID                string                   `json:"doll_id"`
	LastCognitionAt       time.Time                `json:"last_cognition_at,omitempty"`
	LastSpontaneousWakeAt time.Time                `json:"last_spontaneous_wake_at,omitempty"`
	Subjects              []PulseSubjectCheckpoint `json:"subjects,omitempty"`
}

// ToCheckpoint converts the current Runner's checkpoint-relevant fields
// into a PulseCheckpoint. The returned checkpoint has an empty DollID — the
// caller must set it before persisting (typically the persistence layer knows
// which doll the checkpoint belongs to).
func (r *Runner) ToCheckpoint() PulseCheckpoint {
	r.mu.Lock()
	defer r.mu.Unlock()

	cp := PulseCheckpoint{
		LastCognitionAt:       r.lastCognitionAt,
		LastSpontaneousWakeAt: r.lastSpontaneousWakeAt,
	}

	subjs := make([]PulseSubjectCheckpoint, len(r.subjects))
	for i, s := range r.subjects {
		subjs[i] = PulseSubjectCheckpoint{
			SubjectID:             s.SubjectID,
			LastPresentedAt:       s.LastPresentedAt,
			RevisionAtLastPresent: s.RevisionAtLastPresent,
			ChangesSincePresent:   s.ChangesSincePresent,
			LastSettledAt:         s.LastSettledAt,
		}
	}
	cp.Subjects = subjs

	return cp
}

// RestoreFromCheckpoint applies a previously-saved PulseCheckpoint to this
// Runner, reconstructing all durable runtime bookkeeping. This must be called
// BEFORE Start() on a fresh Runner — never on a running instance.
//
// cognition_run_active starts false (default atomic value) in the new process.
// Tick counter, signal snapshots, opportunity snapshots, RNG, goroutines,
// timers, context, provider state, and Interaction state are NOT restored.
// Stale checkpoint subjects (stable IDs not in the current subject list) are
// discarded — they never become fabricated semantic subjects.
func (r *Runner) RestoreFromCheckpoint(cp PulseCheckpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.lastCognitionAt = cp.LastCognitionAt
	r.lastSpontaneousWakeAt = cp.LastSpontaneousWakeAt

	// Build a lookup from checkpoint subjects.
	restored := make(map[string]PulseSubjectCheckpoint, len(cp.Subjects))
	for _, sc := range cp.Subjects {
		restored[sc.SubjectID] = sc
	}

	// Reconcile: merge checkpoint data into current subjects by stable ID.
	// Subjects not in the checkpoint keep their zero state.
	reconciled := make([]PulseSubjectState, len(r.subjects))
	for i, s := range r.subjects {
		if sc, ok := restored[s.SubjectID]; ok {
			s.LastPresentedAt = sc.LastPresentedAt
			s.RevisionAtLastPresent = sc.RevisionAtLastPresent
			s.ChangesSincePresent = sc.ChangesSincePresent
			s.LastSettledAt = sc.LastSettledAt
		}
		reconciled[i] = s
	}
	r.subjects = reconciled

	// cognition_run_active starts false — inherited from NewRunner (atomic zero).
	// Tick counter, ephemeral state are not restored.
}

// MarshalCheckpoint serializes a PulseCheckpoint to JSON.
func MarshalCheckpoint(cp PulseCheckpoint) ([]byte, error) {
	return json.Marshal(cp)
}

// UnmarshalCheckpoint deserializes a PulseCheckpoint from JSON.
func UnmarshalCheckpoint(data []byte) (PulseCheckpoint, error) {
	var cp PulseCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return PulseCheckpoint{}, err
	}
	return cp, nil
}
