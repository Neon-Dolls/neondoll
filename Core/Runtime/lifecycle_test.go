package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Runtime"
)

// TestRuntimeLifecycle verifies clean lifecycle management: RefreshPeers works
// while running, Stop completes and closes Done, Stop is idempotent, and
// lifecycle methods fail cleanly after shutdown.
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
