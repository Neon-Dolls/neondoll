package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/DollState"
)

// LoadSelf reads only the active portable Self snapshot. It does not expose or
// construct revision history.
func (s *store) LoadSelf(ctx context.Context, dollID string) (dollstate.Self, error) {
	if dollID == "" {
		return dollstate.Self{}, ErrInvalidDollID
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT self_json FROM dolls WHERE doll_id = ?`, dollID).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return dollstate.Self{}, ErrDollNotFound
		}
		return dollstate.Self{}, fmt.Errorf("%w: load Self: %v", ErrCannotDecode, err)
	}
	var current dollstate.Self
	if err := json.Unmarshal([]byte(raw), &current); err != nil {
		return dollstate.Self{}, fmt.Errorf("%w: Self: %v", ErrCannotDecode, err)
	}
	return current, nil
}

// ReplaceSelf atomically replaces self_json only when the persisted revision
// still matches expectedRevisionID. The exact previous JSON is used as the SQL
// compare value, so concurrent writers cannot both activate a revision.
func (s *store) ReplaceSelf(ctx context.Context, dollID, expectedRevisionID string, next dollstate.Self) (bool, error) {
	if dollID == "" {
		return false, ErrInvalidDollID
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT self_json FROM dolls WHERE doll_id = ?`, dollID).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return false, ErrDollNotFound
		}
		return false, fmt.Errorf("%w: load Self for replacement: %v", ErrCannotSave, err)
	}
	var current dollstate.Self
	if err := json.Unmarshal([]byte(raw), &current); err != nil {
		return false, fmt.Errorf("%w: decode Self for replacement: %v", ErrCannotSave, err)
	}
	if current.RevisionID != expectedRevisionID {
		return false, nil
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return false, fmt.Errorf("%w: encode Self replacement: %v", ErrCannotSave, err)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE dolls SET self_json = ?, updated_at = ? WHERE doll_id = ? AND self_json = ?`,
		string(encoded), time.Now().UTC().Format(time.RFC3339Nano), dollID, raw)
	if err != nil {
		return false, fmt.Errorf("%w: replace Self: %v", ErrCannotSave, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: replace Self result: %v", ErrCannotSave, err)
	}
	return rows == 1, nil
}
