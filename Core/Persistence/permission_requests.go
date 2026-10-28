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
	arguments_json, status, created_at, expires_at, resolved_at, resolving_authority, correlation_json,
	continuation_status, continuation_result_json, continuation_updated_at`

func (s *store) CreatePermissionRequest(ctx context.Context, value *permissionrequest.Request) error {
	correlation, err := json.Marshal(value.Correlation)
	if err != nil {
		return fmt.Errorf("encode permission request correlation: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO permission_requests (`+permissionRequestColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, NULL)`, value.ID, value.DollID, value.OwnerID,
		value.Origin, value.Action, value.Question, value.Capability, value.Operation, string(value.Arguments),
		value.Status, formatTime(value.CreatedAt), formatTime(value.ExpiresAt), value.ResolvingAuthority, string(correlation),
		value.ContinuationStatus, string(value.ContinuationResult))
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
		SET status = ?, resolved_at = ?, resolving_authority = ?,
			continuation_status = CASE
				WHEN ? = ? AND continuation_status = ? THEN ?
				WHEN ? = ? AND continuation_status = ? THEN ?
				ELSE continuation_status END,
			continuation_updated_at = CASE WHEN continuation_status = ? THEN continuation_updated_at ELSE ? END
		WHERE request_id = ? AND status = ?`, status, formatTime(at), authority,
		status, permissionrequest.StatusApproved, permissionrequest.ContinuationWaiting, permissionrequest.ContinuationReady,
		status, permissionrequest.StatusDenied, permissionrequest.ContinuationWaiting, permissionrequest.ContinuationDenied,
		permissionrequest.ContinuationNone, formatTime(at), id, permissionrequest.StatusPending)
	if err != nil {
		return false, fmt.Errorf("transition permission request: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *store) ListReadyPermissionContinuations(ctx context.Context) ([]*permissionrequest.Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+permissionRequestColumns+` FROM permission_requests
		WHERE (status = ? AND continuation_status = ?) OR (status = ? AND continuation_status = ?)
		ORDER BY resolved_at, request_id`, permissionrequest.StatusApproved, permissionrequest.ContinuationReady,
		permissionrequest.StatusDenied, permissionrequest.ContinuationDenied)
	if err != nil {
		return nil, fmt.Errorf("list ready permission continuations: %w", err)
	}
	defer rows.Close()
	var result []*permissionrequest.Request
	for rows.Next() {
		value, scanErr := scanPermissionRequest(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *store) ClaimPermissionContinuation(ctx context.Context, id permissionrequest.ID, at time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE permission_requests SET continuation_status = ?, continuation_updated_at = ?
		WHERE request_id = ? AND ((status = ? AND continuation_status = ?) OR (status = ? AND continuation_status = ?))`, permissionrequest.ContinuationRunning,
		formatTime(at), id, permissionrequest.StatusApproved, permissionrequest.ContinuationReady,
		permissionrequest.StatusDenied, permissionrequest.ContinuationDenied)
	if err != nil {
		return false, fmt.Errorf("claim permission continuation: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *store) CompletePermissionContinuation(ctx context.Context, id permissionrequest.ID, status permissionrequest.ContinuationStatus, resultJSON json.RawMessage, at time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE permission_requests
		SET continuation_status = ?, continuation_result_json = ?, continuation_updated_at = ?
		WHERE request_id = ? AND continuation_status = ?`, status, string(resultJSON), formatTime(at), id, permissionrequest.ContinuationRunning)
	if err != nil {
		return false, fmt.Errorf("complete permission continuation: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func scanPermissionRequest(row rowScanner) (*permissionrequest.Request, error) {
	var value permissionrequest.Request
	var arguments, createdAt, expiresAt, correlation, continuationResult string
	var resolvedAt, continuationUpdatedAt sql.NullString
	err := row.Scan(&value.ID, &value.DollID, &value.OwnerID, &value.Origin, &value.Action, &value.Question,
		&value.Capability, &value.Operation, &arguments, &value.Status, &createdAt, &expiresAt, &resolvedAt,
		&value.ResolvingAuthority, &correlation, &value.ContinuationStatus, &continuationResult, &continuationUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, permissionrequest.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	value.Arguments = json.RawMessage(arguments)
	value.ContinuationResult = json.RawMessage(continuationResult)
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
	if continuationUpdatedAt.Valid {
		updated, parseErr := time.Parse(time.RFC3339Nano, continuationUpdatedAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("decode permission continuation_updated_at: %w", parseErr)
		}
		value.ContinuationUpdatedAt = &updated
	}
	return &value, nil
}
