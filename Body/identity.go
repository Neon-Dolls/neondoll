// Package body implements the reusable, Core-independent runtime for
// a NeonDoll reference Body.
//
// This runtime is the executable specification for the Body Contract and the
// Doll Network protocol. It deliberately does NOT import any Core-internal
// package: a conforming Body must be implementable from the public protocol
// documents alone (see body-contract.md and doll-network-protocol.md).
//
// M1 — Body Has an Identity:
//   - a stable Body ID persisted locally;
//   - implementation/platform/arch metadata;
//   - a Body-owned WireGuard (X25519) keypair, generated and retained locally;
//   - persistent identity/membership state kept separate from transient
//     connection/endpoint state;
//   - canonical pairing-request data containing only the public key.

package body

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"crypto/rand"
)

// BodyID is a stable opaque identifier for this Body. It is independent of
// any process identity, transport connection, network address, session, or
// authentication credential, and it is created exactly once (on fresh
// installation), then reused across restarts.
//
// A Body ID is opaque: peers and Core treat it as an opaque string.
type BodyID string

// NewBodyID generates a fresh high-entropy Body ID from crypto/rand.
func NewBodyID() (BodyID, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("body: generate body id: %w", err)
	}
	return BodyID("body_" + hex.EncodeToString(buf)), nil
}

// BodyMetadata describes the Body's implementation and platform so Core can
// reason about compatibility (Body Contract: Body Identity). The exact set
// here is the M1 surface; the protocol may add fields without changing
// identity semantics.
type BodyMetadata struct {
	Implementation string `json:"implementation"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
}

// BodyIdentity is the stable identity of this Body: an opaque BodyID and its
// descriptive metadata. It is portable and survives restart.
type BodyIdentity struct {
	BodyID BodyID       `json:"body_id"`
	Name   string       `json:"name,omitempty"`
	Meta   BodyMetadata `json:"meta"`
}

// CurrentIdentityVersion is the format version of the persistable identity
// envelope. Bump it (and teach UnmarshalIdentity) before changing the shape.
const CurrentIdentityVersion = 1

// IdentityState is the persistable form of the Body identity. Keeping the
// protocol-facing BodyIdentity nested under a versioned envelope lets the
// runtime evolve its file format without coupling to the on-the-wire shape.
type IdentityState struct {
	Version  int          `json:"version"`
	Identity BodyIdentity `json:"identity"`
}

// NewIdentityState builds a fresh identity state with a new Body ID and the
// given metadata / optional human-readable name.
func NewIdentityState(name string, meta BodyMetadata) (*IdentityState, error) {
	id, err := NewBodyID()
	if err != nil {
		return nil, err
	}
	return &IdentityState{
		Version: CurrentIdentityVersion,
		Identity: BodyIdentity{
			BodyID: id,
			Name:   name,
			Meta:   meta,
		},
	}, nil
}

// Marshal returns the canonical JSON encoding of the identity state.
func (s *IdentityState) Marshal() (string, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", fmt.Errorf("body: marshal identity: %w", err)
	}
	return string(data), nil
}

// UnmarshalIdentity parses and validates identity state from JSON.
func UnmarshalIdentity(raw string) (*IdentityState, error) {
	var st IdentityState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, fmt.Errorf("body: unmarshal identity: %w", err)
	}
	if st.Version != CurrentIdentityVersion {
		return nil, fmt.Errorf("body: unsupported identity version %d", st.Version)
	}
	if st.Identity.BodyID == "" {
		return nil, errors.New("body: identity missing body_id")
	}
	if st.Identity.Meta.Implementation == "" {
		return nil, errors.New("body: identity missing implementation")
	}
	return &st, nil
}
