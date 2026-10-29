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

func (s *store) BindResolvedSessionParticipant(ctx context.Context, sessionID session.ID, source session.ParticipantSource, seenAt time.Time) (*session.ParticipantBinding, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var previous sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT principal_id FROM interaction_session_participant_sources
		WHERE session_id=? AND gateway_instance_id=? AND platform=? AND subject_id=?`, sessionID, source.GatewayInstanceID, source.Platform, source.SubjectID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO interaction_session_participants (`+participantColumns+`) VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id, principal_id) DO UPDATE SET last_seen_at=excluded.last_seen_at`, sessionID, source.PrincipalID, formatTime(seenAt), formatTime(seenAt))
	if err != nil {
		return nil, fmt.Errorf("bind resolved participant: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO interaction_session_participant_sources
		(session_id,gateway_instance_id,platform,subject_id,principal_id) VALUES (?,?,?,?,?)
		ON CONFLICT(session_id,gateway_instance_id,platform,subject_id) DO UPDATE SET principal_id=excluded.principal_id`,
		sessionID, source.GatewayInstanceID, source.Platform, source.SubjectID, source.PrincipalID)
	if err != nil {
		return nil, fmt.Errorf("bind participant source: %w", err)
	}
	if previous.Valid && previous.String != source.PrincipalID {
		if err = removeUnreferencedParticipant(ctx, tx, sessionID, previous.String); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return scanParticipant(s.db.QueryRowContext(ctx, `SELECT `+participantColumns+` FROM interaction_session_participants WHERE session_id=? AND principal_id=?`, sessionID, source.PrincipalID))
}

func (s *store) ReconcileSessionParticipantSources(ctx context.Context, configured []session.ParticipantSource, principals []string) error {
	allowed := make(map[string]string, len(configured))
	for _, source := range configured {
		allowed[source.GatewayInstanceID+"\x00"+source.Platform+"\x00"+source.SubjectID] = source.PrincipalID
	}
	known := make(map[string]struct{}, len(principals))
	for _, principal := range principals {
		known[principal] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT session_id,gateway_instance_id,platform,subject_id,principal_id FROM interaction_session_participant_sources`)
	if err != nil {
		return err
	}
	var stale []session.ParticipantSource
	for rows.Next() {
		var source session.ParticipantSource
		if err = rows.Scan(&source.SessionID, &source.GatewayInstanceID, &source.Platform, &source.SubjectID, &source.PrincipalID); err != nil {
			rows.Close()
			return err
		}
		if allowed[source.GatewayInstanceID+"\x00"+source.Platform+"\x00"+source.SubjectID] != source.PrincipalID {
			stale = append(stale, source)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, source := range stale {
		if _, err = tx.ExecContext(ctx, `DELETE FROM interaction_session_participant_sources WHERE session_id=? AND gateway_instance_id=? AND platform=? AND subject_id=?`, source.SessionID, source.GatewayInstanceID, source.Platform, source.SubjectID); err != nil {
			return err
		}
		if err = removeUnreferencedParticipant(ctx, tx, source.SessionID, source.PrincipalID); err != nil {
			return err
		}
	}
	participantRows, err := tx.QueryContext(ctx, `SELECT session_id,principal_id FROM interaction_session_participants`)
	if err != nil {
		return err
	}
	type pair struct {
		session   session.ID
		principal string
	}
	var unknown []pair
	for participantRows.Next() {
		var value pair
		if err = participantRows.Scan(&value.session, &value.principal); err != nil {
			participantRows.Close()
			return err
		}
		if _, ok := known[value.principal]; !ok {
			unknown = append(unknown, value)
		}
	}
	if err = participantRows.Close(); err != nil {
		return err
	}
	for _, value := range unknown {
		if _, err = tx.ExecContext(ctx, `DELETE FROM interaction_session_participants WHERE session_id=? AND principal_id=?`, value.session, value.principal); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func removeUnreferencedParticipant(ctx context.Context, tx *sql.Tx, sessionID session.ID, principal string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM interaction_session_participant_sources WHERE session_id=? AND principal_id=?`, sessionID, principal).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM interaction_session_participants WHERE session_id=? AND principal_id=?`, sessionID, principal)
		return err
	}
	return nil
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
