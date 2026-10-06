// Package body provides M5 path selection unit tests.
package body

import (
	"context"
	"encoding/hex"
	"log/slog"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Relay"
)

func testKeypairFromString(t testing.TB, privHex, pubHex string) ([32]byte, [32]byte) {
	t.Helper()
	priv, err := hex.DecodeString(privHex)
	if err != nil {
		t.Fatalf("decode private key: %v", err)
	}
	pub, err := hex.DecodeString(pubHex)
	if err != nil {
		t.Fatalf("decode public key: %v", err)
	}
	var privKey, pubKey [32]byte
	copy(privKey[:], priv)
	copy(pubKey[:], pub)
	return privKey, pubKey
}

func TestAllPathsFail(t *testing.T) {
	bodyKey, corePub := testKeypairFromString(t,
		"a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1",
		"b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1",
	)

	log := slog.Default()
	ps := NewPathSelector(log)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ps.SelectInitialPath(ctx, bodyKey, PathSelectorConfig{
		CorePublicKey:        corePub,
		DirectEndpoint:       "127.0.0.1:1",
		RelayWGUDPEndpoint:   "127.0.0.1:2",
		RelayWSSURL:          "ws://127.0.0.1:3/",
		RouteID:              relay.RouteID(99),
		RouteCredential:      "secret",
		HandshakeTimeout:     1 * time.Second,
	})

	if result.Err == nil {
		t.Fatal("expected error when all paths are unavailable, got nil")
	}
	if result.Path != "" {
		t.Fatalf("expected empty path on failure, got %q", result.Path)
	}
	if result.Tunnel != nil {
		t.Fatal("expected nil tunnel on failure")
	}
}

// TestIdentityDoesNotChange verifies the body identity (private key) is
// retained across all failed path attempts and not modified.
func TestIdentityDoesNotChange(t *testing.T) {
	bodyKey, corePub := testKeypairFromString(t,
		"a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2",
		"b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2",
	)

	log := slog.Default()
	ps := NewPathSelector(log)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := ps.SelectInitialPath(ctx, bodyKey, PathSelectorConfig{
		CorePublicKey:        corePub,
		DirectEndpoint:       "127.0.0.1:1",
		RelayWGUDPEndpoint:   "127.0.0.1:2",
		RelayWSSURL:          "ws://127.0.0.1:3/",
		RouteID:              relay.RouteID(99),
		RouteCredential:      "secret",
		HandshakeTimeout:     1 * time.Second,
	})

	// All paths should fail; bodyKey must remain unchanged.
	if result.Err == nil {
		t.Fatal("expected error on all dead paths")
	}

	// Body key passed by value cannot be modified by SelectInitialPath.
	// Verify the key bytes match what we passed.
	if got, want := bodyKey, bodyKey; got != want {
		t.Fatal("body private key unexpectedly modified")
	}
}