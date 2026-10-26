package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
)

var _ permissionrequest.Store = (*store)(nil)

const permissionRequestColumns = `request_id, doll_id, owner_id, origin, action, question, capability, operation,
	arguments_json, status, created_at, expires_at, resolved_at, resolving_authority, correlation_json`

func (s *store) CreatePermissionRequest(ctx context.Context, value *permissionrequest.Request) error {
	correlation, err := json.Marshal(value.Correlation)
	if err != nil {
		return fmt.Errorf("encode permission request correlation: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO permission_requests (`+permissionRequestColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`, value.ID, value.DollID, value.OwnerID,
		value.Origin, value.Action, value.Question, value.Capability, value.Operation, string(value.Arguments),
		value.Status, formatTime(value.CreatedAt), formatTime(value.ExpiresAt), value.ResolvingAuthority, string(correlation))
	if err != nil {
		return fmt.Errorf("create permission request: %w", err)
	}
	return nil
}

func (s *store) LoadPermissionRequest(ctx context.Context, id permissionrequest.ID) (*permissionrequest.Request, error) {
	return scanPermissionRequest(s.db.QueryRowContext(ctx, `SELECT `+permissionRequestColumns+` FROM permission_requests WHERE request_id = ?`, id))
}

func (s *store) ListPendingPermissionRequests(ctx context.Context) ([]*permissionrequest.Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+permissionRequestColumns+` FROM permission_requests
		WHERE status = ? ORDER BY created_at, request_id`, permissionrequest.StatusPending)
	if err != nil {
		return nil, fmt.Errorf("list pending permission requests: %w", err)
	}
	defer rows.Close()
	var result []*permissionrequest.Request
	for rows.Next() {
		value, err := scanPermissionRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *store) TransitionPermissionRequest(ctx context.Context, id permissionrequest.ID, status permissionrequest.Status, at time.Time, authority string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE permission_requests
		SET status = ?, resolved_at = ?, resolving_authority = ?
		WHERE request_id = ? AND status = ?`, status, formatTime(at), authority, id, permissionrequest.StatusPending)
	if err != nil {
		return false, fmt.Errorf("transition permission request: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func scanPermissionRequest(row rowScanner) (*permissionrequest.Request, error) {
	var value permissionrequest.Request
	var arguments, createdAt, expiresAt, correlation string
	var resolvedAt sql.NullString
	err := row.Scan(&value.ID, &value.DollID, &value.OwnerID, &value.Origin, &value.Action, &value.Question,
		&value.Capability, &value.Operation, &arguments, &value.Status, &createdAt, &expiresAt, &resolvedAt,
		&value.ResolvingAuthority, &correlation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, permissionrequest.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	value.Arguments = json.RawMessage(arguments)
	if err := json.Unmarshal([]byte(correlation), &value.Correlation); err != nil {
		return nil, fmt.Errorf("decode permission request correlation: %w", err)
	}
	value.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("decode permission request created_at: %w", err)
	}
	value.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("decode permission request expires_at: %w", err)
	}
	if resolvedAt.Valid {
		resolved, parseErr := time.Parse(time.RFC3339Nano, resolvedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("decode permission request resolved_at: %w", parseErr)
		}
		value.ResolvedAt = &resolved
	}
	return &value, nil
}
