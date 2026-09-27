package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	pulse "github.com/Neon-Dolls/neondoll/Core/Pulse"
)

// Checkpoint errors returned by LoadPulseCheckpoint.
var (
	// ErrPulseCheckpointNotFound is returned when no checkpoint exists for a
	// given DollID. This is the normal first-run case — NOT an error to be
	// fatal.
	ErrPulseCheckpointNotFound = errors.New("persistence: pulse checkpoint not found")

	// ErrPulseCheckpointCorrupt is returned when a stored checkpoint cannot be
	// decoded. This is a genuine integrity error; callers should log and
	// escalate rather than silently recovering.
	ErrPulseCheckpointCorrupt = errors.New("persistence: pulse checkpoint corrupt")
)

// CheckpointStore is the persistence boundary for Pulse checkpoint data.
// Checkpoints are keyed by stable DollID, Core-local runtime bookkeeping,
// and NOT part of canonical Doll State or Doll Card.
type CheckpointStore interface {
	// SavePulseCheckpoint persists a PulseCheckpoint for the given DollID.
	SavePulseCheckpoint(ctx context.Context, dollID string, cp pulse.PulseCheckpoint) error

	// LoadPulseCheckpoint retrieves a previously-saved PulseCheckpoint.
	// Returns ErrPulseCheckpointNotFound when no checkpoint exists (normal
	// first-run). Returns ErrPulseCheckpointCorrupt when stored data is
	// unparseable — that is a genuine corruption that should be escalated.
	LoadPulseCheckpoint(ctx context.Context, dollID string) (pulse.PulseCheckpoint, error)

	// DeletePulseCheckpoint removes a checkpoint. No-op when none exists.
	DeletePulseCheckpoint(ctx context.Context, dollID string) error
}

// Ensure *store implements CheckpointStore.
var _ CheckpointStore = (*store)(nil)

// SavePulseCheckpoint persists a PulseCheckpoint for the given DollID.
// INSERT OR REPLACE semantics — only one checkpoint per doll at a time.
func (s *store) SavePulseCheckpoint(ctx context.Context, dollID string, cp pulse.PulseCheckpoint) error {
	if dollID == "" {
		return ErrInvalidDollID
	}

	data, err := pulse.MarshalCheckpoint(cp)
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrCannotSave, err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO pulse_checkpoints (doll_id, checkpoint_json, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(doll_id) DO UPDATE SET
			checkpoint_json = excluded.checkpoint_json,
			updated_at = excluded.updated_at`,
		dollID, string(data),
	)
	if err != nil {
		return fmt.Errorf("%w: save pulse checkpoint: %v", ErrCannotSave, err)
	}
	return nil
}

// LoadPulseCheckpoint retrieves a previously-saved PulseCheckpoint.
// Returns ErrPulseCheckpointNotFound when no row exists, and
// ErrPulseCheckpointCorrupt when the JSON blob is unparseable.
func (s *store) LoadPulseCheckpoint(ctx context.Context, dollID string) (pulse.PulseCheckpoint, error) {
	if dollID == "" {
		return pulse.PulseCheckpoint{}, ErrInvalidDollID
	}

	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT checkpoint_json FROM pulse_checkpoints WHERE doll_id = ?`, dollID,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return pulse.PulseCheckpoint{}, ErrPulseCheckpointNotFound
	}
	if err != nil {
		return pulse.PulseCheckpoint{}, fmt.Errorf("%w: load pulse checkpoint: %v", ErrCannotDecode, err)
	}

	cp, err := pulse.UnmarshalCheckpoint([]byte(data))
	if err != nil {
		return pulse.PulseCheckpoint{}, fmt.Errorf("%w: %v", ErrPulseCheckpointCorrupt, err)
	}
	return cp, nil
}

// DeletePulseCheckpoint removes a checkpoint. No-op when none exists.
func (s *store) DeletePulseCheckpoint(ctx context.Context, dollID string) error {
	if dollID == "" {
		return ErrInvalidDollID
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM pulse_checkpoints WHERE doll_id = ?`, dollID)
	if err != nil {
		return fmt.Errorf("delete pulse checkpoint: %v", err)
	}
	return nil
}

// EnsureCheckpointJSON is a helper that marshals a PulseCheckpoint to a JSON
// string for SQL storage. This guarantees the round-trip through the same
// MarshalCheckpoint/UnmarshalCheckpoint used by persistence.
func EnsureCheckpointJSON(cp pulse.PulseCheckpoint) (string, error) {
	data, err := pulse.MarshalCheckpoint(cp)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ParseCheckpointJSON is a helper that deserializes a JSON string into a
// PulseCheckpoint, returning ErrPulseCheckpointCorrupt on parse failure.
func ParseCheckpointJSON(data string) (pulse.PulseCheckpoint, error) {
	var cp pulse.PulseCheckpoint
	if err := json.Unmarshal([]byte(data), &cp); err != nil {
		return pulse.PulseCheckpoint{}, fmt.Errorf("%w: %v", ErrPulseCheckpointCorrupt, err)
	}
	return cp, nil
}
