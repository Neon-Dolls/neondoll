// SPDX-License-Identifier: AGPL-3.0-only

package body

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

func TestCandidatesBetterThan(t *testing.T) {
	tests := []struct {
		current string
		want    int
	}{
		{PathDirect, 0},
		{PathRelayUDP, 1},
		{PathRelayWSS, 2},
		{"unknown", 0},
	}

	for _, tt := range tests {
		got := candidatesBetterThan(tt.current)
		if len(got) != tt.want {
			t.Errorf("candidatesBetterThan(%q) = %d candidates, want %d", tt.current, len(got), tt.want)
		}
	}
}

func TestCandidatesBetterThanOrder(t *testing.T) {
	candidates := candidatesBetterThan(PathRelayWSS)
	if len(candidates) != 2 {
		t.Fatalf("wanted 2 candidates, got %d: %v", len(candidates), candidates)
	}
	if candidates[0] != PathDirect {
		t.Errorf("first candidate should be direct, got %s", candidates[0])
	}
	if candidates[1] != PathRelayUDP {
		t.Errorf("second candidate should be relay-udp, got %s", candidates[1])
	}
}

func TestDirectActiveSkipsProbing(t *testing.T) {
	cfg := fakeCfg()
	result := ProbeBetter(context.Background(), cfg, PathDirect, defaultProbeConfig(), testLogger(t))
	if result != nil {
		t.Errorf("expected nil when on direct, got %+v", *result)
	}
}

func TestUnknownPathSkipsProbing(t *testing.T) {
	cfg := fakeCfg()
	result := ProbeBetter(context.Background(), cfg, "unknown-path", defaultProbeConfig(), testLogger(t))
	if result != nil {
		t.Errorf("expected nil for unknown path, got %+v", *result)
	}
}

func TestProbeConfigDefaults(t *testing.T) {
	dc := defaultProbeConfig()
	if dc.PerAttemptTimeout != 0 {
		t.Errorf("default timeout = %v, want 0 (uses selector config)", dc.PerAttemptTimeout)
	}
	if dc.MinBackoff != 1*time.Second {
		t.Errorf("default min backoff = %v, want 1s", dc.MinBackoff)
	}
	if dc.MaxBackoff != 10*time.Second {
		t.Errorf("default max backoff = %v, want 10s", dc.MaxBackoff)
	}
	if dc.BackoffFactor != 2.0 {
		t.Errorf("default backoffFactor = %v, want 2.0", dc.BackoffFactor)
	}
}

func TestProbeConfigBackoffSequence(t *testing.T) {
	dc := defaultProbeConfig()

	tests := []struct {
		input time.Duration
		want  time.Duration
	}{
		{0 * time.Second, 1 * time.Second},
		{1 * time.Second, 2 * time.Second},
		{3 * time.Second, 6 * time.Second},
		{5 * time.Second, 10 * time.Second},
		{10 * time.Second, 10 * time.Second},
		{15 * time.Second, 10 * time.Second},
	}

	for _, tt := range tests {
		got := dc.effectiveBackoff(tt.input)
		if got != tt.want {
			t.Errorf("effectiveBackoff(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestProbeBetterCancellationAndCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	cfg := fakeCfg()

	result := ProbeBetter(ctx, cfg, PathRelayWSS, ProbeConfig{
		PerAttemptTimeout: 500 * time.Millisecond,
		MinBackoff:        100 * time.Millisecond,
		MaxBackoff:        500 * time.Millisecond,
		BackoffFactor:     2.0,
	}, testLogger(t))

	// Context times out during probing; all probes fail (no real endpoints).
	// Expected: nil return or a failed ProbeResult.
	if result != nil && result.HandshakeOK {
		t.Errorf("expected nil or failed result on cancellation, got HandshakeOK=%v", result.HandshakeOK)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func fakeCfg() PathSelectorConfig {
	var pk [32]byte
	var cpk [32]byte
	rand.Read(pk[:])
	rand.Read(cpk[:])
	return PathSelectorConfig{
		PrivateKey:         pk,
		CorePublicKey:      cpk,
		OverlayAddress:     netip.MustParseAddr("10.0.0.2"),
		OverlayPrefix:      netip.PrefixFrom(netip.MustParseAddr("10.0.0.0"), 24),
		DirectEndpoint:     "192.168.1.1:51820",
		RelayWGUDPEndpoint: "192.168.1.2:51820",
		RelayWSSURL:        "wss://relay.example.com/wa",
		RouteID:            relay.RouteID(42),
		RouteCredential:    "test-cred",
		PerAttemptTimeout:  5 * time.Second,
		TLSConfig:          nil,
	}
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
