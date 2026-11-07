package openviking

import (
	"context"
	"errors"
	"testing"
	"time"

	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
)

func TestConfigurationCapabilitiesHealthAndClose(t *testing.T) {
	for _, cfg := range []Config{{}, {Endpoint: "ftp://x", UserID: "u", DollID: "d"}, {Endpoint: "http://x", UserID: "../u", DollID: "d"}, {Endpoint: "http://x", UserID: "u", DollID: "d", MaxReplayKeys: -1}} {
		if _, err := Open(cfg); err == nil {
			t.Fatalf("accepted %#v", cfg)
		}
	}
	f, server := newFake(t)
	p := openTest(t, server.URL, "d")
	caps, err := p.Capabilities(context.Background())
	if err != nil || caps.Consistency.ReplayScope != memory.ReplayScopeRecord || !caps.Consistency.ReplayDurable || caps.Consistency.RevisionConflicts != memory.RevisionConflictsSingleWriter || caps.Consistency.ReadAfterWrite != memory.ReadAfterWriteEventual || caps.Consistency.ImportAtomicity != memory.ImportAtomicityPerRecord || caps.Consistency.RetrievalSemantics != memory.RetrievalProviderNative {
		t.Fatalf("capabilities: %#v %v", caps, err)
	}
	f.mu.Lock()
	f.failStatus = 503
	f.mu.Unlock()
	health, err := p.Health(context.Background())
	if err == nil || health.Status != memory.HealthUnavailable {
		t.Fatalf("health: %#v %v", health, err)
	}
	f.mu.Lock()
	f.failStatus = 0
	f.delay = 100 * time.Millisecond
	f.mu.Unlock()
	short, err := Open(Config{Endpoint: server.URL, APIKey: "secret", UserID: "u", DollID: "short", Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = short.Health(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	f.mu.Lock()
	f.delay = 0
	f.mu.Unlock()
	_ = p.Close()
	if _, err = p.Remember(context.Background(), memory.RememberRequest{Record: record("x", 1, "x")}); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatalf("closed: %v", err)
	}
}
