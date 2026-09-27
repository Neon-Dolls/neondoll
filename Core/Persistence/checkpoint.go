// Package persistence provides durable storage for non-identity doll
// runtime bookkeeping — pulse checkpoints, intentions, and drives.
package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Checkpoint errors returned by LoadPulseCheckpoint.
var (
	// ErrPulseCheckpointNotFound is returned when no checkpoint exists for a
	// given DollID. This is the normal first-run case — NOT an error to be
	// fatal.
	ErrPulseCheckpointNotFound = errors.New("persistence: pulse checkpoint not found")
)

// CheckpointStore is the persistence boundary for Pulse checkpoint data.
// Checkpoints are keyed by stable DollID, Core-local runtime bookkeeping,
// and NOT part of canonical Doll State or Doll Card.
//
// The interface uses raw bytes so that Persistence has no dependency on
// Core/Pulse types. Callers marshal/unmarshal their own checkpoint format.
type CheckpointStore interface {
	// SavePulseCheckpoint persists opaque checkpoint data for the given DollID.
	SavePulseCheckpoint(ctx context.Context, dollID string, data []byte) error

	// LoadPulseCheckpoint retrieves a previously-saved checkpoint.
	// Returns ErrPulseCheckpointNotFound when no checkpoint exists (normal
	// first-run). Data corruption semantics are handled by the caller's
	// deserialization layer.
	LoadPulseCheckpoint(ctx context.Context, dollID string) ([]byte, error)

	// DeletePulseCheckpoint removes a checkpoint. No-op when none exists.
	DeletePulseCheckpoint(ctx context.Context, dollID string) error
}

// Ensure *store implements CheckpointStore.
var _ CheckpointStore = (*store)(nil)

// SavePulseCheckpoint persists opaque checkpoint data for the given DollID.
// INSERT OR REPLACE semantics — only one checkpoint per doll at a time.
func (s *store) SavePulseCheckpoint(ctx context.Context, dollID string, data []byte) error {
	if dollID == "" {
		return ErrInvalidDollID
	}

	_, err := s.db.ExecContext(ctx,
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

// LoadPulseCheckpoint retrieves previously-saved checkpoint data.
// Returns ErrPulseCheckpointNotFound when no row exists.
// Corruption detection is the caller's responsibility (unmarshal errors).
func (s *store) LoadPulseCheckpoint(ctx context.Context, dollID string) ([]byte, error) {
	if dollID == "" {
		return nil, ErrInvalidDollID
	}

	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT checkpoint_json FROM pulse_checkpoints WHERE doll_id = ?`, dollID,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, ErrPulseCheckpointNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load pulse checkpoint: %v", ErrCannotDecode, err)
	}
	return []byte(data), nil
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
