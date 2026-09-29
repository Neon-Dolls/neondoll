// Package pairing provides the pairing service and authorization boundary
// for Body-Core pairings.
package pairing

import (
	"context"
	"fmt"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

// ── Authorizer — the authorization boundary ──────────────────────────────────

// Authorizer decides whether a Body is permitted to pair with this Core.
// The pairing HTTP handler calls Authorizer before creating a membership;
// the handler is NOT the authority itself.
//
// Implementations MUST deny by default. Returning a non-nil error denies
// the pairing; the error message becomes the protocol-level denial reason.
type Authorizer interface {
	// AuthorizePairing checks whether the Body identified by bodyID and
	// the given request metadata is allowed to pair. A nil return means
	// allowed; a non-nil return denies the pairing with the given reason.
	AuthorizePairing(ctx context.Context, bodyID string, req *dollnetwork.PairRequest) error
}

// ── AllowAuthorizer — approves every request (dev/test only) ─────────────────

// AllowAuthorizer approves every pairing request. Use for development and
// tests only; production deployments MUST inject a restrictive authorizer.
type AllowAuthorizer struct{}

func (AllowAuthorizer) AuthorizePairing(_ context.Context, _ string, _ *dollnetwork.PairRequest) error {
	return nil
}

// ── DenyAuthorizer — always denies (for testing) ─────────────────────────────

// DenyAuthorizer denies every pairing request.
type DenyAuthorizer struct{}

func (DenyAuthorizer) AuthorizePairing(_ context.Context, _ string, _ *dollnetwork.PairRequest) error {
	return fmt.Errorf("denied by test authorizer")
}