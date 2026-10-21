// Package enrollment owns Core-side Body enrollment authority.
package enrollment

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

type Mode string
type Status string

const (
	ModeInvitation  Mode = "invitation"
	ModeBodyRequest Mode = "body_request"

	StatusPending  Status = "pending"
	StatusDenied   Status = "denied"
	StatusExpired  Status = "expired"
	StatusEnrolled Status = "enrolled"
)

var (
	ErrUnauthorized       = errors.New("enrollment: unauthorized")
	ErrUnknownRequest     = errors.New("enrollment: unknown request")
	ErrExpired            = errors.New("enrollment: expired")
	ErrDenied             = errors.New("enrollment: denied")
	ErrReplayed           = errors.New("enrollment: replayed")
	ErrRateLimited        = errors.New("enrollment: rate limited")
	ErrRevoked            = errors.New("enrollment: revoked")
	ErrInvalidProof       = errors.New("enrollment: invalid proof")
	ErrCoreIdentity       = errors.New("enrollment: core identity mismatch")
	ErrUnsupportedVersion = errors.New("enrollment: unsupported version")
	ErrUnknownBody        = errors.New("enrollment: unknown Body")
)

type CoreIdentity struct {
	CoreID     string
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

type PendingEnrollment struct {
	RequestID       string
	Mode            Mode
	CoreID          string
	BodyPublicKey   ed25519.PublicKey
	WGPublicKey     network.WireGuardPublicKey
	BodyNonce       []byte
	CodeVerifier    []byte
	InvitationID    string
	Metadata        dollnetwork.EnrollmentMetadata
	Status          Status
	AttemptCount    int
	ApprovedOwnerID string
	Permissions     []string
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

type BodyRecord struct {
	BodyID        string
	PublicKey     ed25519.PublicKey
	KeyVersion    int
	OwnerID       string
	Permissions   []string
	MembershipRef string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RevokedAt     *time.Time
	Status        string
}

type Completion struct {
	Pending    *PendingEnrollment
	Body       *BodyRecord
	Membership *network.Membership
	Receipt    *dollnetwork.EnrollmentReceipt
}

// Store commits a completed enrollment, its existing Doll Network membership
// record, and invitation consumption in one transaction.
type Store interface {
	LoadCoreIdentity(context.Context) (*CoreIdentity, error)
	SaveCoreIdentity(context.Context, *CoreIdentity) error
	CreatePending(context.Context, *PendingEnrollment) error
	GetPending(context.Context, string) (*PendingEnrollment, error)
	GetPendingByCodeVerifier(context.Context, []byte) (*PendingEnrollment, error)
	ListPending(context.Context) ([]*PendingEnrollment, error)
	CountPending(context.Context, time.Time) (int, error)
	IncrementAttempts(context.Context, string) error
	ConsumeResultNonce(context.Context, string, []byte) (bool, error)
	Deny(context.Context, string, string) error
	Complete(context.Context, *Completion) error
	GetReceipt(context.Context, string) (*dollnetwork.EnrollmentReceipt, error)
	GetBody(context.Context, string) (*BodyRecord, error)
	ListBodies(context.Context) ([]*BodyRecord, error)
	RevokeBody(context.Context, string, *network.Membership, time.Time) error
}

type SessionRevoker interface{ DisconnectBody(string) }
type PeerRefresher interface{ RefreshPeers(context.Context) error }

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type OwnerAction struct {
	Operation string
	RequestID string
}

// OwnerAuthorizer authenticates and authorizes the owner independently of an
// enrollment code. A code is never sufficient authority to approve or deny.
type OwnerAuthorizer interface {
	AuthorizeEnrollment(context.Context, string, OwnerAction) error
}

type AllowOwnerAuthorizer struct{}

func (AllowOwnerAuthorizer) AuthorizeEnrollment(context.Context, string, OwnerAction) error {
	return nil
}

type denyOwnerAuthorizer struct{}

func (denyOwnerAuthorizer) AuthorizeEnrollment(context.Context, string, OwnerAction) error {
	return ErrUnauthorized
}

type Config struct {
	PendingLifetime time.Duration
	MaxPending      int
	MaxAttempts     int
}

func DefaultConfig() Config {
	return Config{PendingLifetime: 10 * time.Minute, MaxPending: 128, MaxAttempts: 8}
}
