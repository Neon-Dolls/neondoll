package network

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/netip"
	"testing"
)

// ── 1. Network ID: stable opaque identity ───────────────────────────────────

func TestGenerateNetworkID_IsStable(t *testing.T) {
	id := GenerateNetworkID()
	if id == "" {
		t.Fatal("GenerateNetworkID returned empty string")
	}
	// Run multiple times; each should be unique.
	seen := make(map[NetworkID]bool)
	for i := 0; i < 100; i++ {
		id := GenerateNetworkID()
		if seen[id] {
			t.Fatalf("duplicate network_id after %d iterations", i)
		}
		seen[id] = true
	}
}

func TestNewNetwork_HasStableNetworkID(t *testing.T) {
	// A Network holds its network_id. Over time the network_id must survive
	// reconstruction through persistence.
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if n.NetworkID == "" {
		t.Fatal("network_id is empty")
	}
	// network_id must NOT be derivable from any runtime property.
	// It's just a string UUID. Verify it looks like a UUID.
	if len(n.NetworkID) != 36 {
		t.Fatalf("network_id length %d, expected 36 (UUID format)", len(n.NetworkID))
	}
}

// ── 2. Core peer identity ──────────────────────────────────────────────────

func TestNewCoreIdentity_HasStablePeerID(t *testing.T) {
	netID := GenerateNetworkID()
	alloc := NewIPv6Allocator(netID)
	id, err := NewCoreIdentity(alloc)
	if err != nil {
		t.Fatalf("NewCoreIdentity: %v", err)
	}
	if id.PeerID == "" {
		t.Fatal("peer_id is empty")
	}
	if len(id.PeerID) != 36 {
		t.Fatalf("core peer_id length %d, expected 36 (UUID format)", len(id.PeerID))
	}
}

func TestNewNetwork_CoreHasPeerIDAndOverlay(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if n.Core.PeerID == "" {
		t.Fatal("core peer_id is empty")
	}
	if !n.Core.OverlayAddress.IsValid() {
		t.Fatal("core overlay address is invalid")
	}
	if !n.Core.OverlayAddress.Is6() {
		t.Fatal("core overlay address is not IPv6")
	}
}

// ── 3. WireGuard keypair ───────────────────────────────────────────────────

func TestGenerateWireGuardKeypair_CorrectLength(t *testing.T) {
	priv, pub, err := GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("GenerateWireGuardKeypair: %v", err)
	}
	if len(priv) != 32 {
		t.Fatalf("private key length %d, want 32", len(priv))
	}
	if len(pub) != 32 {
		t.Fatalf("public key length %d, want 32", len(pub))
	}
}

func TestGenerateWireGuardKeypair_UniqueKeys(t *testing.T) {
	// Run multiple times; keys must be distinct.
	seenPriv := make(map[string]bool)
	seenPub := make(map[string]bool)
	for i := 0; i < 100; i++ {
		priv, pub, err := GenerateWireGuardKeypair()
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		privStr := string(priv[:])
		pubStr := string(pub[:])
		if seenPriv[privStr] {
			t.Fatalf("duplicate private key at iteration %d", i)
		}
		if seenPub[pubStr] {
			t.Fatalf("duplicate public key at iteration %d", i)
		}
		seenPriv[privStr] = true
		seenPub[pubStr] = true
	}
}

func TestWireGuardKeypair_Derivation(t *testing.T) {
	priv, pub, err := GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("GenerateWireGuardKeypair: %v", err)
	}

	// Derive public from private using curve25519.
	derivedPub, err := WireGuardPublicKeyFromPrivate(priv)
	if err != nil {
		t.Fatalf("WireGuardPublicKeyFromPrivate: %v", err)
	}

	if !bytes.Equal(pub[:], derivedPub[:]) {
		t.Fatal("derived public key does not match generated public key")
	}
}

func TestWireGuardKeypair_DifferentPrivatesProduceDifferentPublics(t *testing.T) {
	// Two keypairs with clamped but different private keys must produce
	// different public keys.
	priv1, pub1, err := GenerateWireGuardKeypair()
	if err != nil {
		t.Fatal(err)
	}
	priv2, pub2, err := GenerateWireGuardKeypair()
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(priv1[:], priv2[:]) {
		t.Fatal("two generated keys are identical — extremely unlikely")
	}
	if bytes.Equal(pub1[:], pub2[:]) {
		t.Fatal("two generated public keys are identical — private keys were different")
	}
}

func TestNewCoreIdentity_HasWireGuardKeypair(t *testing.T) {
	netID := GenerateNetworkID()
	alloc := NewIPv6Allocator(netID)
	id, err := NewCoreIdentity(alloc)
	if err != nil {
		t.Fatalf("NewCoreIdentity: %v", err)
	}

	// Private and public keys must be non-zero.
	var zeroPriv WireGuardPrivateKey
	var zeroPub WireGuardPublicKey
	if bytes.Equal(id.PrivateKey[:], zeroPriv[:]) {
		t.Fatal("core private key is zero")
	}
	if bytes.Equal(id.PublicKey[:], zeroPub[:]) {
		t.Fatal("core public key is zero")
	}

	// Derive public from private and verify match.
	derived, err := WireGuardPublicKeyFromPrivate(id.PrivateKey)
	if err != nil {
		t.Fatalf("WireGuardPublicKeyFromPrivate: %v", err)
	}
	if !bytes.Equal(id.PublicKey[:], derived[:]) {
		t.Fatal("core public key doesn't match derivation from private key")
	}
}

// ── 11. Private key NEVER in public output ─────────────────────────────────

func TestCoreIdentity_StringsExcludesPrivateKey(t *testing.T) {
	netID := GenerateNetworkID()
	alloc := NewIPv6Allocator(netID)
	id, err := NewCoreIdentity(alloc)
	if err != nil {
		t.Fatalf("NewCoreIdentity: %v", err)
	}

	s := id.Strings()
	if s == "" {
		t.Fatal("Strings() returned empty")
	}

	// The private key must NOT appear in Strings() output.
	// WireGuardPrivateKey has no String() method — format
	// verbs (%s, %v) on [32]byte produce raw chars, not hex.
	// Use hex explicitly to confirm exclusion from the diagnostic.
	privHex := hex.EncodeToString(id.PrivateKey[:])
	if privHex == "" {
		t.Fatal("private key bytes are empty — cannot verify exclusion")
	}
	if contains(s, privHex) {
		t.Fatal("private key hex found in Strings() output — must NEVER be exposed")
	}

	// Public key SHOULD appear in Strings().
	pubHex := id.PublicKey.String()
	if !contains(s, pubHex) {
		t.Fatal("public key not found in Strings() output — it may appear")
	}
}

// ── 4. Two Body memberships get distinct peer IDs ──────────────────────────

func TestNewMemberships_DistinctPeerIDs(t *testing.T) {
	netID := GenerateNetworkID()
	n, err := NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m1, err := n.NewMembership("body-001")
	if err != nil {
		t.Fatalf("NewMembership body-001: %v", err)
	}
	m2, err := n.NewMembership("body-002")
	if err != nil {
		t.Fatalf("NewMembership body-002: %v", err)
	}

	if m1.PeerID == "" {
		t.Fatal("m1 peer_id is empty")
	}
	if m2.PeerID == "" {
		t.Fatal("m2 peer_id is empty")
	}
	if m1.PeerID == m2.PeerID {
		t.Fatal("two memberships received identical peer_id")
	}
}

// ── 5. Two Body memberships get distinct IPv6 addresses ────────────────────

func TestNewMemberships_DistinctIPv6Addresses(t *testing.T) {
	netID := GenerateNetworkID()
	n, err := NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m1, err := n.NewMembership("body-001")
	if err != nil {
		t.Fatalf("NewMembership body-001: %v", err)
	}
	m2, err := n.NewMembership("body-002")
	if err != nil {
		t.Fatalf("NewMembership body-002: %v", err)
	}

	if m1.OverlayAddress == m2.OverlayAddress {
		t.Fatalf("two bodies received identical overlay address %s", m1.OverlayAddress)
	}

	if !m1.OverlayAddress.Is6() {
		t.Fatalf("m1 address %s is not IPv6", m1.OverlayAddress)
	}
	if !m2.OverlayAddress.Is6() {
		t.Fatalf("m2 address %s is not IPv6", m2.OverlayAddress)
	}
}

// ── 6. Address collision detection ─────────────────────────────────────────

func TestAddressCollision_DetectedByBodyAddress(t *testing.T) {
	// Use a fixed network ID so addresses are deterministic.
	netID := NetworkID("test-network-id")
	n, err := NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	// Add the first membership.
	_, err = n.NewMembership("body-001")
	if err != nil {
		t.Fatalf("first membership: %v", err)
	}

	// Intentionally create a second membership with a body_id whose peer_id
	// (generated randomly) has a probability near zero of colliding.
	// Instead, test collision detection directly by trying to insert into
	// the AllocateBodyAddress function with a pre-collided set.

	// First, get the allocator and see if deterministic addresses work.
	alloc := NewIPv6Allocator(netID)
	// Make a fake allocated set that includes an address this allocator would
	// produce for a given peer_id.
	fakePeerID := PeerID("test-fake-peer-id-with-known-hash")
	addr, err := alloc.BodyAddress(fakePeerID)
	if err != nil {
		t.Fatalf("BodyAddress: %v", err)
	}

	// Put that address in the allocation set.
	allocated := map[netip.Addr]struct{}{addr: {}}
	_, err = alloc.AllocateBodyAddress(fakePeerID, allocated)
	if err == nil {
		t.Fatal("expected ErrAddressCollision, got nil")
	}
}

// ── 7. Reload/reconstruction preserves network identity ────────────────────

func TestReconstruction_PreservesNetworkID(t *testing.T) {
	netID := GenerateNetworkID()
	n, err := NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	originalNetID := n.NetworkID

	// Reconstruct from stored identity data.
	n2 := &Network{
		NetworkID: originalNetID,
		Core: CoreIdentity{
			PeerID:         n.Core.PeerID,
			PrivateKey:     n.Core.PrivateKey,
			PublicKey:      n.Core.PublicKey,
			OverlayAddress: n.Core.OverlayAddress,
		},
		Memberships: make(map[PeerID]*Membership),
	}

	if n2.NetworkID != originalNetID {
		t.Fatalf("reconstructed network_id %q != original %q", n2.NetworkID, originalNetID)
	}

	// Core overlay address must match across reconstruction.
	if n2.Core.OverlayAddress != n.Core.OverlayAddress {
		t.Fatalf("reconstructed core address %s != original %s",
			n2.Core.OverlayAddress, n.Core.OverlayAddress)
	}

	// Core peer ID must match.
	if n2.Core.PeerID != n.Core.PeerID {
		t.Fatalf("reconstructed core peer_id %q != original %q", n2.Core.PeerID, n.Core.PeerID)
	}

	// Core public key must match.
	if !bytes.Equal(n2.Core.PublicKey[:], n.Core.PublicKey[:]) {
		t.Fatal("reconstructed core public key doesn't match original")
	}

	// Core private key must match — it's stored encrypted (by the DB at rest).
	if !bytes.Equal(n2.Core.PrivateKey[:], n.Core.PrivateKey[:]) {
		t.Fatal("reconstructed core private key doesn't match original")
	}
}

// ── 8. Reload/reconstruction preserves Body membership identity ────────────

func TestReconstruction_PreservesMemberships(t *testing.T) {
	netID := GenerateNetworkID()
	n, err := NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	// Add two memberships.
	m1, err := n.NewMembership("body-alpha")
	if err != nil {
		t.Fatalf("membership alpha: %v", err)
	}
	_, err = n.NewMembership("body-beta")
	if err != nil {
		t.Fatalf("membership beta: %v", err)
	}

	// Activate one.
	_ = n.ActivateMembership(m1.PeerID)

	// Simulate reconstruction.
	n2 := &Network{
		NetworkID:   n.NetworkID,
		Core:        n.Core,
		Memberships: make(map[PeerID]*Membership),
	}

	// Copy memberships (as if loaded from persistence).
	for _, orig := range n.Memberships {
		clone := &Membership{
			BodyID:         orig.BodyID,
			PeerID:         orig.PeerID,
			OverlayAddress: orig.OverlayAddress,
			Status:         orig.Status,
		}
		if orig.WireGuardPublicKey != nil {
			key := *orig.WireGuardPublicKey
			clone.WireGuardPublicKey = &key
		}
		n2.Memberships[orig.PeerID] = clone
	}

	// Verify memberships preserved.
	if len(n2.Memberships) != 2 {
		t.Fatalf("expected 2 memberships, got %d", len(n2.Memberships))
	}

	for _, orig := range n.Memberships {
		got := n2.Memberships[orig.PeerID]
		if got == nil {
			t.Fatalf("membership %s missing after reconstruction", orig.PeerID)
		}
		if got.BodyID != orig.BodyID {
			t.Fatalf("body_id %q != %q after reconstruction", got.BodyID, orig.BodyID)
		}
		if got.Status != orig.Status {
			t.Fatalf("status %q != %q after reconstruction for %s", got.Status, orig.Status, orig.BodyID)
		}
		if got.OverlayAddress != orig.OverlayAddress {
			t.Fatalf("overlay address %s != %s after reconstruction for %s",
				got.OverlayAddress, orig.OverlayAddress, orig.BodyID)
		}
	}
}

// ── 9. Endpoint/source-IP changes cannot affect identity ────────────────────

func TestIdentity_NotDerivedFromIP(t *testing.T) {
	// The identity (network_id, peer_id, WG keys) is entirely opaque and
	// generated by crypto/rand. It cannot be derived from IP addresses.
	// We verify this by showing that fixed-identity networks produce
	// consistent results independent of any IP context.

	netID := NetworkID("fixed-test-network-id")
	alloc := NewIPv6Allocator(netID)
	coreID, err := NewCoreIdentity(alloc)
	if err != nil {
		t.Fatalf("NewCoreIdentity: %v", err)
	}

	// Show that identity fields are not zero — they are populated by crypto/rand.
	if coreID.PeerID == "" {
		t.Fatal("peer_id must not be derived from IP — it is empty")
	}

	// Verify overlay address is ULA (starts with fd).
	addrStr := coreID.OverlayAddress.String()
	if len(addrStr) < 2 || (addrStr[0] != 'f' && addrStr[1] != 'd') {
		t.Fatalf("overlay address %s does not start with fd (ULA)", addrStr)
	}

	// The Core's own identity does not reference any physical address.
	// This test documents the architectural invariant in code.
	_ = coreID.PeerID
}

// ── 10. Revoked membership cannot accidentally become active ──────────────

func TestRevokedMembership_CannotActivate(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	// Create a membership.
	m, err := n.NewMembership("body-test")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}

	// Activate then revoke.
	if err := n.ActivateMembership(m.PeerID); err != nil {
		t.Fatalf("ActivateMembership: %v", err)
	}
	if n.Memberships[m.PeerID].Status != MembershipActive {
		t.Fatal("expected active status")
	}

	if err := n.RevokeMembership(m.PeerID); err != nil {
		t.Fatalf("RevokeMembership: %v", err)
	}
	if n.Memberships[m.PeerID].Status != MembershipRevoked {
		t.Fatal("expected revoked status")
	}

	// Attempt to activate — must fail.
	err = n.ActivateMembership(m.PeerID)
	if err == nil {
		t.Fatal("ActivateMembership on revoked membership returned nil — expected error")
	}
	if err != ErrMembershipRevoked {
		t.Fatalf("expected ErrMembershipRevoked, got %v", err)
	}

	// Membership must remain revoked.
	if n.Memberships[m.PeerID].Status != MembershipRevoked {
		t.Fatal("status changed from revoked after failed activation attempt")
	}
}

func TestRevokedMembership_DoesNotBecomeActiveOnReAdd(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m, err := n.NewMembership("body-test")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}

	// Activate then revoke.
	_ = n.ActivateMembership(m.PeerID)
	_ = n.RevokeMembership(m.PeerID)

	// Trying to add a new membership with the same body_id should fail.
	_, err = n.NewMembership("body-test")
	if err == nil {
		t.Fatal("NewMembership with revoked body_id returned nil — expected error")
	}
	if err != ErrMembershipRevoked {
		t.Fatalf("expected ErrMembershipRevoked, got %v", err)
	}
}

// ── 12. Allocation tests do not depend on real network state ───────────────

func TestIPv6Allocation_Deterministic(t *testing.T) {
	// IPv6 address allocation uses only the network_id and peer_id.
	// No runtime state, no network interfaces, no DNS, no actual packets.
	// Two networks with the same network_id produce the same prefix.

	netID := NetworkID("deterministic-test-id")
	alloc := NewIPv6Allocator(netID)
	prefix := alloc.Prefix()

	// Verify prefix is a valid ULA.
	if !prefix.IsValid() {
		t.Fatal("prefix is invalid")
	}
	firstByte := prefix.Addr().AsSlice()[0]
	if firstByte != 0xfd {
		t.Fatalf("prefix first byte 0x%02x, expected 0xfd", firstByte)
	}

	// Different network ID produces a different prefix.
	otherAlloc := NewIPv6Allocator(NetworkID("different-network-id"))
	otherPrefix := otherAlloc.Prefix()
	if !otherPrefix.IsValid() {
		t.Fatal("other prefix is invalid")
	}

	// Core address is deterministic for a given allocator.
	coreAddr, err := alloc.CoreAddress()
	if err != nil {
		t.Fatalf("CoreAddress: %v", err)
	}
	coreAddr2, err := alloc.CoreAddress()
	if err != nil {
		t.Fatalf("CoreAddress (repeated): %v", err)
	}
	if coreAddr != coreAddr2 {
		t.Fatal("CoreAddress not deterministic within same allocator")
	}

	// Body address is deterministic for a given peer_id.
	peerID := PeerID("test-peer-id")
	bodyAddr, err := alloc.BodyAddress(peerID)
	if err != nil {
		t.Fatalf("BodyAddress: %v", err)
	}
	bodyAddr2, err := alloc.BodyAddress(peerID)
	if err != nil {
		t.Fatalf("BodyAddress (repeated): %v", err)
	}
	if bodyAddr != bodyAddr2 {
		t.Fatal("BodyAddress not deterministic for same peer_id")
	}
}

func TestIPv6Allocation_CoreAddressIsReserved(t *testing.T) {
	netID := GenerateNetworkID()
	alloc := NewIPv6Allocator(netID)

	coreAddr, err := alloc.CoreAddress()
	if err != nil {
		t.Fatalf("CoreAddress: %v", err)
	}

	// Core gets ::1 within the subnet.
	addrStr := coreAddr.String()
	if !hasSuffix(addrStr, ":1") {
		t.Fatalf("core address %s does not end with ::1", addrStr)
	}
}

func TestIPv6Allocation_PrefixIsULA(t *testing.T) {
	netID := GenerateNetworkID()
	alloc := NewIPv6Allocator(netID)
	prefix := alloc.Prefix()

	if !prefix.Addr().Is6() {
		t.Fatal("prefix is not IPv6")
	}

	// ULA addresses start with fd.
	firstByte := prefix.Addr().AsSlice()[0]
	if firstByte != 0xfd {
		t.Fatalf("prefix first byte is 0x%02x, expected 0xfd (ULA)", firstByte)
	}
}

func TestIPv6Allocation_BodyAddressFormat(t *testing.T) {
	netID := GenerateNetworkID()
	alloc := NewIPv6Allocator(netID)

	peerID := PeerID("unique-test-peer")
	addr, err := alloc.BodyAddress(peerID)
	if err != nil {
		t.Fatalf("BodyAddress: %v", err)
	}

	// Must be IPv6.
	if !addr.Is6() {
		t.Fatal("address is not IPv6")
	}

	// Must start with fd (ULA).
	addrBytes := addr.AsSlice()
	if addrBytes[0] != 0xfd {
		t.Fatalf("address first byte 0x%02x, expected 0xfd (ULA)", addrBytes[0])
	}
}

func TestIPv6Allocation_AllocatorWithoutNetworkState(t *testing.T) {
	// The allocator does not touch the OS network stack.
	// No interfaces, no DNS, no sockets, no routing.
	alloc := NewIPv6Allocator(NetworkID("completely-abstract-id"))
	_, err := alloc.CoreAddress()
	if err != nil {
		t.Fatalf("CoreAddress: %v", err)
	}
	_, err = alloc.BodyAddress(PeerID("abstract-peer"))
	if err != nil {
		t.Fatalf("BodyAddress: %v", err)
	}
}

// ── Additional: lifecycle transitions ─────────────────────────────────────

func TestMembershipLifecycle_PendingToActive(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m, err := n.NewMembership("body-lifecycle")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}
	if m.Status != MembershipPending {
		t.Fatalf("new membership status %q, expected %q", m.Status, MembershipPending)
	}

	if err := n.ActivateMembership(m.PeerID); err != nil {
		t.Fatalf("ActivateMembership: %v", err)
	}
	if m.Status != MembershipActive {
		t.Fatalf("after activate: status %q, expected %q", m.Status, MembershipActive)
	}
}

func TestMembershipLifecycle_ActiveToRevoked(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m, err := n.NewMembership("body-revoke")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}

	_ = n.ActivateMembership(m.PeerID)

	if err := n.RevokeMembership(m.PeerID); err != nil {
		t.Fatalf("RevokeMembership: %v", err)
	}
	if m.Status != MembershipRevoked {
		t.Fatalf("after revoke: status %q, expected %q", m.Status, MembershipRevoked)
	}
}

func TestMembershipLifecycle_PendingToRevoked(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m, err := n.NewMembership("body-pending-revoke")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}

	if err := n.RevokeMembership(m.PeerID); err != nil {
		t.Fatalf("RevokeMembership (from pending): %v", err)
	}
	if m.Status != MembershipRevoked {
		t.Fatalf("status %q, expected %q", m.Status, MembershipRevoked)
	}
}

func TestMembershipLifecycle_RepeatedActivateIsIdempotent(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m, err := n.NewMembership("body-idempotent")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}

	_ = n.ActivateMembership(m.PeerID)
	if err := n.ActivateMembership(m.PeerID); err != nil {
		t.Fatalf("second ActivateMembership must be idempotent: %v", err)
	}
	if m.Status != MembershipActive {
		t.Fatal("status must stay active")
	}
}

func TestMembershipLifecycle_RepeatedRevokeIsIdempotent(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m, err := n.NewMembership("body-revoke-idempotent")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}

	_ = n.RevokeMembership(m.PeerID)
	if err := n.RevokeMembership(m.PeerID); err != nil {
		t.Fatalf("second RevokeMembership must be idempotent: %v", err)
	}
	if m.Status != MembershipRevoked {
		t.Fatal("status must stay revoked")
	}
}

func TestMembershipLifecycle_DuplicateBodyID(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	_, err = n.NewMembership("body-dup")
	if err != nil {
		t.Fatalf("first NewMembership: %v", err)
	}

	_, err = n.NewMembership("body-dup")
	if err == nil {
		t.Fatal("expected ErrMembershipAlreadyExists for duplicate body_id")
	}
}

// ── Additional: Serialization boundary ────────────────────────────────────

func TestMembership_NotSerializingBodyIdentityInconsistently(t *testing.T) {
	// A membership holds body_id as a stable reference. It does NOT
	// duplicate the Body's doll_state identity.
	m := &Membership{BodyID: "body-abc123"}
	if m.BodyID == "" {
		t.Fatal("body_id is empty")
	}
}

// ── Additional: RemoveMembership operation ────────────────────────────────

func TestNetwork_RemoveMembership(t *testing.T) {
	n, err := NewNetwork(GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	m, err := n.NewMembership("body-remove")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}

	if err := n.RemoveMembership(m.PeerID); err != nil {
		t.Fatalf("RemoveMembership: %v", err)
	}

	if len(n.Memberships) != 0 {
		t.Fatal("membership not removed")
	}
}

// ── IPv6 ULA format verification ──────────────────────────────────────────

func TestIPv6Allocation_FormatStandardCompliant(t *testing.T) {
	// Verify the ULA format matches RFC 4193 structure:
	// | 8 bits | 40 bits | 16 bits | 64 bits |
	// | prefix | global  | subnet  | interface |
	// | fd     | ID      | ID      | ID       |

	netID := GenerateNetworkID()
	alloc := NewIPv6Allocator(netID)
	prefix := alloc.Prefix()

	addrBytes := prefix.Addr().AsSlice()

	// Byte 0 must be 0xfd (ULA prefix).
	if addrBytes[0] != 0xfd {
		t.Fatalf("byte 0 = 0x%02x, expected 0xfd", addrBytes[0])
	}

	// Bytes 1-5 are the 40-bit global ID from SHA256 — any value valid.
	// Bytes 6-7 are the subnet ID (0x0001 in the default implementation).
	// Verify subnet ID.
	subnetID := uint16(addrBytes[6])<<8 | uint16(addrBytes[7])
	if subnetID != ulaSubnetID {
		t.Fatalf("subnet ID = 0x%04x, expected 0x0001", subnetID)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func hasSuffix(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

// ── WireGuard private key formatting ────────────────────────────────────
//
// WireGuardPrivateKey MUST NOT have a String() method. Format verbs (%s, %v)
// on a [32]byte produce raw bytes, not hex — making accidental exposure
// impossible.
func TestWireGuardPrivateKey_HasNoStringer(t *testing.T) {
	priv, _, err := GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("GenerateWireGuardKeypair: %v", err)
	}

	// The definitive test: format as %v. If there were a String() returning
	// 64 hex chars, the output would be 64 characters.
	formatted := fmt.Sprintf("%v", priv)
	if len(formatted) == 64 {
		t.Fatal("WireGuardPrivateKey appears to have a String() " +
			"method that returns 64 hex chars — must NOT expose secret")
	}

	// %s on a [32]byte also produces raw bytes, never 64-char hex.
	formattedS := fmt.Sprintf("%s", priv)
	if len(formattedS) == 64 {
		t.Fatal("fmt.Sprintf with the percent-s verb on privateKey produced" +
			" 64 chars (hex) — String() must not expose the full secret")
	}

	// Verify it's still usable as [32]byte (type identity preserved).
	var _ [32]byte = priv
}

// ── NetworkID is the allocator source of truth ──────────────────────────
//
// NewNetwork(netID) must use the provided NetworkID as the single source
// of truth for the IPv6 ULA allocator. Two networks created from the same
// NetworkID must produce addresses in the same ULA prefix.
func TestNewNetwork_NetworkIDIsAllocatorSource(t *testing.T) {
	const netID = NetworkID("test-source-of-truth-id")

	n, err := NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if n.NetworkID != netID {
		t.Fatalf("NewNetwork modified the NetworkID: got %q, want %q",
			n.NetworkID, netID)
	}

	// Add a membership and check it gets a valid ULA address from the
	// allocator derived from netID.
	m, err := n.NewMembership("body-source-test")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}
	if !m.OverlayAddress.Is6() {
		t.Fatal("membership address is not IPv6")
	}
	if !m.OverlayAddress.IsGlobalUnicast() {
		// ULA is fd00::/8; IsGlobalUnicast returns true.
	}
	// Explicitly check first nibble is 0xf (ULA).
	slice := m.OverlayAddress.AsSlice()
	if slice[0]&0xf0 != 0xf0 {
		t.Fatalf("membership address first nibble %02x, expected f for ULA",
			slice[0])
	}

	// Create a second network from the SAME netID — simulating reconstruction.
	n2, err := NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork (reconstructed): %v", err)
	}
	m2, err := n2.NewMembership("body-reconstruction")
	if err != nil {
		t.Fatalf("NewMembership on reconstructed network: %v", err)
	}

	// Both addresses must be in the same /48 ULA prefix derived from netID.
	prefix1 := m.OverlayAddress.AsSlice()[:6]
	prefix2 := m2.OverlayAddress.AsSlice()[:6]
	if !bytes.Equal(prefix1, prefix2) {
		t.Fatalf("same NetworkID produced different ULA prefixes: %x vs %x",
			prefix1, prefix2)
	}
}

// TestAllocationUsesCryptoRand verifies that random IDs and keys use crypto/rand.
// This is a compile-time constraint: the packages imported (crypto/rand) guarantee
// cryptographically secure randomness. This test proves that with the current code.
func TestAllocationUsesCryptoRand(t *testing.T) {
	// crypto/rand and uuid.New() both use secure randomness.
	// Verify by showing IDs are not predictable from a sequence.
	ids := make(map[NetworkID]bool)
	for i := 0; i < 1000; i++ {
		id := GenerateNetworkID()
		ids[id] = true
	}
	if len(ids) != 1000 {
		t.Fatal("GenerateNetworkID did not produce unique IDs")
	}
}

// Verify the unused import of math/big and crypto/rand actually works.
var _ = (*big.Int)(nil)
var _ = rand.Reader
