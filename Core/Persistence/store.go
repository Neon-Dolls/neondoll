package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll/DollState"

	_ "modernc.org/sqlite"
)

// Common errors returned by Store operations.
var (
	ErrDollNotFound    = errors.New("persistence: doll not found")
	ErrInvalidDollID   = errors.New("persistence: invalid doll ID")
	ErrCannotOpen      = errors.New("persistence: cannot open database")
	ErrCannotSave      = errors.New("persistence: cannot save doll")
	ErrCannotDecode    = errors.New("persistence: cannot decode stored state")
)

// Store is the public interface for Doll persistence.
type Store interface {
	// SaveDoll stores or replaces a Doll's state keyed by its DollID.
	SaveDoll(ctx context.Context, state *dollstate.DollState) error

	// LoadDoll retrieves a Doll's state by its stable DollID.
	LoadDoll(ctx context.Context, dollID string) (*dollstate.DollState, error)

	// Close releases the underlying database connection.
	Close() error
}

// store is the SQLite implementation of Store.
type store struct {
	db *sql.DB
}

// NewStore opens (or creates) a SQLite database at dbPath and returns a Store.
func NewStore(dbPath string) (Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCannotOpen, dbPath, err)
	}

	// Enable WAL mode and foreign keys for robustness.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%w: pragma %q: %v", ErrCannotOpen, p, err)
		}
	}

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: schema: %v", ErrCannotOpen, err)
	}

	return &store{db: db}, nil
}

// createSchema ensures the dolls and memories tables exist.
func createSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS dolls (
		doll_id      TEXT PRIMARY KEY,
		version      INTEGER NOT NULL,
		identity_json TEXT NOT NULL,
		soul_json     TEXT NOT NULL,
		owner_json    TEXT NOT NULL,
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE TABLE IF NOT EXISTS memories (
		doll_id        TEXT NOT NULL,
		seq            INTEGER NOT NULL,
		mem_id         TEXT NOT NULL,
		interaction_id TEXT NOT NULL DEFAULT '',
		kind           TEXT NOT NULL,
		content        TEXT NOT NULL,
		timestamp      TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (doll_id, seq),
		FOREIGN KEY (doll_id) REFERENCES dolls(doll_id)
	);`
	_, err := db.Exec(schema)
	return err
}

// SaveDoll inserts or replaces a Doll's state, keyed by its stable DollID.
// The doll row and any memory changes are applied atomically in a single
// SQLite transaction. If Memories.Items is nil, existing memories are
// preserved untouched; if non-nil (including empty slice), they are replaced.
func (s *store) SaveDoll(ctx context.Context, state *dollstate.DollState) error {
	if state == nil || state.Identity.DollID == "" {
		return ErrInvalidDollID
	}

	identityJSON, err := json.Marshal(state.Identity)
	if err != nil {
		return fmt.Errorf("%w: identity: %v", ErrCannotSave, err)
	}
	soulJSON, err := json.Marshal(state.Soul)
	if err != nil {
		return fmt.Errorf("%w: soul: %v", ErrCannotSave, err)
	}
	ownerJSON, err := json.Marshal(state.Owner)
	if err != nil {
		return fmt.Errorf("%w: owner: %v", ErrCannotSave, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin tx: %v", ErrCannotSave, err)
	}
	defer tx.Rollback() // no-op if Commit succeeds

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO dolls (doll_id, version, identity_json, soul_json, owner_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(doll_id) DO UPDATE SET
			version=excluded.version,
			identity_json=excluded.identity_json,
			soul_json=excluded.soul_json,
			owner_json=excluded.owner_json,
			updated_at=excluded.updated_at`,
		state.Identity.DollID, state.Version,
		string(identityJSON), string(soulJSON), string(ownerJSON),
		now, now,
	)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCannotSave, err)
	}

	// Persist MemoryItems: nil = leave untouched, non-nil = replace.
	if state.Memories.Items != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE doll_id = ?`, state.Identity.DollID); err != nil {
			return fmt.Errorf("%w: delete memories: %v", ErrCannotSave, err)
		}
		if len(state.Memories.Items) > 0 {
			for i := range state.Memories.Items {
				m := &state.Memories.Items[i]
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO memories (doll_id, seq, mem_id, interaction_id, kind, content, timestamp)
					 VALUES (?, ?, ?, ?, ?, ?, ?)`,
					state.Identity.DollID, m.Sequence, m.ID, m.InteractionID, m.Kind, m.Content, m.Timestamp,
				); err != nil {
					return fmt.Errorf("%w: insert memory %q: %v", ErrCannotSave, m.ID, err)
				}
			}
		}
	}

	return tx.Commit()
}

// LoadDoll retrieves a Doll's state by its stable DollID.
func (s *store) LoadDoll(ctx context.Context, dollID string) (*dollstate.DollState, error) {
	if dollID == "" {
		return nil, ErrInvalidDollID
	}

	var (
		version       int
		identityJSON, soulJSON, ownerJSON string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT version, identity_json, soul_json, owner_json
		 FROM dolls WHERE doll_id = ?`, dollID,
	).Scan(&version, &identityJSON, &soulJSON, &ownerJSON)
	if err == sql.ErrNoRows {
		return nil, ErrDollNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load %q: %v", ErrCannotDecode, dollID, err)
	}

	state := dollstate.NewDollState()
	state.Version = version

	if err := json.Unmarshal([]byte(identityJSON), &state.Identity); err != nil {
		return nil, fmt.Errorf("%w: identity: %v", ErrCannotDecode, err)
	}
	if err := json.Unmarshal([]byte(soulJSON), &state.Soul); err != nil {
		return nil, fmt.Errorf("%w: soul: %v", ErrCannotDecode, err)
	}
	if err := json.Unmarshal([]byte(ownerJSON), &state.Owner); err != nil {
		return nil, fmt.Errorf("%w: owner: %v", ErrCannotDecode, err)
	}

	// Load memories ordered by sequence.
	mrows, err := s.db.QueryContext(ctx,
		`SELECT seq, mem_id, interaction_id, kind, content, timestamp
		 FROM memories WHERE doll_id = ? ORDER BY seq`, dollID)
	if err != nil {
		return nil, fmt.Errorf("%w: query memories: %v", ErrCannotDecode, err)
	}
	defer mrows.Close()

	var items []dollstate.MemoryItem
	for mrows.Next() {
		var m dollstate.MemoryItem
		if err := mrows.Scan(&m.Sequence, &m.ID, &m.InteractionID, &m.Kind, &m.Content, &m.Timestamp); err != nil {
			return nil, fmt.Errorf("%w: scan memory: %v", ErrCannotDecode, err)
		}
		items = append(items, m)
	}
	if err := mrows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate memories: %v", ErrCannotDecode, err)
	}
	if items != nil {
		state.Memories.Items = items
	}

	return &state, nil
}

// Close releases the database connection.
func (s *store) Close() error {
	return s.db.Close()
}
