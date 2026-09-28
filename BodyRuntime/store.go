// Package bodyruntime — local persistent store for Body identity.
//
// M1 persistence semantics:
//   - a fresh installation creates the Body identity (and WG keypair) exactly
//     once;
//   - a restart loads the same Body/WG identity;
//   - missing or corrupt state fails safely — it is NEVER silently replaced.
//     Regeneration happens only under explicit fresh-state semantics
//     (ForceFresh), never implicitly on load.
//
// The store keeps the private WG key in a separate, owner-local file with
// restricted permissions, and never embeds it in any protocol-facing file.

package bodyruntime

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// stateFileName is the on-disk name of the durable identity file.
const stateFileName = "body.json"

// wgPrivateFileName is the on-disk name of the WG private key.
const wgPrivateFileName = "wg_private.key"

// Store persists Body identity on disk under a state directory.
type Store struct {
	dir string
}

// StateError describes a local store failure (missing, corrupt, io).
type StateError struct {
	Op  string
	Err error
}

// Error returns a short description of the state error.
func (e *StateError) Error() string {
	if e.Err != nil {
		return "bodyruntime: " + e.Op + ": " + e.Err.Error()
	}
	return "bodyruntime: " + e.Op
}

// ErrStateNotFound is returned when no identity exists yet (fresh install).
var ErrStateNotFound error = &StateError{Op: "not_found"}

// NewStore returns a store rooted at the given state directory.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the state directory this store is rooted at.
func (s *Store) Dir() string {
	return s.dir
}

// EnsureDir creates the state directory if missing.
func (s *Store) EnsureDir() error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return &StateError{Op: "mkdir", Err: err}
	}
	return nil
}

// statePath returns the path to the identity state file.
func (s *Store) statePath() string {
	return filepath.Join(s.dir, stateFileName)
}

// wgPrivatePath returns the path to the WG private key file.
func (s *Store) wgPrivatePath() string {
	return filepath.Join(s.dir, wgPrivateFileName)
}

// HasIdentity reports whether an identity file exists on disk.
func (s *Store) HasIdentity() bool {
	_, err := os.Stat(s.statePath())
	return err == nil
}

// LoadIdentity reads the persisted identity. If no identity exists yet it
// returns ErrStateNotFound; if the file is corrupt or the wrong version it
// returns a StateError. It never regenerates implicitly.
func (s *Store) LoadIdentity() (*IdentityState, error) {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, &StateError{Op: "read identity", Err: err}
	}
	st, err := UnmarshalIdentity(string(raw))
	if err != nil {
		return nil, &StateError{Op: "parse identity", Err: err}
	}
	return st, nil
}

// LoadWgKeypair reads the persisted WG private key and recomputes the public
// key. Returns ErrStateNotFound if the private key file is missing.
func (s *Store) LoadWgKeypair() (*WgKeypair, error) {
	raw, err := os.ReadFile(s.wgPrivatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, &StateError{Op: "read wg key", Err: err}
	}
	priv, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, &StateError{Op: "decode wg key", Err: err}
	}
	kp, err := NewWgKeypair(priv)
	if err != nil {
		return nil, &StateError{Op: "reconstruct wg key", Err: err}
	}
	Wipe(priv)
	return kp, nil
}

// SaveIdentity persists the identity state file. This is idempotent: if the
// identity already exists it is simply rewritten, not regenerated.
func (s *Store) SaveIdentity(st *IdentityState) error {
	if st == nil || st.Identity.BodyID == "" {
		return &StateError{Op: "save identity", Err: errors.New("empty identity")}
	}
	if err := s.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return &StateError{Op: "marshal identity", Err: err}
	}
	if err := os.WriteFile(s.statePath(), data, 0600); err != nil {
		return &StateError{Op: "write identity", Err: err}
	}
	return nil
}

// SaveWgKeypair persists the WG private key with owner-only permissions. The
// public key is recomputed and not stored separately.
func (s *Store) SaveWgKeypair(kp *WgKeypair) error {
	if kp == nil {
		return &StateError{Op: "save wg key", Err: errors.New("empty keypair")}
	}
	if err := s.EnsureDir(); err != nil {
		return err
	}
	priv := kp.PrivateKeyBytes()
	defer Wipe(priv)
	enc := base64.StdEncoding.EncodeToString(priv)
	if err := os.WriteFile(s.wgPrivatePath(), []byte(enc), 0600); err != nil {
		return &StateError{Op: "write wg key", Err: err}
	}
	return nil
}

// LoadOrError is a convenience for callers that want an explicit "fresh vs
// existing vs corrupt" decision. It returns the loaded identity keypair, or
// ErrStateNotFound when nothing exists yet.
func (s *Store) LoadOrError() (*IdentityState, *WgKeypair, error) {
	st, idErr := s.LoadIdentity()
	if idErr != nil {
		if idErr == ErrStateNotFound {
			return nil, nil, ErrStateNotFound
		}
		return nil, nil, idErr
	}
	kp, kpErr := s.LoadWgKeypair()
	if kpErr != nil {
		if kpErr == ErrStateNotFound {
			return nil, nil, &StateError{Op: "identity present but wg key missing", Err: nil}
		}
		return nil, nil, kpErr
	}
	return st, kp, nil
}

// FreshResult is the outcome of an explicit fresh-state creation.
type FreshResult struct {
	State *IdentityState
	Key   *WgKeypair
}

// CreateFresh forces a brand-new identity and WG keypair and persists them.
// This is the ONLY path that generates new identity material; it is invoked
// explicitly (e.g. an --init flag), never implicitly by Load.
func (s *Store) CreateFresh(name string, meta BodyMetadata) (*FreshResult, error) {
	st, err := NewIdentityState(name, meta)
	if err != nil {
		return nil, err
	}
	kp, err := GenerateWgKeypair()
	if err != nil {
		return nil, err
	}
	if err := s.SaveIdentity(st); err != nil {
		return nil, err
	}
	if err := s.SaveWgKeypair(kp); err != nil {
		return nil, err
	}
	return &FreshResult{State: st, Key: kp}, nil
}
