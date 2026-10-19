package runtime_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Network"
)

// testLogger returns a discard logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testNetwork builds a source network with three memberships in the states
// that matter for reconstruction:
//
//   - pending: invited, not yet authorized (must NOT appear in WG config);
//   - active: authorized with a WireGuard public key (must appear);
//   - revoked: previously authorized, now revoked (must NOT appear).
func testNetwork(t *testing.T) (*network.Network, network.WireGuardPublicKey, network.WireGuardPublicKey) {
	t.Helper()
	n, err := network.NewNetwork(network.GenerateNetworkID())
	if err != nil {
		t.Fatalf("new network: %v", err)
	}

	if _, err := n.NewMembership("body-pending"); err != nil {
		t.Fatalf("membership pending: %v", err)
	}

	active, err := n.NewMembership("body-active")
	if err != nil {
		t.Fatalf("membership active: %v", err)
	}
	_, activePub, err := network.GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("keypair active: %v", err)
	}
	if err := n.SetBodyWireGuardPublicKey(active.PeerID, activePub); err != nil {
		t.Fatalf("set active pubkey: %v", err)
	}
	if err := n.ActivateMembership(active.PeerID); err != nil {
		t.Fatalf("activate active: %v", err)
	}

	revoked, err := n.NewMembership("body-revoked")
	if err != nil {
		t.Fatalf("membership revoked: %v", err)
	}
	_, revokedPub, err := network.GenerateWireGuardKeypair()
	if err != nil {
		t.Fatalf("keypair revoked: %v", err)
	}
	if err := n.SetBodyWireGuardPublicKey(revoked.PeerID, revokedPub); err != nil {
		t.Fatalf("set revoked pubkey: %v", err)
	}
	if err := n.ActivateMembership(revoked.PeerID); err != nil {
		t.Fatalf("activate revoked: %v", err)
	}
	if err := n.RevokeMembership(revoked.PeerID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	return n, activePub, revokedPub
}

// membershipStore is the runtime's membership window, mirroring the
// production path: after migration the reconstructed memberships are
// persisted, and the runtime reads peers from the store.
type membershipStore struct {
	memberships []*network.Membership
}

func (s *membershipStore) ListMemberships(context.Context) ([]*network.Membership, error) {
	out := make([]*network.Membership, len(s.memberships))
	copy(out, s.memberships)
	return out, nil
}

func membershipStoreFrom(nw *network.Network) *membershipStore {
	return &membershipStore{memberships: nw.ListMemberships()}
}
