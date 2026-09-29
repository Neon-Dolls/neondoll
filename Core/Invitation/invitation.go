// Package invitation provides the invitation domain for bootstrapping
// a Body into the Doll Network.
package invitation

import (
	"context"
	"time"
)

// ── Clock: injectable time boundary ──────────────────────────────────────────

// Clock provides the current time. Injecting Clock (rather than calling
// time.Now() directly) makes expiry tests deterministic without sleeping.
type Clock interface {
	Now() time.Time
}

// RealClock returns the real system clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// ── Invitation types ─────────────────────────────────────────────────────────

// SecretHash is the SHA-256 hash of the invitation secret, stored instead
// of the plaintext secret so the secret is never persisted.
type SecretHash [32]byte

// Invitation represents a single-use pairing invitation that expires.
type Invitation struct {
	ID                 string     `json:"id"`
	SecretHash         SecretHash `json:"-"`
	ExpiresAt          time.Time  `json:"expires_at"`
	Consumed           bool       `json:"consumed"`
	BootstrapEndpoints []string   `json:"bootstrap_endpoints,omitempty"`
}

// ── Store: persistence abstraction ───────────────────────────────────────────

// Store provides durable (or in-process) storage for invitations.
type Store interface {
	// Create persists a new invitation.
	Create(ctx context.Context, inv *Invitation) error
	// Get retrieves an invitation by ID.
	Get(ctx context.Context, id string) (*Invitation, error)
	// Consume marks an invitation as consumed, returning true if the
	// invitation was previously unconsumed. It is safe for concurrent use:
	// at most one concurrent caller will receive true.
	Consume(ctx context.Context, id string) (bool, error)
}

// ── Default lifetime ─────────────────────────────────────────────────────────

// DefaultLifetime is the default duration before an invitation expires.
const DefaultLifetime = 30 * time.Minute
