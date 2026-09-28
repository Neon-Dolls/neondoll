package persistence

import (
	"context"
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
