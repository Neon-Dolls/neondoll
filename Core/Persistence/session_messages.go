package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Session"
)

var _ contextbuilder.HistoryStore = (*store)(nil)

func (s *store) AppendSessionMessages(ctx context.Context, sessionID session.ID, messages []contextbuilder.NewHistoryMessage) ([]contextbuilder.HistoryMessage, error) {
	if sessionID == "" {
		return nil, contextbuilder.ErrInvalidRequest
	}
	if len(messages) == 0 {
		if _, err := s.LoadSession(ctx, sessionID); err != nil {
			return nil, err
		}
		return []contextbuilder.HistoryMessage{}, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("append session messages: acquire connection: %w", err)
	}
	defer conn.Close()
	// BEGIN IMMEDIATE acquires SQLite's write reservation before reading the
	// current sequence. Concurrent appenders therefore cannot calculate the
	// same starting sequence. busy_timeout lets the existing SQLite locking
	// mechanism serialize writers without an application-level retry loop.
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return nil, fmt.Errorf("append session messages: configure writer: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, fmt.Errorf("append session messages: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT 1 FROM interaction_sessions WHERE session_id = ?`, sessionID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return nil, session.ErrNotFound
		}
		return nil, fmt.Errorf("append session messages: resolve session: %w", err)
	}
	var next int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence) + 1, 0) FROM interaction_session_messages WHERE session_id = ?`, sessionID).Scan(&next); err != nil {
		return nil, fmt.Errorf("append session messages: sequence: %w", err)
	}
	created := make([]contextbuilder.HistoryMessage, len(messages))
	for index, message := range messages {
		if message.Role != contextbuilder.RoleSystem && message.Role != contextbuilder.RoleUser && message.Role != contextbuilder.RoleAssistant && message.Role != contextbuilder.RoleTool {
			return nil, contextbuilder.ErrInvalidRequest
		}
		created[index] = contextbuilder.HistoryMessage{ID: uuid.NewString(), SessionID: sessionID, Sequence: next + index, Role: message.Role, Content: message.Content, CreatedAt: time.Now().UTC()}
		if _, err := conn.ExecContext(ctx, `INSERT INTO interaction_session_messages (session_id, sequence, message_id, role, content, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, created[index].Sequence, created[index].ID, created[index].Role, created[index].Content, formatTime(created[index].CreatedAt)); err != nil {
			return nil, fmt.Errorf("append session messages: insert: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, fmt.Errorf("append session messages: commit: %w", err)
	}
	committed = true
	return created, nil
}

func (s *store) ListSessionMessages(ctx context.Context, sessionID session.ID) ([]contextbuilder.HistoryMessage, error) {
	if sessionID == "" {
		return nil, contextbuilder.ErrInvalidRequest
	}
	if _, err := s.LoadSession(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, sequence, role, content, created_at FROM interaction_session_messages WHERE session_id = ? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session messages: %w", err)
	}
	defer rows.Close()
	var result []contextbuilder.HistoryMessage
	for rows.Next() {
		message := contextbuilder.HistoryMessage{SessionID: sessionID}
		var createdAt string
		if err := rows.Scan(&message.ID, &message.Sequence, &message.Role, &message.Content, &createdAt); err != nil {
			return nil, fmt.Errorf("list session messages: scan: %w", err)
		}
		message.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("list session messages: created_at: %w", err)
		}
		result = append(result, message)
	}
	return result, rows.Err()
}
