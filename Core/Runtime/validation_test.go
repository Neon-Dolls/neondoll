package runtime_test

import (
	"context"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Network"
	"github.com/Neon-Dolls/neondoll/Core/Runtime"
)

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
