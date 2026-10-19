package runtime_test

import (
	"context"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Persistence"
	"github.com/Neon-Dolls/neondoll/Core/Runtime"
)

// identitySnapshot is a plain copy of the durable identity state that must
// survive a restart. Defined locally because there is no exported snapshot
// type in the network package — callers are expected to hold the *Network
// itself, and a restart test deliberately copies only the durable fields.
type identitySnapshot struct {
	networkID      network.NetworkID
	core           network.CoreIdentity
	membershipKeys []string
}

// snapshotIdentity copies the durable fields out of a loaded network.
func snapshotIdentity(nw *network.Network) identitySnapshot {
	s := identitySnapshot{
		networkID: nw.NetworkID,
		core:      nw.Core,
	}
	for peerID := range nw.Memberships {
		s.membershipKeys = append(s.membershipKeys, string(peerID))
	}
	return s
}

// mustOpenNetworkStore opens the real SQLite persistence store at dbPath and
// returns it as a NetworkStore plus a cleanup func that closes it.
func mustOpenNetworkStore(t *testing.T, dbPath string) (network.NetworkStore, func()) {
	t.Helper()
	store, err := persistence.NewStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ns, ok := store.(network.NetworkStore)
	if !ok {
		store.Close()
		t.Fatalf("store %T does not implement NetworkStore", store)
	}
	return ns, func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}
}

// mustSaveNetwork persists a network and fails the test on error.
func mustSaveNetwork(t *testing.T, ctx context.Context, ns network.NetworkStore, nw *network.Network) {
	t.Helper()
	if err := ns.SaveNetwork(ctx, nw); err != nil {
		t.Fatalf("save network: %v", err)
	}
}

// mustNewNetwork creates a fresh network with a stable network ID.
func mustNewNetwork(t *testing.T) *network.Network {
	t.Helper()
	n, err := network.NewNetwork(network.GenerateNetworkID())
	if err != nil {
		t.Fatalf("new network: %v", err)
	}
	return n
}

// mustEnrollBody creates, activates, and persists a single membership for the
// given body, returning the membership.
func mustEnrollBody(t *testing.T, ctx context.Context, ns network.NetworkStore, nw *network.Network, bodyID string) *network.Membership {
	t.Helper()
	m, err := nw.NewMembership(bodyID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	_, pub, err := network.GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	m.WireGuardPublicKey = &pub
	if err := nw.ActivateMembership(m.PeerID); err != nil {
		t.Fatalf("activate membership: %v", err)
	}
	if err := ns.SaveMembership(ctx, m); err != nil {
		t.Fatalf("save membership: %v", err)
	}
	return m
}

// assertRuntimePeers checks that the WireGuard runtime has exactly the given
// set of enabled peer public keys (hex-encoded, order-insensitive).
func assertRuntimePeers(t *testing.T, ctx context.Context, rt *runtime.Runtime, want []string) {
	t.Helper()
	diag, err := rt.Diagnostics(ctx)
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	var got []string
	for _, p := range diag.Peers {
		got = append(got, p.PublicKey)
	}
	if !sameStringSet(got, want) {
		t.Errorf("peer config mismatch\n got: %v\nwant: %v", got, want)
	}
}

// sameStringSet compares two string slices as unordered sets.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ma := make(map[string]int, len(a))
	for _, s := range a {
		ma[s]++
	}
	for _, s := range b {
		ma[s]--
		if ma[s] < 0 {
			return false
		}
	}
	for _, cnt := range ma {
		if cnt != 0 {
			return false
		}
	}
	return true
}

// assertSameSnapshot fails unless the loader-derived snapshot is identical to
// the reference snapshot captured before restart.
func assertSameSnapshot(t *testing.T, label string, got, want identitySnapshot) {
	t.Helper()
	if got.networkID != want.networkID {
		t.Errorf("%s: network ID changed\n got: %q\nwant: %q", label, got.networkID, want.networkID)
	}
	if !reflect.DeepEqual(got.core, want.core) {
		t.Errorf("%s: core identity changed\n got: %+v\nwant: %+v", label, got.core, want.core)
	}
	if !sameStringSet(got.membershipKeys, want.membershipKeys) {
		t.Errorf("%s: membership set changed\n got: %v\nwant: %v", label, got.membershipKeys, want.membershipKeys)
	}
}

// hexPub renders a WireGuard public key the same way Manager.Diagnostics does.
func hexPub(pub network.WireGuardPublicKey) string {
	return hex.EncodeToString(pub[:])
}
