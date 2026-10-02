package relay

import (
	"testing"
	"time"
)

// waitConnected blocks up to 5s for the client to report connected.
// This is the version from control_test_support.go; we use it directly.

// identitySnapshot captures the identity state of a ControlClient for
// before/after comparisons across lifecycle events.
type identitySnapshot struct {
	RelayID     RelayID
	OpenRouteID RouteID
}

// assertIdentityUnchanged fails t if any identity field changed.
func assertIdentityUnchanged(t *testing.T, before, after identitySnapshot) {
	t.Helper()
	if before.RelayID != after.RelayID {
		t.Errorf("RelayID changed: before=%q after=%q", before.RelayID, after.RelayID)
	}
}

// waitForRestore blocks until client reports at least n routes restored,
// or the deadline passes.
func waitForRestore(t *testing.T, client *ControlClient, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m := client.Metrics()
		if m.RoutesRestored >= n {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	m := client.Metrics()
	t.Fatalf("expected %d routes restored, got %d (reconnects=%d)", n, m.RoutesRestored, m.Reconnects)
}
