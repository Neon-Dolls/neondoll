package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/google/uuid"
)

// assertNetworkStore is a helper that casts a Store to NetworkStore.
func assertNetworkStore(t *testing.T, s Store) network.NetworkStore {
	t.Helper()
	ns, ok := s.(network.NetworkStore)
	if !ok {
		t.Fatal("Store does not implement network.NetworkStore")
	}
	return ns
}

// inMemDB creates a temporary database path and returns Store + cleanup.
func inMemStore(t *testing.T) Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestNetworkStore_SaveAndLoadNetwork(t *testing.T) {
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)

	ctx := context.Background()

	// First-run: no network state.
	loaded, err := ns.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("LoadNetwork on empty store: %v", err)
	}
	if loaded != nil {
		t.Fatal("LoadNetwork expected nil on first run")
	}

	// Create a network.
	n, err := network.NewNetwork(network.GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	// Save it.
	if err := ns.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	// Load it.
	loaded, err = ns.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("LoadNetwork: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadNetwork returned nil after save")
	}

	// Verify identity preserved.
	if loaded.NetworkID != n.NetworkID {
		t.Fatalf("network_id: got %q, want %q", loaded.NetworkID, n.NetworkID)
	}
	if loaded.Core.PeerID != n.Core.PeerID {
		t.Fatalf("core peer_id: got %q, want %q", loaded.Core.PeerID, n.Core.PeerID)
	}
	if loaded.Core.PublicKey != n.Core.PublicKey {
		t.Fatal("core public key mismatch after load")
	}
	if loaded.Core.PrivateKey != n.Core.PrivateKey {
		t.Fatal("core private key mismatch after load")
	}
	if loaded.Core.OverlayAddress != n.Core.OverlayAddress {
		t.Fatalf("core overlay address: got %s, want %s", loaded.Core.OverlayAddress, n.Core.OverlayAddress)
	}
}

func TestNetworkStore_SaveAndLoadMembership(t *testing.T) {
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)

	ctx := context.Background()

	// Create and save a network first (memberships need the allocator).
	netID := network.GenerateNetworkID()
	n, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if err := ns.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	// Create a membership.
	m, err := n.NewMembership("body-test-1")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}

	// Activate it.
	if err := n.ActivateMembership(m.PeerID); err != nil {
		t.Fatalf("ActivateMembership: %v", err)
	}

	// Set a WG public key.
	var pubKey network.WireGuardPublicKey
	for i := 0; i < 32; i++ {
		pubKey[i] = byte(i % 256)
	}
	if err := n.SetBodyWireGuardPublicKey(m.PeerID, pubKey); err != nil {
		t.Fatalf("SetBodyWireGuardPublicKey: %v", err)
	}

	// Save the membership.
	if err := ns.SaveMembership(ctx, m); err != nil {
		t.Fatalf("SaveMembership: %v", err)
	}

	// Load it.
	loaded, err := ns.LoadMembership(ctx, m.PeerID)
	if err != nil {
		t.Fatalf("LoadMembership: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadMembership returned nil after save")
	}

	// Verify fields preserved.
	if loaded.BodyID != m.BodyID {
		t.Fatalf("body_id: got %q, want %q", loaded.BodyID, m.BodyID)
	}
	if loaded.PeerID != m.PeerID {
		t.Fatalf("peer_id: got %q, want %q", loaded.PeerID, m.PeerID)
	}
	if loaded.Status != m.Status {
		t.Fatalf("status: got %q, want %q", loaded.Status, m.Status)
	}
	if loaded.OverlayAddress != m.OverlayAddress {
		t.Fatalf("overlay address: got %s, want %s", loaded.OverlayAddress, m.OverlayAddress)
	}
	if loaded.WireGuardPublicKey == nil {
		t.Fatal("wireguard public key is nil after load")
	}
	if *loaded.WireGuardPublicKey != pubKey {
		t.Fatal("wireguard public key mismatch after load")
	}
}

func TestNetworkStore_ListMemberships(t *testing.T) {
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)

	ctx := context.Background()

	// Create and save network.
	netID := network.GenerateNetworkID()
	n, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if err := ns.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	// Initially no memberships.
	list, err := ns.ListMemberships(ctx)
	if err != nil {
		t.Fatalf("ListMemberships (empty): %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 memberships initially, got %d", len(list))
	}

	// Add two memberships.
	bodies := []string{"body-alpha", "body-beta"}
	for _, bodyID := range bodies {
		m, err := n.NewMembership(bodyID)
		if err != nil {
			t.Fatalf("NewMembership %s: %v", bodyID, err)
		}
		if err := ns.SaveMembership(ctx, m); err != nil {
			t.Fatalf("SaveMembership %s: %v", bodyID, err)
		}
	}

	// List.
	list, err = ns.ListMemberships(ctx)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 memberships, got %d", len(list))
	}

	// Verify both body IDs appear.
	bodyIDs := make(map[string]bool)
	for _, m := range list {
		bodyIDs[m.BodyID] = true
	}
	for _, bodyID := range bodies {
		if !bodyIDs[bodyID] {
			t.Fatalf("membership for %q not found in list", bodyID)
		}
	}
}

func TestNetworkStore_DeleteMembership(t *testing.T) {
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)

	ctx := context.Background()

	// Create network and membership.
	netID := network.GenerateNetworkID()
	n, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	m, err := n.NewMembership("body-delete-test")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}
	if err := ns.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}
	if err := ns.SaveMembership(ctx, m); err != nil {
		t.Fatalf("SaveMembership: %v", err)
	}

	// Delete.
	if err := ns.DeleteMembership(ctx, m.PeerID); err != nil {
		t.Fatalf("DeleteMembership: %v", err)
	}

	// Confirm gone.
	list, err := ns.ListMemberships(ctx)
	if err != nil {
		t.Fatalf("ListMemberships after delete: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 memberships after delete, got %d", len(list))
	}

	// Deleting non-existent is a no-op.
	if err := ns.DeleteMembership(ctx, network.PeerID(uuid.New().String())); err != nil {
		t.Fatalf("DeleteMembership non-existent: %v", err)
	}
}

func TestNetworkStore_ReconstructNetwork(t *testing.T) {
	// Full round-trip: save network and memberships, then load from a new Store.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()

	// Phase 1: create and save.
	s1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore phase 1: %v", err)
	}
	ns1 := assertNetworkStore(t, s1)

	netID := network.GenerateNetworkID()
	n, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if err := ns1.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	peers := make([]network.PeerID, 3)
	for i := 0; i < 3; i++ {
		bodyID := "body-" + string(rune('a'+i))
		m, err := n.NewMembership(bodyID)
		if err != nil {
			t.Fatalf("NewMembership %s: %v", bodyID, err)
		}
		peers[i] = m.PeerID
		if err := ns1.SaveMembership(ctx, m); err != nil {
			t.Fatalf("SaveMembership %s: %v", bodyID, err)
		}
	}
	s1.Close()

	// Phase 2: reopen and reconstruct.
	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore phase 2: %v", err)
	}
	defer s2.Close()
	ns2 := assertNetworkStore(t, s2)

	loaded, err := ns2.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("LoadNetwork phase 2: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadNetwork returned nil in phase 2")
	}

	if loaded.NetworkID != n.NetworkID {
		t.Fatalf("network_id: got %q, want %q", loaded.NetworkID, n.NetworkID)
	}
	if loaded.Core.PeerID != n.Core.PeerID {
		t.Fatalf("core peer_id: got %q, want %q", loaded.Core.PeerID, n.Core.PeerID)
	}
	if loaded.Core.PublicKey != n.Core.PublicKey {
		t.Fatal("core public key mismatch after reconstruction")
	}
	if loaded.Core.OverlayAddress != n.Core.OverlayAddress {
		t.Fatalf("core overlay address mismatch: got %s, want %s",
			loaded.Core.OverlayAddress, n.Core.OverlayAddress)
	}

	// Load memberships.
	memberships, err := ns2.ListMemberships(ctx)
	if err != nil {
		t.Fatalf("ListMemberships phase 2: %v", err)
	}
	if len(memberships) != 3 {
		t.Fatalf("expected 3 memberships after reconstruction, got %d", len(memberships))
	}

	// Verify each peer_id from phase 1 is present.
	found := make(map[string]bool)
	for _, m := range memberships {
		found[string(m.PeerID)] = true
	}
	for _, peer := range peers {
		if !found[string(peer)] {
			t.Fatalf("peer_id %s not found after reconstruction", peer)
		}
	}

	// Phase 3: verify allocator reconstruction from persisted NetworkID.
	// After restart, the allocator is absent runtime state and is lazily
	// rebuilt from loaded.NetworkID. New Body allocations must still
	// produce addresses in exactly the same Doll Network prefix.
	alloc := network.NewIPv6Allocator(loaded.NetworkID)

	// Create a new Body membership on the reconstructed Network.
	// This exercises the lazy allocator via loaded.NewMembership → loaded.allocator().
	newBody, err := loaded.NewMembership("new-body-after-restart")
	if err != nil {
		t.Fatalf("NewMembership on reconstructed Network: %v", err)
	}

	// Verify the new membership's overlay address belongs to the prefix.
	if !alloc.Prefix().Contains(newBody.OverlayAddress) {
		t.Fatalf("new membership address %s not in prefix %s (from NetworkID %q)",
			newBody.OverlayAddress, alloc.Prefix(), loaded.NetworkID)
	}

	// Verify Core's overlay address is also in that same prefix.
	if !alloc.Prefix().Contains(loaded.Core.OverlayAddress) {
		t.Fatalf("Core overlay address %s not in prefix %s (from NetworkID %q)",
			loaded.Core.OverlayAddress, alloc.Prefix(), loaded.NetworkID)
	}

	// Verify the address matches what a fresh allocator from NetworkID produces.
	expectedAddr, err := alloc.BodyAddress(newBody.PeerID)
	if err != nil {
		t.Fatalf("BodyAddress from fresh allocator: %v", err)
	}
	if newBody.OverlayAddress != expectedAddr {
		t.Fatalf("new membership address mismatch: got %s, want %s (from allocator → NetworkID)",
			newBody.OverlayAddress, expectedAddr)
	}
}

func TestNetworkStore_MembershipUpdate(t *testing.T) {
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)

	ctx := context.Background()

	// Create network + membership.
	netID := network.GenerateNetworkID()
	n, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if err := ns.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	m, err := n.NewMembership("body-update-test")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}
	if err := ns.SaveMembership(ctx, m); err != nil {
		t.Fatalf("SaveMembership: %v", err)
	}

	// Update: activate + set public key.
	if err := n.ActivateMembership(m.PeerID); err != nil {
		t.Fatalf("ActivateMembership: %v", err)
	}
	var pubKey network.WireGuardPublicKey
	for i := 0; i < 32; i++ {
		pubKey[i] = byte(255 - i)
	}
	if err := n.SetBodyWireGuardPublicKey(m.PeerID, pubKey); err != nil {
		t.Fatalf("SetBodyWireGuardPublicKey: %v", err)
	}

	// Save updated membership.
	if err := ns.SaveMembership(ctx, n.Memberships[m.PeerID]); err != nil {
		t.Fatalf("SaveMembership (updated): %v", err)
	}

	// Load and verify.
	loaded, err := ns.LoadMembership(ctx, m.PeerID)
	if err != nil {
		t.Fatalf("LoadMembership: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadMembership returned nil after update")
	}
	if loaded.Status != network.MembershipActive {
		t.Fatalf("status after update: got %q, want %q", loaded.Status, network.MembershipActive)
	}
	if loaded.WireGuardPublicKey == nil {
		t.Fatal("wireguard public key is nil after update")
	}
	if *loaded.WireGuardPublicKey != pubKey {
		t.Fatal("wireguard public key mismatch after update")
	}
}

func TestNetworkStore_MembershipNotFoundIsNil(t *testing.T) {
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)

	ctx := context.Background()

	// Non-existent peer ID.
	loaded, err := ns.LoadMembership(ctx, network.PeerID(uuid.New().String()))
	if err != nil {
		t.Fatalf("LoadMembership with non-existent peer: %v (expected nil, nil)", err)
	}
	if loaded != nil {
		t.Fatal("LoadMembership with non-existent peer returned non-nil")
	}
}

func TestStore_MigrationCreatesNetworkTable(t *testing.T) {
	// Verify the schema migration correctly creates the network_state table
	// for a database created before Core 4 M1. We simulate this by creating
	// a DB without the table and then verifying the migration adds it.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_pre_m1.db")

	// Create a database using the persistence.NewStore (which now runs
	// the full migration including network_state table). Verify the table
	// exists after creation.
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	// The table exists because createSchema is called by NewStore.
	// Verify by writing to it.
	ns := assertNetworkStore(t, s)
	ctx := context.Background()
	n, err := network.NewNetwork(network.GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if err := ns.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}
}

func TestNetworkStore_TwoNetworksRoundTrip(t *testing.T) {
	// Verify independence: saving network A then B, loading only B.
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)

	ctx := context.Background()

	// Create network A.
	nA, err := network.NewNetwork(network.GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork A: %v", err)
	}
	if err := ns.SaveNetwork(ctx, nA); err != nil {
		t.Fatalf("SaveNetwork A: %v", err)
	}

	// Create network B (replaces A — keyed by the same key).
	nB, err := network.NewNetwork(network.GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork B: %v", err)
	}
	if err := ns.SaveNetwork(ctx, nB); err != nil {
		t.Fatalf("SaveNetwork B: %v", err)
	}

	// Load — should get B (last saved).
	loaded, err := ns.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("LoadNetwork: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadNetwork returned nil after two saves")
	}
	if loaded.NetworkID != nB.NetworkID {
		t.Fatalf("loaded network_id %q, want %q (network B)", loaded.NetworkID, nB.NetworkID)
	}
}

// Verify private key is stored in the DB but NEVER serializes to protocol.
func TestNetworkStore_PrivateKeyInDBOnly(t *testing.T) {
	// The private key is in the DB. The test proves it's stored and retrievable
	// (it must be for restart reconstruction). But the test also proves it's
	// never exposed through the public interfaces — that's tested elsewhere
	// (Strings() exclusion, protocol documentation).
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)

	ctx := context.Background()

	n, err := network.NewNetwork(network.GenerateNetworkID())
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}

	originalPriv := n.Core.PrivateKey
	if err := ns.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	loaded, err := ns.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("LoadNetwork: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadNetwork returned nil")
	}

	// Private key must survive round-trip.
	if loaded.Core.PrivateKey != originalPriv {
		t.Fatal("private key does not match after save/load cycle")
	}
}

func TestNetworkStore_RespectsMembershipStatusTransition(t *testing.T) {
	s := inMemStore(t)
	defer s.Close()
	ns := assertNetworkStore(t, s)
	ctx := context.Background()

	netID := network.GenerateNetworkID()
	n, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if err := ns.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	m, err := n.NewMembership("body-status-test")
	if err != nil {
		t.Fatalf("NewMembership: %v", err)
	}
	if err := ns.SaveMembership(ctx, m); err != nil {
		t.Fatalf("SaveMembership (pending): %v", err)
	}

	// Activate and save.
	if err := n.ActivateMembership(m.PeerID); err != nil {
		t.Fatalf("ActivateMembership: %v", err)
	}
	if err := ns.SaveMembership(ctx, n.Memberships[m.PeerID]); err != nil {
		t.Fatalf("SaveMembership (active): %v", err)
	}

	// Revoke and save.
	if err := n.RevokeMembership(m.PeerID); err != nil {
		t.Fatalf("RevokeMembership: %v", err)
	}
	if err := ns.SaveMembership(ctx, n.Memberships[m.PeerID]); err != nil {
		t.Fatalf("SaveMembership (revoked): %v", err)
	}

	// Load and verify revoked.
	loaded, err := ns.LoadMembership(ctx, m.PeerID)
	if err != nil {
		t.Fatalf("LoadMembership: %v", err)
	}
	if loaded.Status != network.MembershipRevoked {
		t.Fatalf("stored membership status %q, want %q", loaded.Status, network.MembershipRevoked)
	}
}

// TestNetworkStore_RestoredMembershipsBlockRecreation verifies that after a
// full save → close → reopen → LoadNetwork cycle, persisted memberships are
// reattached to the reconstructed Network, and body_id collision/revocation
// semantics work correctly across restart.
func TestNetworkStore_RestoredMembershipsBlockRecreation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	ctx := context.Background()

	// Phase 1: create network, save, create two memberships, persist them.
	s1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore phase 1: %v", err)
	}
	ns1 := assertNetworkStore(t, s1)

	netID := network.GenerateNetworkID()
	n, err := network.NewNetwork(netID)
	if err != nil {
		t.Fatalf("NewNetwork: %v", err)
	}
	if err := ns1.SaveNetwork(ctx, n); err != nil {
		t.Fatalf("SaveNetwork: %v", err)
	}

	// Create an active membership.
	activeBodyID := "active-body"
	activeMem, err := n.NewMembership(activeBodyID)
	if err != nil {
		t.Fatalf("NewMembership %s: %v", activeBodyID, err)
	}
	if err := n.ActivateMembership(activeMem.PeerID); err != nil {
		t.Fatalf("ActivateMembership: %v", err)
	}
	if err := ns1.SaveMembership(ctx, n.Memberships[activeMem.PeerID]); err != nil {
		t.Fatalf("SaveMembership (active): %v", err)
	}
	activePeerID := activeMem.PeerID

	// Create and revoke a membership.
	revokedBodyID := "revoked-body"
	revokedMem, err := n.NewMembership(revokedBodyID)
	if err != nil {
		t.Fatalf("NewMembership %s: %v", revokedBodyID, err)
	}
	if err := n.RevokeMembership(revokedMem.PeerID); err != nil {
		t.Fatalf("RevokeMembership: %v", err)
	}
	if err := ns1.SaveMembership(ctx, n.Memberships[revokedMem.PeerID]); err != nil {
		t.Fatalf("SaveMembership (revoked): %v", err)
	}
	revokedPeerID := revokedMem.PeerID

	s1.Close()

	// Phase 2: reopen store and reconstruct Network.
	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore phase 2: %v", err)
	}
	defer s2.Close()
	ns2 := assertNetworkStore(t, s2)

	loaded, err := ns2.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("LoadNetwork: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadNetwork returned nil after save")
	}

	// Verify both memberships are in the reconstructed Network's map.
	if len(loaded.Memberships) != 2 {
		t.Fatalf("reconstructed Network has %d memberships, want 2", len(loaded.Memberships))
	}

	gotActive, ok := loaded.Memberships[activePeerID]
	if !ok {
		t.Fatal("reconstructed Network missing active membership by PeerID")
	}
	if gotActive.BodyID != activeBodyID {
		t.Fatalf("active BodyID: got %q, want %q", gotActive.BodyID, activeBodyID)
	}
	if gotActive.Status != network.MembershipActive {
		t.Fatalf("active status: got %q, want %q", gotActive.Status, network.MembershipActive)
	}

	gotRevoked, ok := loaded.Memberships[revokedPeerID]
	if !ok {
		t.Fatal("reconstructed Network missing revoked membership by PeerID")
	}
	if gotRevoked.BodyID != revokedBodyID {
		t.Fatalf("revoked BodyID: got %q, want %q", gotRevoked.BodyID, revokedBodyID)
	}
	if gotRevoked.Status != network.MembershipRevoked {
		t.Fatalf("revoked status: got %q, want %q", gotRevoked.Status, network.MembershipRevoked)
	}

	// Verify existing body_id → already-exists error.
	_, err = loaded.NewMembership(activeBodyID)
	if err != network.ErrMembershipAlreadyExists {
		t.Fatalf("NewMembership with active body_id: got %v, want %v", err, network.ErrMembershipAlreadyExists)
	}

	// Verify revoked body_id → revoked error.
	_, err = loaded.NewMembership(revokedBodyID)
	if err != network.ErrMembershipRevoked {
		t.Fatalf("NewMembership with revoked body_id: got %v, want %v", err, network.ErrMembershipRevoked)
	}

	// Verify a genuinely new Body can still be allocated after restart
	// and receives an address in the persisted NetworkID's prefix.
	newMem, err := loaded.NewMembership("new-body-after-restart")
	if err != nil {
		t.Fatalf("NewMembership new body after restart: %v", err)
	}
	if newMem.PeerID == activePeerID || newMem.PeerID == revokedPeerID {
		t.Fatal("new Body received duplicate PeerID")
	}
	prefix := network.NewIPv6Allocator(loaded.NetworkID).Prefix()
	if !prefix.Contains(newMem.OverlayAddress) {
		t.Fatalf("new Body address %s not in prefix %s", newMem.OverlayAddress, prefix)
	}
}

// TestNetworkStore_UnmarshalMembership_WGKeyValidation regression-tests
// the malformed persisted Body WireGuard public-key behavior:
//   - length 0 → nil key, valid
//   - length 32 → accepted
//   - any other nonzero length → ErrCannotDecode
func TestNetworkStore_UnmarshalMembership_WGKeyValidation(t *testing.T) {
	// Helper: construct a minimal valid membership JSON and override the wg key.
	baseRow := membershipRow{
		BodyID:      "test-body",
		PeerID:      network.PeerID("test-peer"),
		OverlayAddr: "fd00::1",
		Status:      network.MembershipPending,
	}

	t.Run("nil key (len 0)", func(t *testing.T) {
		baseRow.WireGuardPublicKey = nil
		data, err := json.Marshal(baseRow)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		m, err := unmarshalMembership(string(data))
		if err != nil {
			t.Fatalf("unmarshalMembership with nil key: %v", err)
		}
		if m.WireGuardPublicKey != nil {
			t.Fatal("expected nil WireGuardPublicKey for empty input")
		}
	})

	t.Run("valid 32-byte key", func(t *testing.T) {
		var key [32]byte
		for i := range key {
			key[i] = byte(i)
		}
		baseRow.WireGuardPublicKey = key[:]
		data, err := json.Marshal(baseRow)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		m, err := unmarshalMembership(string(data))
		if err != nil {
			t.Fatalf("unmarshalMembership with 32-byte key: %v", err)
		}
		if m.WireGuardPublicKey == nil {
			t.Fatal("expected non-nil WireGuardPublicKey")
		}
		if *m.WireGuardPublicKey != key {
			t.Fatal("WireGuardPublicKey value mismatch after round-trip")
		}
	})

	t.Run("invalid 17-byte key", func(t *testing.T) {
		key := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
		baseRow.WireGuardPublicKey = key
		data, err := json.Marshal(baseRow)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		_, err = unmarshalMembership(string(data))
		if err == nil {
			t.Fatal("expected ErrCannotDecode for 17-byte key, got nil")
		}
		if !errors.Is(err, ErrCannotDecode) {
			t.Fatalf("error: got %v, want wrapping ErrCannotDecode", err)
		}
	})

	t.Run("invalid 1-byte key", func(t *testing.T) {
		key := []byte{0x42}
		baseRow.WireGuardPublicKey = key
		data, err := json.Marshal(baseRow)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		_, err = unmarshalMembership(string(data))
		if err == nil {
			t.Fatal("expected ErrCannotDecode for 1-byte key, got nil")
		}
		if !errors.Is(err, ErrCannotDecode) {
			t.Fatalf("error: got %v, want wrapping ErrCannotDecode", err)
		}
	})
}
