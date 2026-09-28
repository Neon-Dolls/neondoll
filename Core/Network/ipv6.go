package network

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
)

// ULA constants per RFC 4193.
const (
	ulaPrefix       = 0xfd // ULA prefix (fd00::/8)
	ulaSubnetID     = 0x0001
	ulaPrefixLength = 64 // bits
)

// IPv6Allocator manages deterministic allocation of IPv6 ULA addresses
// within a Doll Network. Allocation is derived from the network ID so it
// is deterministic, testable, and independent of real network state.
//
// Address format:
//
//		fd<40-bit-global-id>:<16-bit-subnet-id>::<64-bit-interface-id>
//
//	  - 40-bit Global ID = first 40 bits of SHA256(network_id)
//	  - 16-bit Subnet ID = 0x0001 (fixed for v1)
//	  - Core interface ID = ::1
//	  - Body interface ID = last 64 bits of SHA256(peer_id)
type IPv6Allocator struct {
	prefix netip.Prefix
}

// NewIPv6Allocator creates an allocator for the given NetworkID.
// The prefix is derived deterministically from the network ID so that
// reconstruction produces the same address space.
func NewIPv6Allocator(netID NetworkID) *IPv6Allocator {
	hash := sha256.Sum256([]byte(netID))

	// Build the 64-bit prefix: fd + 40-bit global ID + 16-bit subnet.
	// Store in a 16-byte net.IP (IPv6).
	var ip16 [16]byte
	ip16[0] = ulaPrefix

	// Copy 40 bits of global ID starting at byte 0 offset 8 bits in.
	// ip16[0] has the fd prefix (8 bits), so the global ID starts at ip16[0] bit 0 (which is half-used).
	// Better approach: construct the first 8 bytes explicitly.
	// Format: fd GG:GG:SS:SS (where G=global_id nibbles, S=subnet)
	// First 8 bytes: 0xfd, global_id byte 0-4, subnet byte 0-1

	// Global ID = first 40 bits from hash. We'll use bytes 0-4 of hash (40 bits).
	// ip16[1] = top 8 bits of global ID (hash[0])
	// ip16[2] = next 8 bits (hash[1])
	// ip16[3] = next 8 bits (hash[2])
	// ip16[4] = next 8 bits (hash[3])
	// ip16[5] = bottom 8 bits (hash[4] << 4) | (ulaSubnetID >> 8)
	// ... this is getting complicated.

	// Simpler: build the prefix as a netip.Prefix directly.
	// First 8 bytes of the address:
	// Byte 0: 0xfd
	// Bytes 1-5: global ID (40 bits from hash[0:5])
	// Bytes 6-7: subnet ID (16 bits = 0x0001)

	addrBytes := [16]byte{}
	addrBytes[0] = ulaPrefix
	copy(addrBytes[1:6], hash[:5]) // 40-bit global ID
	binary.BigEndian.PutUint16(addrBytes[6:8], ulaSubnetID)

	addr := netip.AddrFrom16(addrBytes)
	prefix := netip.PrefixFrom(addr, ulaPrefixLength)

	return &IPv6Allocator{prefix: prefix}
}

// CoreAddress returns the reserved address for the Core itself.
// Core always gets ::1 within the ULA prefix.
func (a *IPv6Allocator) CoreAddress() (netip.Addr, error) {
	// Take the prefix base address and set the interface ID to ::1.
	base := a.prefix.Addr()
	addrBytes := base.As16()
	// Set the last 8 bytes (interface ID) to ::1
	for i := 8; i < 16; i++ {
		addrBytes[i] = 0
	}
	addrBytes[15] = 1
	return netip.AddrFrom16(addrBytes), nil
}

// BodyAddress computes the deterministic IPv6 address for a Body
// identified by its PeerID. The interface ID is derived from SHA256(peer_id).
func (a *IPv6Allocator) BodyAddress(peerID PeerID) (netip.Addr, error) {
	if peerID == "" {
		return netip.Addr{}, fmt.Errorf("network: cannot allocate address for empty peer_id")
	}

	hash := sha256.Sum256([]byte(peerID))

	// Take the prefix base address.
	base := a.prefix.Addr()
	addrBytes := base.As16()

	// Interface ID = last 64 bits of SHA256(peer_id).
	// Bytes 8-15 of the address = hash[8:16] (last 8 bytes of the 32-byte hash).
	// This gives us the lower 64 bits, which is the standard for SLAAC/EUI-64-like allocation.
	copy(addrBytes[8:16], hash[8:16])

	return netip.AddrFrom16(addrBytes), nil
}

// Prefix returns the ULA prefix for this allocator (64-bit).
func (a *IPv6Allocator) Prefix() netip.Prefix {
	return a.prefix
}

// AllocateBodyAddress allocates a unique address for a Body, checking
// against a provided set of already-allocated addresses. Returns the
// address, or ErrAddressCollision if all deterministic slots are exhausted
// (practically impossible with 64-bit hash space).
func (a *IPv6Allocator) AllocateBodyAddress(peerID PeerID, allocated map[netip.Addr]struct{}) (netip.Addr, error) {
	addr, err := a.BodyAddress(peerID)
	if err != nil {
		return netip.Addr{}, err
	}

	if allocated != nil {
		if _, exists := allocated[addr]; exists {
			return netip.Addr{}, fmt.Errorf("%w: address %s derived from peer_id %s",
				ErrAddressCollision, addr, peerID)
		}
	}

	return addr, nil
}

// MustParseIPv6 parses an IPv6 string or panics. For use in tests only.
func MustParseIPv6(s string) netip.Addr {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		panic(fmt.Sprintf("invalid IPv6 address %q: %v", s, err))
	}
	return addr
}
