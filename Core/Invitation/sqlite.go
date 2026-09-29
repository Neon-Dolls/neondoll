package invitation

import (
	"context"
	"fmt"
)

// ── SQLite persistent store — stub ──────────────────────────────────────────

// SQLiteStore is a persistent invitation store backed by SQLite. It implements
// the store interface used by invitation.Service but is currently a stub.
// TODO: implement actual SQLite persistence.
type SQLiteStore struct{}

// NewSQLiteStore creates a SQLiteStore. It is a stub and does not actually
// connect to SQLite.
func NewSQLiteStore() *SQLiteStore {
	return &SQLiteStore{}
}

func (s *SQLiteStore) Create(_ context.Context, _ *Invitation) error {
	return fmt.Errorf("SQLiteStore.Create: not implemented")
}

func (s *SQLiteStore) Get(_ context.Context, _ string) (*Invitation, error) {
	return nil, fmt.Errorf("SQLiteStore.Get: not implemented")
}

func (s *SQLiteStore) Consume(_ context.Context, _ string) (bool, error) {
	return false, fmt.Errorf("SQLiteStore.Consume: not implemented")
}