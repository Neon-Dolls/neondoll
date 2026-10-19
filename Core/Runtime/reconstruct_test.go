package runtime_test

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Runtime"
)

// TestReconstructRuntimeFromPortableState verifies that the WireGuard runtime
// is reconstructed from the portable network state: identity survives, and the
// running configuration contains exactly the authorized active memberships
// (revoked and pending are excluded).
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
