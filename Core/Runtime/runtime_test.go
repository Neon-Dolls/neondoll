package runtime_test

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Runtime"
)

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

// TestReconstructRuntimeFromPortableState exercises the full M7.1 -> M7.2
// chain: portable state is extracted from a source network, the network is
// reconstructed, and a live runtime is brought up around it. It verifies
// that identity and the full membership graph survive, and that the running
// WireGuard configuration contains exactly the authorized active membership —
// revoked and pending members are excluded.
func TestReconstructRuntimeFromPortableState(t *testing.T) {
	ctx := context.Background()
	src, activePub, revokedPub := testNetwork(t)
	srcCore := src.Core

	pn := src.Portable()
	if len(pn.Memberships) != 3 {
		t.Fatalf("portable memberships = %d, want 3", len(pn.Memberships))
	}

	recon, err := pn.ReconstructNetwork()
	if err != nil {
		t.Fatalf("reconstruct network: %v", err)
	}

	// M7.1 boundary: identity and the full membership graph survive.
	if recon.NetworkID != src.NetworkID {
		t.Errorf("network id = %q, want %q", recon.NetworkID, src.NetworkID)
	}
	if recon.Core.PeerID != srcCore.PeerID {
		t.Errorf("core peer id = %q, want %q", recon.Core.PeerID, srcCore.PeerID)
	}
	if recon.Core.PrivateKey != srcCore.PrivateKey {
		t.Errorf("core private key changed across reconstruction")
	}
	if recon.Core.PublicKey != srcCore.PublicKey {
		t.Errorf("core public key changed across reconstruction")
	}
	if recon.Core.OverlayAddress != srcCore.OverlayAddress {
		t.Errorf("overlay address = %v, want %v", recon.Core.OverlayAddress, srcCore.OverlayAddress)
	}
	if recon.Core.OverlayPrefix != srcCore.OverlayPrefix {
		t.Errorf("overlay prefix = %v, want %v", recon.Core.OverlayPrefix, srcCore.OverlayPrefix)
	}
	if len(recon.Memberships) != 3 {
		t.Fatalf("reconstructed memberships = %d, want 3", len(recon.Memberships))
	}

	rt, err := runtime.Reconstruct(ctx, recon, membershipStoreFrom(recon), testLogger())
	if err != nil {
		t.Fatalf("reconstruct runtime: %v", err)
	}
	defer rt.Stop()

	if rt.Network().NetworkID != src.NetworkID {
		t.Errorf("runtime network id = %q, want %q", rt.Network().NetworkID, src.NetworkID)
	}

	// The running WireGuard configuration contains exactly the authorized
	// active membership. Revoked and pending members are excluded.
	diag, err := rt.Manager().Diagnostics(ctx)
	if err != nil {
		t.Fatalf("diagnostics: %v", err)
	}
	got := make(map[string]bool)
	for _, p := range diag.Peers {
		got[p.PublicKey] = true
	}
	if len(got) != 1 {
		t.Fatalf("wg peers = %d, want 1: %v", len(got), got)
	}
	if !got[hex.EncodeToString(activePub[:])] {
		t.Errorf("active membership missing from wg config (%v)", got)
	}
	if got[hex.EncodeToString(revokedPub[:])] {
		t.Errorf("revoked membership present in wg config (%v)", got)
	}
}

// TestRuntimeLifecycle verifies clean lifecycle management: RefreshPeers
// works while running, Stop completes and closes Done, Stop is idempotent,
// and lifecycle methods fail cleanly after shutdown.
func TestRuntimeLifecycle(t *testing.T) {
	ctx := context.Background()
	src, _, _ := testNetwork(t)
	recon, err := src.Portable().ReconstructNetwork()
	if err != nil {
		t.Fatalf("reconstruct network: %v", err)
	}

	rt, err := runtime.Reconstruct(ctx, recon, membershipStoreFrom(recon), testLogger())
	if err != nil {
		t.Fatalf("reconstruct runtime: %v", err)
	}

	// RefreshPeers works while the runtime is running.
	if err := rt.RefreshPeers(ctx); err != nil {
		t.Fatalf("refresh peers: %v", err)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- rt.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stop hung")
	}

	select {
	case <-rt.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}

	// Stop is idempotent.
	if err := rt.Stop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}

	// Lifecycle methods fail cleanly after stop.
	if _, err := rt.Manager().Diagnostics(ctx); err == nil {
		t.Error("Diagnostics succeeded after stop, want error")
	}
	if err := rt.RefreshPeers(ctx); err == nil {
		t.Error("RefreshPeers succeeded after stop, want error")
	}
}

// TestReconstructValidation verifies Reconstruct rejects invalid inputs
// before touching the WireGuard runtime.
func TestReconstructValidation(t *testing.T) {
	ctx := context.Background()
	logger := testLogger()

	if _, err := runtime.Reconstruct(ctx, nil, nil, logger); err == nil {
		t.Error("Reconstruct(nil network) succeeded, want error")
	}

	// A network without core identity is not reconstructible.
	empty := &network.Network{}
	if _, err := runtime.Reconstruct(ctx, empty, nil, logger); err == nil {
		t.Error("Reconstruct(no core identity) succeeded, want error")
	}

	// The membership store is required.
	src, _, _ := testNetwork(t)
	recon, err := src.Portable().ReconstructNetwork()
	if err != nil {
		t.Fatalf("reconstruct network: %v", err)
	}
	if _, err := runtime.Reconstruct(ctx, recon, nil, logger); err == nil {
		t.Error("Reconstruct(nil store) succeeded, want error")
	}
}
