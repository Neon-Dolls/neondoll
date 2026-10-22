package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Session"
)

var _ session.Store = (*store)(nil)

const sessionColumns = `session_id, owner_kind, owner_instance_id, external_conversation_ref, status, created_at, updated_at`

func (s *store) CreateSession(ctx context.Context, value *session.InteractionSession) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO interaction_sessions (`+sessionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		value.ID, value.Owner.Kind, value.Owner.InstanceID, value.ExternalConversationRef, value.Status,
		formatTime(value.CreatedAt), formatTime(value.UpdatedAt))
	if err != nil {
		if value.ExternalConversationRef != "" {
			_, conflictErr := s.LoadActiveSessionByConversation(ctx, value.Owner, value.ExternalConversationRef)
			if conflictErr == nil {
				return session.ErrConversationInUse
			}
		}
		return fmt.Errorf("create interaction session: %w", err)
	}
	return nil
}

func (s *store) LoadSession(ctx context.Context, id session.ID) (*session.InteractionSession, error) {
	return scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM interaction_sessions WHERE session_id = ?`, id))
}

func (s *store) LoadActiveSessionByConversation(ctx context.Context, owner session.Owner, externalRef string) (*session.InteractionSession, error) {
	return scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM interaction_sessions
		WHERE owner_kind = ? AND owner_instance_id = ? AND external_conversation_ref = ? AND status = ?`,
		owner.Kind, owner.InstanceID, externalRef, session.StatusActive))
}

func (s *store) ResumeSession(ctx context.Context, id session.ID, now time.Time) (*session.InteractionSession, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE interaction_sessions SET updated_at = ? WHERE session_id = ? AND status = ?`,
		formatTime(now), id, session.StatusActive)
	if err != nil {
		return nil, fmt.Errorf("resume interaction session: %w", err)
	}
	return sessionTransitionResult(ctx, s.db, result, id)
}

func (s *store) CloseSession(ctx context.Context, id session.ID, now time.Time) (*session.InteractionSession, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE interaction_sessions SET status = ?, updated_at = ? WHERE session_id = ? AND status = ?`,
		session.StatusClosed, formatTime(now), id, session.StatusActive)
	if err != nil {
		return nil, fmt.Errorf("close interaction session: %w", err)
	}
	return sessionTransitionResult(ctx, s.db, result, id)
}

func sessionTransitionResult(ctx context.Context, db *sql.DB, result sql.Result, id session.ID) (*session.InteractionSession, error) {
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 1 {
		return scanSession(db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM interaction_sessions WHERE session_id = ?`, id))
	}
	var status session.Status
	err = db.QueryRowContext(ctx, `SELECT status FROM interaction_sessions WHERE session_id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return nil, session.ErrInvalidTransition
}

func (s *store) ListActiveSessions(ctx context.Context, owner *session.Owner) ([]*session.InteractionSession, error) {
	query := `SELECT ` + sessionColumns + ` FROM interaction_sessions WHERE status = ?`
	args := []any{session.StatusActive}
	if owner != nil {
		query += ` AND owner_kind = ? AND owner_instance_id = ?`
		args = append(args, owner.Kind, owner.InstanceID)
	}
	query += ` ORDER BY created_at, session_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*session.InteractionSession
	for rows.Next() {
		value, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func scanSession(row rowScanner) (*session.InteractionSession, error) {
	var value session.InteractionSession
	var createdAt, updatedAt string
	err := row.Scan(&value.ID, &value.Owner.Kind, &value.Owner.InstanceID, &value.ExternalConversationRef,
		&value.Status, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	value.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("decode interaction session created_at: %w", err)
	}
	value.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("decode interaction session updated_at: %w", err)
	}
	return &value, nil
}
