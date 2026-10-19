package network

import (
	"fmt"
	"net/netip"
)

// portableVersion is the current schema version for PortableNetwork.
// Bump when the portable format changes incompatibly (old-format PortableNetworks
// must be migrated or rejected).
const portableVersion = 1

// PortableNetwork is the durable, machine-independent representation of a
// Doll Network that may cross a Core migration boundary.
//
// It is explicitly designed as the boundary between what can be exported
// from a live Core and what must be re-established after migration:
//
// Portable (included):
//   - Network identity (NetworkID)
//   - Core identity (PeerID, WireGuard keypair, overlay address + prefix)
//   - Body memberships (BodyID, PeerID, WireGuard public keys,
//     overlay addresses, membership status)
//
// NOT portable (deliberately excluded — re-established at runtime):
//   - Endpoints (live direct UDP addresses)
//   - sockets, interfaces, routes
//   - live Relay tunnels, packet mappings, transient handshakes
//   - runtime handles (allocator, supervisors, epochs)
//
// The type is JSON-serializable by encoding/json and can be sent across
// process or machine boundaries.
type PortableNetwork struct {
	Version     int                  `json:"version"`
	NetworkID   NetworkID            `json:"network_id"`
	Core        PortableCoreIdentity `json:"core"`
	Memberships []PortableMembership `json:"memberships,omitempty"`
}

// PortableCoreIdentity is the portable subset of CoreIdentity.
type PortableCoreIdentity struct {
	PeerID         PeerID              `json:"peer_id"`
	PrivateKey     WireGuardPrivateKey `json:"private_key"`
	PublicKey      WireGuardPublicKey  `json:"public_key"`
	OverlayAddress netip.Addr          `json:"overlay_address"`
	OverlayPrefix  netip.Prefix        `json:"overlay_prefix"`
}

// PortableMembership is the portable subset of Membership.
// Endpoint is deliberately absent: it is live transport state and must be
// re-established by path discovery after reconstruction, never migrated.
type PortableMembership struct {
	BodyID             string              `json:"body_id"`
	PeerID             PeerID              `json:"peer_id"`
	WireGuardPublicKey *WireGuardPublicKey `json:"wg_public_key,omitempty"`
	OverlayAddress     netip.Addr          `json:"overlay_address"`
	Status             MembershipStatus    `json:"status"`
}

// Portable returns the portable/reconstructible boundary representation of
// this Network. All live transport state (Endpoint etc.) is dropped.
//
// The resulting PortableNetwork is JSON-serializable and can be reconstructed
// on another (or cold-restarted) Core instance via ReconstructNetwork.
func (n *Network) Portable() *PortableNetwork {
	p := &PortableNetwork{
		Version:   portableVersion,
		NetworkID: n.NetworkID,
		Core: PortableCoreIdentity{
			PeerID:         n.Core.PeerID,
			PrivateKey:     n.Core.PrivateKey,
			PublicKey:      n.Core.PublicKey,
			OverlayAddress: n.Core.OverlayAddress,
			OverlayPrefix:  n.Core.OverlayPrefix,
		},
	}
	if len(n.Memberships) > 0 {
		p.Memberships = make([]PortableMembership, 0, len(n.Memberships))
	}
	for _, m := range n.Memberships {
		p.Memberships = append(p.Memberships, PortableMembership{
			BodyID:             m.BodyID,
			PeerID:             m.PeerID,
			WireGuardPublicKey: m.WireGuardPublicKey,
			OverlayAddress:     m.OverlayAddress,
			Status:             m.Status,
			// Endpoint is NOT carried: it is live transport state, not
			// durable network membership.
		})
	}
	return p
}

// ReconstructNetwork rebuilds a fully usable Network from portable state.
//
// The allocator is re-derived deterministically from NetworkID; memberships
// are restored with durable fields only — Endpoint stays empty and is
// re-established by live path discovery after the Core starts on a new
// machine.
//
// Validation checks:
//   - Version compatibility
//   - NetworkID and Core identity completeness
//   - Overlay prefix matches the deterministic prefix derived from NetworkID
//   - Overlay addresses (core and all memberships) are within the prefix
//   - WireGuard keypair consistency (pub = f(priv))
//   - Membership status is a valid enum value
//   - No duplicate PeerIDs
func (p *PortableNetwork) ReconstructNetwork() (*Network, error) {
	if p == nil {
		return nil, fmt.Errorf("network: nil portable state")
	}
	if p.Version != portableVersion {
		return nil, fmt.Errorf("network: unsupported portable version %d (want %d)", p.Version, portableVersion)
	}
	if p.NetworkID == "" {
		return nil, fmt.Errorf("network: portable state missing network id")
	}

	// Re-derive the allocator deterministically from NetworkID — this is
	// how NewNetwork / LoadNetwork also reconstruct the address space.
	alloc := NewIPv6Allocator(p.NetworkID)
	prefix := alloc.Prefix()

	// Validate core identity.
	if p.Core.PeerID == "" {
		return nil, fmt.Errorf("network: portable state has empty core peer id")
	}
	coreAddr, err := alloc.CoreAddress()
	if err != nil {
		return nil, fmt.Errorf("network: derive core address from network id: %w", err)
	}
	if !p.Core.OverlayAddress.IsValid() || p.Core.OverlayAddress.Compare(coreAddr) != 0 {
		return nil, fmt.Errorf("network: portable core overlay address %v does not match NetworkID-derived address %v",
			p.Core.OverlayAddress, coreAddr)
	}
	if !p.Core.OverlayPrefix.IsValid() || p.Core.OverlayPrefix != prefix {
		return nil, fmt.Errorf("network: portable core overlay prefix %v does not match NetworkID-derived prefix %v",
			p.Core.OverlayPrefix, prefix)
	}

	// Validate WireGuard keypair consistency — both are stored so this is
	// an integrity check against corruption or tampering.
	if p.Core.PrivateKey == (WireGuardPrivateKey{}) {
		return nil, fmt.Errorf("network: portable core private key is zero")
	}
	if p.Core.PublicKey == (WireGuardPublicKey{}) {
		return nil, fmt.Errorf("network: portable core public key is zero")
	}
	expectedPub, err := WireGuardPublicKeyFromPrivate(p.Core.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("network: derive core public key from private: %w", err)
	}
	if p.Core.PublicKey != expectedPub {
		return nil, fmt.Errorf("network: portable core public key does not match private key")
	}

	// Rebuild the network.
	n := &Network{
		NetworkID: p.NetworkID,
		Core: CoreIdentity{
			PeerID:         p.Core.PeerID,
			PrivateKey:     p.Core.PrivateKey,
			PublicKey:      p.Core.PublicKey,
			OverlayAddress: p.Core.OverlayAddress,
			OverlayPrefix:  p.Core.OverlayPrefix,
		},
		Memberships: make(map[PeerID]*Membership, len(p.Memberships)),
	}

	// Validate and restore memberships.
	for _, pm := range p.Memberships {
		if pm.PeerID == "" {
			return nil, fmt.Errorf("network: portable membership with empty peer id")
		}
		if pm.BodyID == "" {
			return nil, fmt.Errorf("network: portable membership %s has empty body id", pm.PeerID)
		}
		if !pm.OverlayAddress.IsValid() || !prefix.Contains(pm.OverlayAddress) {
			return nil, fmt.Errorf("network: portable membership %s overlay address %v outside prefix %v",
				pm.PeerID, pm.OverlayAddress, prefix)
		}
		// A membership can never own the Core's identity or its overlay
		// address — the allocator derives the Core address from the network
		// ID and would never allocate it to a Body.
		if pm.PeerID == p.Core.PeerID {
			return nil, fmt.Errorf("network: portable membership %s collides with core peer id", pm.PeerID)
		}
		if pm.OverlayAddress == p.Core.OverlayAddress {
			return nil, fmt.Errorf("network: portable membership %s collides with core overlay address", pm.PeerID)
		}
		if _, dup := n.Memberships[pm.PeerID]; dup {
			return nil, fmt.Errorf("network: portable state contains duplicate membership %s", pm.PeerID)
		}
		switch pm.Status {
		case MembershipPending, MembershipActive, MembershipRevoked:
		default:
			return nil, fmt.Errorf("network: portable membership %s has invalid status %q", pm.PeerID, pm.Status)
		}

		n.Memberships[pm.PeerID] = &Membership{
			BodyID:             pm.BodyID,
			PeerID:             pm.PeerID,
			WireGuardPublicKey: pm.WireGuardPublicKey,
			OverlayAddress:     pm.OverlayAddress,
			// Endpoint stays empty: it is live transport state and must be
			// re-established by path discovery after reconstruction.
			Status: pm.Status,
		}
	}

	return n, nil
}
