package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	dn "github.com/Neon-Dolls/neondoll/DollNetwork"
)

// Compile-time assertion: *store satisfies invitation.Store.
var _ invitation.Store = (*store)(nil)

// Create persists a new invitation in the SQLite database.
// The invitation's secret hash and bootstrap endpoints are stored; the
// plaintext secret is never persisted.
func (s *store) Create(ctx context.Context, inv *invitation.Invitation) error {
	endJSON, err := json.Marshal(inv.BootstrapEndpoints)
	if err != nil {
		return fmt.Errorf("marshal invitation endpoints: %v", err)
	}

	hashBytes := inv.SecretHash[:] // [32]byte → []byte for SQL BLOB binding

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO invitations (id, secret_hash, expires_at, consumed, endpoints_json)
		 VALUES (?, ?, ?, ?, ?)`,
		inv.ID, hashBytes, inv.ExpiresAt.UTC().Format(time.RFC3339),
		boolToInt(inv.Consumed), string(endJSON),
	)
	if err != nil {
		return fmt.Errorf("persist invitation %q: %v", inv.ID, err)
	}
	return nil
}

// Get retrieves an invitation by ID. Returns nil, nil if not found.
func (s *store) Get(ctx context.Context, id string) (*invitation.Invitation, error) {
	var (
		hashBytes     []byte
		expiresStr    string
		consumed      int
		endpointsJSON string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT secret_hash, expires_at, consumed, endpoints_json
		 FROM invitations WHERE id = ?`, id,
	).Scan(&hashBytes, &expiresStr, &consumed, &endpointsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get invitation %q: %v", id, err)
	}

	if len(hashBytes) != 32 {
		return nil, fmt.Errorf("get invitation %q: corrupted hash (%d bytes)", id, len(hashBytes))
	}

	var endpoints dn.Endpoints
	if endpointsJSON != "" {
		if err := json.Unmarshal([]byte(endpointsJSON), &endpoints); err != nil {
			return nil, fmt.Errorf("unmarshal invitation %q endpoints: %v", id, err)
		}
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		return nil, fmt.Errorf("parse invitation %q expires_at %q: %v", id, expiresStr, err)
	}

	inv := &invitation.Invitation{
		ID:                 id,
		SecretHash:         bytesToHash(hashBytes),
		ExpiresAt:          expiresAt,
		Consumed:           consumed == 1,
		BootstrapEndpoints: endpoints,
	}
	return inv, nil
}

// Consume atomically marks an invitation as consumed. Returns true if the
// invitation was unconsumed and is now consumed. Returns an error if the
// invitation does not exist or was already consumed.
func (s *store) Consume(ctx context.Context, id string) (bool, error) {
	// First check existence.
	var rowID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM invitations WHERE id = ?`, id).Scan(&rowID)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("invitation %q not found", id)
	}
	if err != nil {
		return false, fmt.Errorf("check invitation %q: %v", id, err)
	}

	// Atomic consume: only marks consumed if it was unconsumed.
	res, err := s.db.ExecContext(ctx,
		`UPDATE invitations SET consumed = 1 WHERE id = ? AND consumed = 0`, id,
	)
	if err != nil {
		return false, fmt.Errorf("consume invitation %q: %v", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("consume invitation %q rows affected: %v", id, err)
	}
	if n == 0 {
		return false, fmt.Errorf("invitation %q already consumed", id)
	}
	return true, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// boolToInt converts a bool to 0/1 for SQL INTEGER columns.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// bytesToHash converts a []byte slice to a [32]byte SecretHash. The caller
// guarantees len(b) == 32.
func bytesToHash(b []byte) [32]byte {
	var h [32]byte
	copy(h[:], b[:32])
	return h
}
