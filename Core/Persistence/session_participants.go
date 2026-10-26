package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Session"
)

const participantColumns = `session_id, principal_id, joined_at, last_seen_at`

func (s *store) BindSessionParticipant(ctx context.Context, sessionID session.ID, principalID string, seenAt time.Time) (*session.ParticipantBinding, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO interaction_session_participants (`+participantColumns+`)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id, principal_id) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		sessionID, principalID, formatTime(seenAt), formatTime(seenAt))
	if err != nil {
		var exists int
		if checkErr := s.db.QueryRowContext(ctx, `SELECT 1 FROM interaction_sessions WHERE session_id = ?`, sessionID).Scan(&exists); errors.Is(checkErr, sql.ErrNoRows) {
			return nil, session.ErrNotFound
		}
		return nil, fmt.Errorf("bind session participant: %w", err)
	}
	return scanParticipant(s.db.QueryRowContext(ctx, `SELECT `+participantColumns+` FROM interaction_session_participants
		WHERE session_id = ? AND principal_id = ?`, sessionID, principalID))
}

func (s *store) ListSessionParticipants(ctx context.Context, sessionID session.ID) ([]*session.ParticipantBinding, error) {
	return s.listParticipants(ctx, `SELECT `+participantColumns+` FROM interaction_session_participants
		WHERE session_id = ? ORDER BY joined_at, principal_id`, sessionID)
}

func (s *store) ListPrincipalSessions(ctx context.Context, principalID string) ([]*session.ParticipantBinding, error) {
	return s.listParticipants(ctx, `SELECT `+participantColumns+` FROM interaction_session_participants
		WHERE principal_id = ? ORDER BY joined_at, session_id`, principalID)
}

func (s *store) listParticipants(ctx context.Context, query string, argument any) ([]*session.ParticipantBinding, error) {
	rows, err := s.db.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*session.ParticipantBinding
	for rows.Next() {
		binding, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, binding)
	}
	return result, rows.Err()
}

func scanParticipant(row rowScanner) (*session.ParticipantBinding, error) {
	var binding session.ParticipantBinding
	var joinedAt, lastSeenAt string
	if err := row.Scan(&binding.SessionID, &binding.PrincipalID, &joinedAt, &lastSeenAt); err != nil {
		return nil, err
	}
	var err error
	binding.JoinedAt, err = time.Parse(time.RFC3339Nano, joinedAt)
	if err != nil {
		return nil, fmt.Errorf("decode participant joined_at: %w", err)
	}
	binding.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenAt)
	if err != nil {
		return nil, fmt.Errorf("decode participant last_seen_at: %w", err)
	}
	return &binding, nil
}
