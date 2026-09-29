package invitation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

// ── In-memory store ──────────────────────────────────────────────────────────

// MemoryStore is an in-memory invitation store protected by a mutex for
// concurrency-safe consumption.
type MemoryStore struct {
	mu  sync.Mutex
	inv map[string]*Invitation
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{inv: make(map[string]*Invitation)}
}

func (s *MemoryStore) Create(_ context.Context, inv *Invitation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.inv[inv.ID]; exists {
		return fmt.Errorf("invitation %q already exists", inv.ID)
	}
	clone := *inv
	s.inv[inv.ID] = &clone
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (*Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.inv[id]
	if !ok {
		return nil, fmt.Errorf("invitation %q not found", id)
	}
	clone := *inv
	return &clone, nil
}

// Consume atomically marks an invitation as consumed. At most one concurrent
// caller receives true; all others receive false (and error). This guarantees
// single-use semantics even under concurrent submission.
func (s *MemoryStore) Consume(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.inv[id]
	if !ok {
		return false, fmt.Errorf("invitation %q not found", id)
	}
	if inv.Consumed {
		return false, fmt.Errorf("invitation %q already consumed", id)
	}
	inv.Consumed = true
	return true, nil
}

// ── Secret generation ────────────────────────────────────────────────────────

// GenerateSecret creates a cryptographically strong random secret (32 bytes)
// encoded as unpadded base64url.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashSecret returns the SHA-256 hash of the secret.
func HashSecret(secret string) SecretHash {
	return sha256.Sum256([]byte(secret))
}

// GenerateID creates a cryptographically strong random invitation ID (32
// bytes) encoded as unpadded base64url.
func GenerateID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate invitation ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ── Service ──────────────────────────────────────────────────────────────────

// Service provides invitation creation and validation.
type Service struct {
	store Store
	clock Clock
}

// NewService creates an invitation Service.
func NewService(store Store, clock Clock) *Service {
	return &Service{store: store, clock: clock}
}

// CreateParams are the parameters for creating an invitation.
type CreateParams struct {
	Lifetime           time.Duration
	BootstrapEndpoints []string
}

// CreatedInvitation is the result of a successful Create call. It carries
// the invitation ID and the secret (which is returned only once and not
// stored in plaintext by the service). It also carries metadata the caller
// needs to build the out-of-band invitation document.
type CreatedInvitation struct {
	ID                 string
	Secret             string
	ExpiresAt          time.Time
	BootstrapEndpoints []string
}

// Create generates a new invitation with a cryptographically strong secret
// and stores it. Returns the invitation metadata; the secret is returned
// ONLY in this response and is never persisted in plaintext.
func (s *Service) Create(ctx context.Context, params CreateParams) (*CreatedInvitation, error) {
	id, err := GenerateID()
	if err != nil {
		return nil, err
	}
	secret, err := GenerateSecret()
	if err != nil {
		return nil, err
	}

	lifetime := params.Lifetime
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}

	hash := HashSecret(secret)
	now := s.clock.Now()

	inv := &Invitation{
		ID:                 id,
		SecretHash:         hash,
		ExpiresAt:          now.Add(lifetime),
		Consumed:           false,
		BootstrapEndpoints: params.BootstrapEndpoints,
	}

	if err := s.store.Create(ctx, inv); err != nil {
		return nil, err
	}

	return &CreatedInvitation{
		ID:                 id,
		Secret:             secret,
		ExpiresAt:          inv.ExpiresAt,
		BootstrapEndpoints: inv.BootstrapEndpoints,
	}, nil
}

// Validate checks whether an invitation with the given ID and secret is
// valid (exists, not expired, not consumed, secret matches). It does NOT
// consume the invitation — call Consume separately.
func (s *Service) Validate(ctx context.Context, id, secret string) (*Invitation, error) {
	inv, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("invalid invitation: %w", err)
	}

	if inv.Consumed {
		return nil, fmt.Errorf("invitation %q already consumed", id)
	}

	if s.clock.Now().After(inv.ExpiresAt) {
		return nil, fmt.Errorf("invitation %q expired at %s", id, inv.ExpiresAt.Format(time.RFC3339))
	}

	hash := HashSecret(secret)
	if hash != inv.SecretHash {
		return nil, fmt.Errorf("invitation %q: wrong secret", id)
	}

	return inv, nil
}

// Consume atomically marks an invitation as consumed. Returns an error if
// already consumed or not found.
func (s *Service) Consume(ctx context.Context, id string) error {
	ok, err := s.store.Consume(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("invitation %q already consumed", id)
	}
	return nil
}
