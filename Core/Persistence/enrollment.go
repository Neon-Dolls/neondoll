package persistence

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

var _ enrollment.Store = (*store)(nil)

func (s *store) LoadCoreIdentity(ctx context.Context) (*enrollment.CoreIdentity, error) {
	var coreID string
	var publicKey, privateKey []byte
	err := s.db.QueryRowContext(ctx, `SELECT core_id, public_key, private_key FROM enrollment_core_identity WHERE singleton = 1`).Scan(&coreID, &publicKey, &privateKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load enrollment Core identity: %w", err)
	}
	if len(publicKey) != ed25519.PublicKeySize || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("load enrollment Core identity: invalid key length")
	}
	if !ed25519.PublicKey(privateKey[32:]).Equal(ed25519.PublicKey(publicKey)) {
		return nil, errors.New("load enrollment Core identity: keypair mismatch")
	}
	return &enrollment.CoreIdentity{CoreID: coreID, PublicKey: publicKey, PrivateKey: privateKey}, nil
}

func (s *store) SaveCoreIdentity(ctx context.Context, identity *enrollment.CoreIdentity) error {
	if identity == nil || identity.CoreID == "" || len(identity.PublicKey) != ed25519.PublicKeySize || len(identity.PrivateKey) != ed25519.PrivateKeySize {
		return errors.New("save enrollment Core identity: invalid identity")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO enrollment_core_identity(singleton, core_id, public_key, private_key) VALUES(1, ?, ?, ?)`, identity.CoreID, []byte(identity.PublicKey), []byte(identity.PrivateKey))
	if err != nil {
		return fmt.Errorf("save enrollment Core identity: %w", err)
	}
	return nil
}

func (s *store) CreatePending(ctx context.Context, pending *enrollment.PendingEnrollment) error {
	metadata, _ := json.Marshal(pending.Metadata)
	permissions, _ := json.Marshal(pending.Permissions)
	var verifier any
	if len(pending.CodeVerifier) > 0 {
		verifier = pending.CodeVerifier
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO pending_enrollments
		(request_id, mode, core_id, body_public_key, wg_public_key, body_nonce, code_verifier, invitation_id, metadata_json, status, attempt_count, approved_owner_id, permissions_json, created_at, expires_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pending.RequestID, pending.Mode, pending.CoreID, []byte(pending.BodyPublicKey), pending.WGPublicKey[:], pending.BodyNonce,
		verifier, nullIfEmpty(pending.InvitationID), string(metadata), pending.Status, pending.AttemptCount, pending.ApprovedOwnerID,
		string(permissions), formatTime(pending.CreatedAt), formatTime(pending.ExpiresAt))
	if err != nil {
		return fmt.Errorf("create pending enrollment: %w", err)
	}
	return nil
}

func (s *store) GetPending(ctx context.Context, requestID string) (*enrollment.PendingEnrollment, error) {
	return scanPending(s.db.QueryRowContext(ctx, pendingSelect+` WHERE request_id = ?`, requestID))
}

func (s *store) GetPendingByCodeVerifier(ctx context.Context, verifier []byte) (*enrollment.PendingEnrollment, error) {
	return scanPending(s.db.QueryRowContext(ctx, pendingSelect+` WHERE code_verifier = ?`, verifier))
}

func (s *store) ListPending(ctx context.Context) ([]*enrollment.PendingEnrollment, error) {
	rows, err := s.db.QueryContext(ctx, pendingSelect+` ORDER BY created_at, request_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*enrollment.PendingEnrollment
	for rows.Next() {
		pending, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, pending)
	}
	return result, rows.Err()
}

const pendingSelect = `SELECT request_id, mode, core_id, body_public_key, wg_public_key, body_nonce,
	code_verifier, invitation_id, metadata_json, status, attempt_count, approved_owner_id,
	permissions_json, created_at, expires_at FROM pending_enrollments`

type rowScanner interface{ Scan(...any) error }

func scanPending(row rowScanner) (*enrollment.PendingEnrollment, error) {
	var pending enrollment.PendingEnrollment
	var bodyKey, wgKey []byte
	var verifier, invitationID sql.NullString
	var metadataJSON, permissionsJSON, created, expires string
	err := row.Scan(&pending.RequestID, &pending.Mode, &pending.CoreID, &bodyKey, &wgKey, &pending.BodyNonce,
		&verifier, &invitationID, &metadataJSON, &pending.Status, &pending.AttemptCount, &pending.ApprovedOwnerID,
		&permissionsJSON, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bodyKey) != ed25519.PublicKeySize || len(wgKey) != 32 {
		return nil, errors.New("pending enrollment has invalid key length")
	}
	pending.BodyPublicKey = append(ed25519.PublicKey(nil), bodyKey...)
	copy(pending.WGPublicKey[:], wgKey)
	if verifier.Valid {
		pending.CodeVerifier = []byte(verifier.String)
	}
	if invitationID.Valid {
		pending.InvitationID = invitationID.String
	}
	if err := json.Unmarshal([]byte(metadataJSON), &pending.Metadata); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(permissionsJSON), &pending.Permissions); err != nil {
		return nil, err
	}
	var errTime error
	pending.CreatedAt, errTime = time.Parse(time.RFC3339Nano, created)
	if errTime != nil {
		return nil, errTime
	}
	pending.ExpiresAt, errTime = time.Parse(time.RFC3339Nano, expires)
	return &pending, errTime
}

func (s *store) CountPending(ctx context.Context, now time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_enrollments WHERE status = ? AND expires_at > ?`, enrollment.StatusPending, formatTime(now)).Scan(&count)
	return count, err
}

func (s *store) IncrementAttempts(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pending_enrollments SET attempt_count = attempt_count + 1 WHERE request_id = ?`, requestID)
	return err
}

func (s *store) ConsumeResultNonce(ctx context.Context, requestID string, nonce []byte) (bool, error) {
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO enrollment_result_nonces(request_id, nonce) VALUES(?, ?)`, requestID, nonce)
	if err != nil {
		return false, fmt.Errorf("consume enrollment result nonce: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("consume enrollment result nonce rows: %w", err)
	}
	return rows == 1, nil
}

func (s *store) Deny(ctx context.Context, requestID, ownerID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE pending_enrollments SET status = ?, approved_owner_id = ? WHERE request_id = ? AND status = ?`, enrollment.StatusDenied, ownerID, requestID, enrollment.StatusPending)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return enrollment.ErrReplayed
	}
	return nil
}

func (s *store) Complete(ctx context.Context, completion *enrollment.Completion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if completion.Pending.InvitationID != "" {
		result, err := tx.ExecContext(ctx, `UPDATE invitations SET consumed = 1 WHERE id = ? AND consumed = 0`, completion.Pending.InvitationID)
		if err != nil {
			return err
		}
		rows, _ := result.RowsAffected()
		if rows != 1 {
			return enrollment.ErrReplayed
		}
	}
	permissions, _ := json.Marshal(completion.Body.Permissions)
	_, err = tx.ExecContext(ctx, `INSERT INTO body_credentials(body_id, public_key, key_version, owner_id, permissions_json, membership_reference, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, completion.Body.BodyID, []byte(completion.Body.PublicKey), completion.Body.KeyVersion,
		completion.Body.OwnerID, string(permissions), completion.Body.MembershipRef, formatTime(completion.Body.CreatedAt), formatTime(completion.Body.UpdatedAt))
	if err != nil {
		return err
	}
	membershipJSON, err := json.Marshal(membershipToRow(completion.Membership))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO network_state(key, value, updated_at) VALUES(?, ?, datetime('now'))`, membershipKey(completion.Membership.PeerID), string(membershipJSON))
	if err != nil {
		return err
	}
	receiptJSON, _ := json.Marshal(completion.Receipt)
	_, err = tx.ExecContext(ctx, `INSERT INTO enrollment_receipts(request_id, receipt_json) VALUES(?, ?)`, completion.Pending.RequestID, string(receiptJSON))
	if err != nil {
		return err
	}
	pendingPermissions, _ := json.Marshal(completion.Pending.Permissions)
	result, err := tx.ExecContext(ctx, `UPDATE pending_enrollments SET status = ?, approved_owner_id = ?, permissions_json = ? WHERE request_id = ? AND status = ?`,
		enrollment.StatusEnrolled, completion.Pending.ApprovedOwnerID, string(pendingPermissions), completion.Pending.RequestID, enrollment.StatusPending)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return enrollment.ErrReplayed
	}
	return tx.Commit()
}

func (s *store) GetReceipt(ctx context.Context, requestID string) (*dollnetwork.EnrollmentReceipt, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT receipt_json FROM enrollment_receipts WHERE request_id = ?`, requestID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt dollnetwork.EnrollmentReceipt
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (s *store) GetBody(ctx context.Context, bodyID string) (*enrollment.BodyRecord, error) {
	return scanBody(s.db.QueryRowContext(ctx, bodySelect+` WHERE body_id = ?`, bodyID))
}

const bodySelect = `SELECT body_id, public_key, key_version, owner_id, permissions_json, membership_reference, created_at, updated_at, revoked_at FROM body_credentials`

func (s *store) ListBodies(ctx context.Context) ([]*enrollment.BodyRecord, error) {
	rows, err := s.db.QueryContext(ctx, bodySelect+` ORDER BY body_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*enrollment.BodyRecord
	for rows.Next() {
		body, err := scanBody(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, body)
	}
	return result, rows.Err()
}

func scanBody(row rowScanner) (*enrollment.BodyRecord, error) {
	var body enrollment.BodyRecord
	var publicKey []byte
	var permissions, created, updated string
	var revoked sql.NullString
	err := row.Scan(&body.BodyID, &publicKey, &body.KeyVersion, &body.OwnerID, &permissions, &body.MembershipRef, &created, &updated, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	body.PublicKey = publicKey
	_ = json.Unmarshal([]byte(permissions), &body.Permissions)
	body.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	body.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if revoked.Valid {
		value, err := time.Parse(time.RFC3339Nano, revoked.String)
		if err != nil {
			return nil, err
		}
		body.RevokedAt = &value
	}
	return &body, nil
}

func (s *store) RevokeBody(ctx context.Context, bodyID string, membership *network.Membership, revokedAt time.Time) error {
	if membership == nil || membership.BodyID != bodyID || membership.Status != network.MembershipRevoked {
		return errors.New("revoke Body: invalid revoked membership")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE body_credentials SET revoked_at = ?, updated_at = ? WHERE body_id = ? AND revoked_at IS NULL`, formatTime(revokedAt), formatTime(revokedAt), bodyID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return enrollment.ErrUnknownBody
	}
	membershipJSON, err := json.Marshal(membershipToRow(membership))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO network_state(key, value, updated_at) VALUES(?, ?, datetime('now'))`, membershipKey(membership.PeerID), string(membershipJSON)); err != nil {
		return err
	}
	return tx.Commit()
}

func membershipToRow(m *network.Membership) membershipRow {
	row := membershipRow{BodyID: m.BodyID, PeerID: m.PeerID, OverlayAddr: m.OverlayAddress.String(), Status: m.Status}
	if m.WireGuardPublicKey != nil {
		row.WireGuardPublicKey = m.WireGuardPublicKey[:]
	}
	return row
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
