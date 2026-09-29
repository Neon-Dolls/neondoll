package network

import (
	"crypto/rand"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/crypto/curve25519"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

// GenerateNetworkID creates a new opaque, stable Doll Network identifier.
// It is a UUIDv4 and is NOT derived from any runtime property.
func GenerateNetworkID() NetworkID {
	return NetworkID(uuid.New().String())
}

// GeneratePeerID creates a new opaque peer identifier for a network member.
// It is a UUIDv4 and is NOT derived from any runtime property.
func GeneratePeerID() PeerID {
	return PeerID(uuid.New().String())
}

// GenerateWireGuardKeypair creates a new Curve25519 keypair suitable for
// WireGuard. Returns (privateKey, publicKey, error).
//
// The private key is clamped per WireGuard/RFC 7748:
//   - bits 0, 1, 2 of the first byte are cleared
//   - bit 7 of the last byte is cleared
//   - bit 6 of the last byte is set
func GenerateWireGuardKeypair() (WireGuardPrivateKey, WireGuardPublicKey, error) {
	var priv WireGuardPrivateKey
	_, err := rand.Read(priv[:])
	if err != nil {
		return priv, WireGuardPublicKey{}, fmt.Errorf("network: generate wg private key: %w", err)
	}

	// Clamp per RFC 7748 / WireGuard convention.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pubBytes, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return priv, WireGuardPublicKey{}, fmt.Errorf("network: generate wg public key: %w", err)
	}

	var pub WireGuardPublicKey
	copy(pub[:], pubBytes)
	return priv, pub, nil
}

// NewCoreIdentity creates a new Core peer identity with a fresh WireGuard
// keypair and reserved overlay address. The overlay address is the first
// valid host address in the network's ULA prefix (::1 within the subnet).
func NewCoreIdentity(alloc *IPv6Allocator) (*CoreIdentity, error) {
	peerID := GeneratePeerID()
	priv, pub, err := GenerateWireGuardKeypair()
	if err != nil {
		return nil, err
	}

	addr, err := alloc.CoreAddress()
	if err != nil {
		return nil, fmt.Errorf("network: core overlay address: %w", err)
	}

	return &CoreIdentity{
		PeerID:         peerID,
		PrivateKey:     priv,
		PublicKey:      pub,
		OverlayAddress: addr,
	}, nil
}

// NewNetwork creates a new Doll Network with the given network ID, a new
// Core identity derived from that network ID, and an empty membership set.
// The NetworkID is the single source of truth: the allocator is constructed
// from it, ensuring the IPv6 ULA prefix is always consistent with the
// network identity.
func NewNetwork(netID NetworkID) (*Network, error) {
	alloc := NewIPv6Allocator(netID)
	coreID, err := NewCoreIdentity(alloc)
	if err != nil {
		return nil, err
	}

	return &Network{
		NetworkID:   netID,
		Core:        *coreID,
		Memberships: make(map[PeerID]*Membership),
		alloc:       alloc,
	}, nil
}

// allocator returns the internal IPv6 allocator, lazily initialising it
// from the NetworkID if necessary (for deserialised/reconstructed Networks).
func (n *Network) allocator() *IPv6Allocator {
	if n.alloc == nil {
		n.alloc = NewIPv6Allocator(n.NetworkID)
	}
	return n.alloc
}

// Strings returns a non-secret diagnostic summary of the Core identity.
// The private key is NEVER included. Public key is shown via the shared
// Doll Network wire representation (base64).
func (id *CoreIdentity) Strings() string {
	// PublicKey is always 32 bytes, so EncodeWgPublicKey cannot error here.
	pub, _ := dollnetwork.EncodeWgPublicKey(id.PublicKey[:])
	return fmt.Sprintf("peer_id=%s public_key=%s overlay=%s",
		id.PeerID, pub, id.OverlayAddress)
}

// WireGuardPublicKeyFromPrivate derives the public key from a clamped
// Curve25519 private key.
func WireGuardPublicKeyFromPrivate(priv WireGuardPrivateKey) (WireGuardPublicKey, error) {
	var pub WireGuardPublicKey
	pubBytes, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return WireGuardPublicKey{}, fmt.Errorf("derive wg public key: %w", err)
	}
	copy(pub[:], pubBytes)
	return pub, nil
}

// NewMembership creates a new Body membership in the Pending state.
// It assigns a unique peer_id, allocates an IPv6 address via the
// network's internal allocator, and verifies there is no collision.
// The Body's WireGuard public key is initially nil and will be set
// during the pairing protocol.
func (n *Network) NewMembership(bodyID string) (*Membership, error) {
	if bodyID == "" {
		return nil, fmt.Errorf("network: body_id must not be empty")
	}

	// Check for duplicate body_id. If the existing membership is revoked,
	// return ErrMembershipRevoked rather than a generic already-exists error.
	for _, m := range n.Memberships {
		if m.BodyID == bodyID {
			if m.Status == MembershipRevoked {
				return nil, ErrMembershipRevoked
			}
			return nil, ErrMembershipAlreadyExists
		}
	}

	peerID := GeneratePeerID()
	addr, err := n.allocator().BodyAddress(peerID)
	if err != nil {
		return nil, err
	}

	// Collision detection: ensure the address is not already assigned.
	for _, m := range n.Memberships {
		if m.OverlayAddress == addr {
			return nil, fmt.Errorf("%w: address %s already assigned to peer %s",
				ErrAddressCollision, addr, m.PeerID)
		}
	}

	m := &Membership{
		BodyID:             bodyID,
		PeerID:             peerID,
		WireGuardPublicKey: nil,
		OverlayAddress:     addr,
		Status:             MembershipPending,
	}
	n.Memberships[peerID] = m
	return m, nil
}

// ActivateMembership transitions a membership to the Active state.
// Returns ErrMembershipRevoked if the membership has been revoked.
func (n *Network) ActivateMembership(peerID PeerID) error {
	m, ok := n.Memberships[peerID]
	if !ok {
		return ErrMembershipNotFound
	}
	if m.Status == MembershipRevoked {
		return ErrMembershipRevoked
	}
	m.Status = MembershipActive
	return nil
}

// RevokeMembership permanently revokes a membership.
// Once revoked, the membership cannot be reactivated.
func (n *Network) RevokeMembership(peerID PeerID) error {
	m, ok := n.Memberships[peerID]
	if !ok {
		return ErrMembershipNotFound
	}
	m.Status = MembershipRevoked
	return nil
}

// ListMemberships returns all memberships in a stable order (sorted by PeerID).
// This is the recommended way to iterate memberships.
func (n *Network) ListMemberships() []*Membership {
	if len(n.Memberships) == 0 {
		return nil
	}
	ids := make([]PeerID, 0, len(n.Memberships))
	for id := range n.Memberships {
		ids = append(ids, id)
	}
	sortPeerIDs(ids)

	result := make([]*Membership, len(ids))
	for i, id := range ids {
		result[i] = n.Memberships[id]
	}
	return result
}

// GetMembership returns the membership for the given PeerID, or nil.
func (n *Network) GetMembership(peerID PeerID) *Membership {
	return n.Memberships[peerID]
}

// SetBodyWireGuardPublicKey sets a Body's WireGuard public key.
// This is a no-op if the membership is revoked.
func (n *Network) SetBodyWireGuardPublicKey(peerID PeerID, pub WireGuardPublicKey) error {
	m, ok := n.Memberships[peerID]
	if !ok {
		return ErrMembershipNotFound
	}
	if m.Status == MembershipRevoked {
		return ErrMembershipRevoked
	}
	m.WireGuardPublicKey = &pub
	return nil
}

// RemoveMembership removes a membership from the network entirely.
// This is a destructive operation — use RevokeMembership for revocation.
func (n *Network) RemoveMembership(peerID PeerID) error {
	if _, ok := n.Memberships[peerID]; !ok {
		return ErrMembershipNotFound
	}
	delete(n.Memberships, peerID)
	return nil
}

// sortPeerIDs sorts a slice of PeerIDs in lexical order for stable iteration.
func sortPeerIDs(ids []PeerID) {
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[i] < ids[j] {
				continue
			}
			ids[i], ids[j] = ids[j], ids[i]
		}
	}
}
