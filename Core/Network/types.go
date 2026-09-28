// Package network provides the durable identity and membership model for
// Doll Network membership. It is Core-local infrastructure — NOT part of
// Doll State, Doll Card, or the Doll Link protocol.
//
// A Doll Network is an overlay network (IPv6-only ULA) that connects a
// Core instance with its remote Doll Bodies through an encrypted mesh.
// Core 4 M1 establishes the data model and cryptographic boundary that
// later milestones (pairing, Relay, WireGuard, path selection) consume.
package network

import (
	"encoding/hex"
	"fmt"
	"net/netip"
)

// NetworkID is a stable opaque identifier for a Doll Network.
// It must survive runtime reconstruction and must NOT be derived from
// an IP address, interface, process, socket, machine address, or
// runtime handle.
type NetworkID string

// PeerID is a stable opaque identifier for a network peer (Core or Body).
type PeerID string

// MembershipStatus represents the lifecycle of a Body's network membership.
type MembershipStatus string

const (
	// MembershipPending means the Body has been invited but has not yet
	// completed pairing.
	MembershipPending MembershipStatus = "pending"

	// MembershipActive means the Body is a fully participating network member.
	MembershipActive MembershipStatus = "active"

	// MembershipRevoked means the Body's membership has been permanently
	// revoked. A revoked membership must never become active again due
	// to the Body reappearing, its IP changing, its old public key
	// appearing, an endpoint existing, or runtime state reconstruction.
	MembershipRevoked MembershipStatus = "revoked"
)

// WireGuardPrivateKey is a 32-byte Curve25519 secret key, owned by Core.
// It must NEVER appear in network protocol documents, Body-visible
// structures, Relay-visible structures, logs, or diagnostic serialization.
type WireGuardPrivateKey [32]byte

// String returns a hex representation of the private key.
// The zero value is detectable but not exposed as a valid key.
func (k WireGuardPrivateKey) String() string {
	return hex.EncodeToString(k[:])
}

// WireGuardPublicKey is a 32-byte Curve25519 public key, derivable from
// the private key via ScalarBaseMult. It MAY cross network boundaries
// during pairing and membership announcements.
type WireGuardPublicKey [32]byte

// String returns a hex representation of the public key.
func (k WireGuardPublicKey) String() string {
	return hex.EncodeToString(k[:])
}

// CoreIdentity encapsulates Core's own network peer identity and its
// WireGuard keypair. The private key is secret local material; only
// the public key crosses network boundaries.
type CoreIdentity struct {
	PeerID         PeerID
	PrivateKey     WireGuardPrivateKey
	PublicKey      WireGuardPublicKey
	OverlayAddress netip.Addr
}

// Membership represents a remote Body's membership in the Doll Network.
// It refers to a Body by body_id but is NOT a second Body identity system.
// Membership is a Core-side relationship record.
type Membership struct {
	// BodyID is the canonical identity of the Doll Body, from body-contract.
	// This is a reference to an existing Body identity, not a duplicate.
	BodyID string

	// PeerID is this Body's network peer identity, distinct from body_id.
	// Each membership gets a unique peer_id during creation.
	PeerID PeerID

	// WireGuardPublicKey is the remote Body's WireGuard public key,
	// provided during pairing. Null until the Body has presented its key.
	WireGuardPublicKey *WireGuardPublicKey

	// OverlayAddress is the assigned IPv6 ULA address for this Body.
	// Core is the address authority.
	OverlayAddress netip.Addr

	// Status is the current membership lifecycle state.
	Status MembershipStatus
}

// Network represents the Core-side view of a Doll Network: a private
// overlay network for a Doll House. It bundles the network identity,
// Core's own peer identity, and the set of Body memberships.
type Network struct {
	// NetworkID is the stable opaque identity of this network.
	NetworkID NetworkID

	// Core holds Core's own peer identity and WireGuard keypair.
	Core CoreIdentity

	// Memberships indexes Body memberships by PeerID.
	// Order is non-deterministic; use ListMemberships for stable iteration.
	Memberships map[PeerID]*Membership
}

// ErrAddressCollision is returned when an IPv6 address allocation
// collides with an existing member's address.
var ErrAddressCollision = fmt.Errorf("network: IPv6 address collision")

// ErrMembershipNotFound is returned when no membership exists for the
// given PeerID.
var ErrMembershipNotFound = fmt.Errorf("network: membership not found")

// ErrMembershipAlreadyExists is returned when a membership for the given
// PeerID or BodyID already exists.
var ErrMembershipAlreadyExists = fmt.Errorf("network: membership already exists")

// ErrMembershipRevoked is returned when an operation is attempted on a
// revoked membership.
var ErrMembershipRevoked = fmt.Errorf("network: membership is revoked")
