package network

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

// TestPortableNetwork_RoundTripPreservesMembershipIdentity is the focused
// M7.1 test: a Network's durable state must reconstruct valid network
// membership WITHOUT carrying live transport state. Live Endpoints set on
// memberships must NOT survive the boundary; membership identity, authority
// relationships, WireGuard public keys, overlay addresses, and statuses must.
func TestPortableNetwork_RoundTripPreservesMembershipIdentity(t *testing.T) {
	netID := NetworkID("test-portable-0001")
	n, err := NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	// Three memberships exercising distinct statuses and key states:
	//   - pending: no WireGuard public key yet
	//   - active: WireGuard public key set (paired Body)
	//   - revoked: was active, now permanently revoked
	_, err = n.NewMembership("body-pending")
	if err != nil {
		t.Fatalf("NewMembership(pending): %v", err)
	}

	activeBody, err := n.NewMembership("body-active")
	if err != nil {
		t.Fatalf("NewMembership(active): %v", err)
	}
	_, activePub, err := GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("GenerateWireGuardKeypair: %v", err)
	}
	if err := n.SetBodyWireGuardPublicKey(activeBody.PeerID, activePub); err != nil {
		t.Fatalf("SetBodyWireGuardPublicKey: %v", err)
	}
	if err := n.ActivateMembership(activeBody.PeerID); err != nil {
		t.Fatalf("ActivateMembership: %v", err)
	}

	revokedBody, err := n.NewMembership("body-revoked")
	if err != nil {
		t.Fatalf("NewMembership(revoked): %v", err)
	}
	_, revokedPub, err := GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("GenerateWireGuardKeypair: %v", err)
	}
	if err := n.SetBodyWireGuardPublicKey(revokedBody.PeerID, revokedPub); err != nil {
		t.Fatalf("SetBodyWireGuardPublicKey: %v", err)
	}
	if err := n.ActivateMembership(revokedBody.PeerID); err != nil {
		t.Fatalf("ActivateMembership: %v", err)
	}
	if err := n.RevokeMembership(revokedBody.PeerID); err != nil {
		t.Fatalf("RevokeMembership: %v", err)
	}

	// Simulate live transport state on memberships. This is exactly the
	// state a migrated Core must NOT carry: current source addresses of
	// established transport paths.
	n.GetMembership(activeBody.PeerID).Endpoint = "198.51.100.7:51820"
	n.GetMembership(revokedBody.PeerID).Endpoint = "[2001:db8::9]:51820"

	// Sanity: the live Network DOES have live transport state.
	if n.GetMembership(activeBody.PeerID).Endpoint == "" {
		t.Fatal("precondition failed: live membership should carry an Endpoint")
	}

	// Export the portable boundary.
	pn := n.Portable()
	if pn == nil {
		t.Fatal("Portable() returned nil")
	}

	// Reconstruct a fresh Network from portable state.
	recon, err := pn.ReconstructNetwork()
	if err != nil {
		t.Fatalf("ReconstructNetwork: %v", err)
	}

	// 1. Core identity must survive: peer id, keypair, overlay identity.
	if recon.NetworkID != netID {
		t.Errorf("reconstructed NetworkID = %q, want %q", recon.NetworkID, netID)
	}
	if recon.Core.PeerID != n.Core.PeerID {
		t.Errorf("reconstructed Core.PeerID = %q, want %q", recon.Core.PeerID, n.Core.PeerID)
	}
	if recon.Core.PrivateKey != n.Core.PrivateKey {
		t.Errorf("reconstructed Core.PrivateKey mismatch")
	}
	if recon.Core.PublicKey != n.Core.PublicKey {
		t.Errorf("reconstructed Core.PublicKey mismatch")
	}
	if recon.Core.OverlayAddress != n.Core.OverlayAddress {
		t.Errorf("reconstructed Core.OverlayAddress = %v, want %v", recon.Core.OverlayAddress, n.Core.OverlayAddress)
	}
	if !recon.Core.OverlayPrefix.IsValid() || recon.Core.OverlayPrefix != n.Core.OverlayPrefix {
		t.Errorf("reconstructed Core.OverlayPrefix = %v, want %v", recon.Core.OverlayPrefix, n.Core.OverlayPrefix)
	}

	// 2. Membership identity/authority/key/status must survive.
	if len(recon.Memberships) != len(n.Memberships) {
		t.Fatalf("reconstructed %d memberships, want %d", len(recon.Memberships), len(n.Memberships))
	}
	for peerID, want := range n.Memberships {
		got := recon.GetMembership(peerID)
		if got == nil {
			t.Errorf("reconstructed membership %q missing", peerID)
			continue
		}
		if got.BodyID != want.BodyID {
			t.Errorf("membership %q BodyID = %q, want %q", peerID, got.BodyID, want.BodyID)
		}
		if got.PeerID != want.PeerID {
			t.Errorf("membership %q PeerID = %q, want %q", peerID, got.PeerID, want.PeerID)
		}
		if got.Status != want.Status {
			t.Errorf("membership %q Status = %q, want %q", peerID, got.Status, want.Status)
		}
		if got.OverlayAddress != want.OverlayAddress {
			t.Errorf("membership %q OverlayAddress = %v, want %v", peerID, got.OverlayAddress, want.OverlayAddress)
		}
		wantKey, wantSet := want.WireGuardPublicKey, want.WireGuardPublicKey != nil
		gotKey, gotSet := got.WireGuardPublicKey, got.WireGuardPublicKey != nil
		if wantSet != gotSet {
			t.Errorf("membership %q WireGuardPublicKey set = %v, want %v", peerID, gotSet, wantSet)
		} else if wantSet && *gotKey != *wantKey {
			t.Errorf("membership %q WireGuardPublicKey mismatch", peerID)
		}
	}

	// 3. Live transport state must NOT survive: Endpoint must be empty on
	//    every reconstructed membership, even though it was set pre-export.
	for peerID, m := range recon.Memberships {
		if m.Endpoint != "" {
			t.Errorf("reconstructed membership %q carried Endpoint %q — live transport state leaked the portable boundary", peerID, m.Endpoint)
		}
	}

	// 4. The reconstructed Network must be functionally usable: the lazy
	//    allocator must derive the same overlay identity and future
	//    memberships must fall inside the derived prefix.
	if recon.Core.OverlayPrefix != NewIPv6Allocator(recon.NetworkID).Prefix() {
		t.Errorf("reconstructed prefix %v does not match allocator-derived prefix %v",
			recon.Core.OverlayPrefix, NewIPv6Allocator(recon.NetworkID).Prefix())
	}

	// The allocator's deterministic CoreAddress must agree with the
	// reconstructed Core overlay address (identity coherence).
	allocCore, err := NewIPv6Allocator(recon.NetworkID).CoreAddress()
	if err != nil {
		t.Fatalf("CoreAddress: %v", err)
	}
	if allocCore != recon.Core.OverlayAddress {
		t.Errorf("allocator CoreAddress %v != reconstructed Core.OverlayAddress %v",
			allocCore, recon.Core.OverlayAddress)
	}

	// A new membership allocated after reconstruction must be inside the
	// reconstructed network's prefix and must not collide.
	fresh, err := recon.NewMembership("body-after-reconstruction")
	if err != nil {
		t.Fatalf("NewMembership on reconstructed network: %v", err)
	}
	if !recon.Core.OverlayPrefix.Contains(fresh.OverlayAddress) {
		t.Errorf("fresh membership overlay %v outside reconstructed prefix %v",
			fresh.OverlayAddress, recon.Core.OverlayPrefix)
	}
	for _, m := range recon.Memberships {
		if m.PeerID != fresh.PeerID && m.OverlayAddress == fresh.OverlayAddress {
			t.Errorf("fresh membership address %v collides with %q", fresh.OverlayAddress, m.PeerID)
		}
	}
}

// TestPortableNetwork_JSONRoundTrip proves the portable boundary survives
// serialisation — the actual mechanism a migrating Core ships to a new host.
func TestPortableNetwork_JSONRoundTrip(t *testing.T) {
	n, err := NewNetwork(NetworkID("test-portable-json-0002"))
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	m, err := n.NewMembership("body-json")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}
	_, pub, err := GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("GenerateWireGuardKeypair: %v", err)
	}
	if err := n.SetBodyWireGuardPublicKey(m.PeerID, pub); err != nil {
		t.Fatalf("SetBodyWireGuardPublicKey: %v", err)
	}
	// Live transport state that must not survive serialisation.
	n.GetMembership(m.PeerID).Endpoint = "203.0.113.44:51820"

	pn := n.Portable()
	data, err := json.Marshal(pn)
	if err != nil {
		t.Fatalf("json.Marshal(portable): %v", err)
	}

	// The serialised form must not contain the live Endpoint value.
	if strings.Contains(string(data), "203.0.113.44") {
		t.Errorf("serialised portable state leaked live Endpoint %q", "203.0.113.44")
	}

	var decoded PortableNetwork
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(portable): %v", err)
	}

	recon, err := decoded.ReconstructNetwork()
	if err != nil {
		t.Fatalf("ReconstructNetwork after JSON round-trip: %v", err)
	}

	if recon.NetworkID != n.NetworkID {
		t.Errorf("reconstructed NetworkID = %q, want %q", recon.NetworkID, n.NetworkID)
	}
	if recon.Core.PrivateKey != n.Core.PrivateKey || recon.Core.PublicKey != n.Core.PublicKey {
		t.Errorf("reconstructed WireGuard keypair mismatch after JSON round-trip")
	}
	if recon.Core.OverlayAddress != n.Core.OverlayAddress {
		t.Errorf("reconstructed Core.OverlayAddress = %v, want %v", recon.Core.OverlayAddress, n.Core.OverlayAddress)
	}
	if !recon.Core.OverlayPrefix.IsValid() || recon.Core.OverlayPrefix != n.Core.OverlayPrefix {
		t.Errorf("reconstructed Core.OverlayPrefix = %v, want %v", recon.Core.OverlayPrefix, n.Core.OverlayPrefix)
	}

	got := recon.GetMembership(m.PeerID)
	if got == nil {
		t.Fatalf("reconstructed membership %q missing", m.PeerID)
	}
	if got.Status != m.Status || got.BodyID != m.BodyID {
		t.Errorf("reconstructed membership identity mismatch: got (%q, %q), want (%q, %q)",
			got.BodyID, got.Status, m.BodyID, m.Status)
	}
	if got.WireGuardPublicKey == nil || *got.WireGuardPublicKey != pub {
		t.Errorf("reconstructed membership WireGuardPublicKey mismatch")
	}
	if got.Endpoint != "" {
		t.Errorf("reconstructed membership carried Endpoint %q across JSON — transport state leaked", got.Endpoint)
	}
}

// TestPortableNetwork_RejectCorruptPortableState ensures reconstruction
// validates the imported boundary instead of blindly trusting it.
func TestPortableNetwork_RejectCorruptPortableState(t *testing.T) {
	build := func(t *testing.T) *PortableNetwork {
		t.Helper()
		netID := NetworkID("test-portable-corrupt-0003")
		n, err := NewNetwork(netID)
		if err != nil {
			t.Fatalf("NewNetwork: %v", err)
		}
		return n.Portable()
	}

	t.Run("unsupported version", func(t *testing.T) {
		pn := build(t)
		pn.Version = 999
		if _, err := pn.ReconstructNetwork(); err == nil {
			t.Fatalf("expected error for unsupported version, got nil")
		} else if !strings.Contains(err.Error(), "version") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("nil portable state", func(t *testing.T) {
		var pn *PortableNetwork
		if _, err := pn.ReconstructNetwork(); err == nil {
			t.Fatalf("expected error for nil portable state, got nil")
		}
	})

	t.Run("overlay address outside prefix", func(t *testing.T) {
		pn := build(t)
		// Assume a valid /64 ULA prefix: an address in a different prefix
		// must be rejected regardless of where the derived prefix lands.
		pn.Core.OverlayAddress = netip.AddrFrom16([16]byte{0xfd, 0x99, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
		if _, err := pn.ReconstructNetwork(); err == nil {
			t.Fatalf("expected error for overlay address outside derived prefix, got nil")
		}
	})

	t.Run("membership address outside prefix", func(t *testing.T) {
		pn := build(t)
		// Add a mismatch: an address that is not on the derived subnet.
		bn, err := NewNetwork(pn.NetworkID)
		if err != nil {
			t.Fatalf("NewNetwork: %v", err)
		}
		pm := bn.Portable()
		if len(pm.Memberships) != 0 {
			t.Fatal("unexpected memberships in fresh network")
		}
		// Inject a membership with an address outside the derived prefix.
		pm.Memberships = append(pm.Memberships, PortableMembership{
			BodyID:         "body-x",
			PeerID:         PeerID("peer-x"),
			OverlayAddress: netip.AddrFrom16([16]byte{0xfd, 0x99, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}),
			Status:         MembershipPending,
		})
		if _, err := pm.ReconstructNetwork(); err == nil {
			t.Fatalf("expected error for membership overlay address outside derived prefix, got nil")
		}
	})

	t.Run("duplicate membership peer id", func(t *testing.T) {
		pn := build(t)
		dup := PortableMembership{
			BodyID:         "body-dup",
			PeerID:         pn.Core.PeerID,
			OverlayAddress: pn.Core.OverlayAddress,
			Status:         MembershipPending,
		}
		pn.Memberships = append(pn.Memberships, dup)
		if _, err := pn.ReconstructNetwork(); err == nil {
			t.Fatalf("expected error for duplicate membership peer id, got nil")
		}
	})

	t.Run("missing body id", func(t *testing.T) {
		pn := build(t)
		pn.Memberships = append(pn.Memberships, PortableMembership{
			PeerID:         PeerID("peer-nobody"),
			OverlayAddress: pn.Core.OverlayPrefix.Masked().Addr(),
			Status:         MembershipPending,
		})
		if _, err := pn.ReconstructNetwork(); err == nil {
			t.Fatalf("expected error for missing body id, got nil")
		}
	})

	t.Run("invalid membership status", func(t *testing.T) {
		pn := build(t)
		pn.Memberships = append(pn.Memberships, PortableMembership{
			BodyID:         "body-badstatus",
			PeerID:         PeerID("peer-badstatus"),
			OverlayAddress: pn.Core.OverlayPrefix.Masked().Addr(),
			Status:         MembershipStatus("banana"),
		})
		if _, err := pn.ReconstructNetwork(); err == nil {
			t.Fatalf("expected error for invalid membership status, got nil")
		}
	})
}
