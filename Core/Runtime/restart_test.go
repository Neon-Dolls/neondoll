package runtime_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Runtime"
)

// TestRestartReusesPersistedState is the M7.3 startup integration test. It
// proves the full daemon boot path against the real SQLite persistence store:
//
//  1. Core starts with an enrolled Body.
//  2. Core stops.
//  3. Core restarts using the same persistent storage.
//  4. Network identity and authorized peer configuration remain unchanged.
//  5. No new pairing is required (the persisted membership is reused,
//     not recreated).
//
// It deliberately reopens the same DB file across two boots instead of using
// an in-memory store, so the "survives a restart" property is real.
func TestRestartReusesPersistedState(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "core.db")

	// ---- Boot 1: enroll one Body and start the runtime. ----
	ns1, close1 := mustOpenNetworkStore(t, dbPath)
	nw1, err := ns1.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("load network (boot 1): %v", err)
	}
	if nw1 != nil {
		t.Fatalf("expected empty store on first boot, got network %+v", nw1)
	}
	nw1 = mustNewNetwork(t)
	mustSaveNetwork(t, ctx, ns1, nw1)
	m := mustEnrollBody(t, ctx, ns1, nw1, "body-1")

	rt1, err := runtime.Reconstruct(ctx, nw1, ns1, testLogger())
	if err != nil {
		t.Fatalf("reconstruct (boot 1): %v", err)
	}
	assertRuntimePeers(t, ctx, rt1, []string{hexPub(*m.WireGuardPublicKey)})

	before := snapshotIdentity(nw1)

	if err := rt1.Stop(); err != nil {
		t.Fatalf("stop runtime (boot 1): %v", err)
	}
	close1()

	// ---- Boot 2: same storage, new store instance. ----
	ns2, close2 := mustOpenNetworkStore(t, dbPath)
	defer close2()

	nw2, err := ns2.LoadNetwork(ctx)
	if err != nil {
		t.Fatalf("load network (boot 2): %v", err)
	}
	if nw2 == nil {
		t.Fatal("boot 2: no persisted network found — storage lost the network")
	}
	assertSameSnapshot(t, "boot 2 load", snapshotIdentity(nw2), before)

	// The runtime must not require a fresh pairing: the membership created in
	// boot 1 is already Active with the same WireGuard key, and nowhere below
	// do we create or persist a new membership.
	rt2, err := runtime.Reconstruct(ctx, nw2, ns2, testLogger())
	if err != nil {
		t.Fatalf("reconstruct (boot 2): %v", err)
	}
	defer rt2.Stop()
	assertRuntimePeers(t, ctx, rt2, []string{hexPub(*m.WireGuardPublicKey)})
}
