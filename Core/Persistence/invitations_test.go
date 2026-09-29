package persistence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	dn "github.com/Neon-Dolls/neondoll/DollNetwork"
)

// assertInvitationStore is a helper that casts a Store to invitation.Store.
func assertInvitationStore(t *testing.T, s Store) invitation.Store {
	t.Helper()
	is, ok := s.(invitation.Store)
	if !ok {
		t.Fatal("Store does not implement invitation.Store")
	}
	return is
}

// dbPath returns a temporary database file path that the caller must clean up.
func dbPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test.db")
}

func TestInvitationDurableRestart(t *testing.T) {
	ctx := context.Background()
	p := dbPath(t) // t.TempDir handles cleanup

	// ── Create invitation in first store ─────────────────────────────
	s1, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore(1): %v", err)
	}
	is1 := assertInvitationStore(t, s1)

	secret, hash := createTestInvitation()
	err = is1.Create(ctx, &invitation.Invitation{
		ID:                 "test-restart-1",
		SecretHash:         hash,
		ExpiresAt:          time.Now().Add(24 * time.Hour),
		Consumed:           false,
		BootstrapEndpoints: dn.Endpoints{{URL: "relay://core.example.com:51820"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// ── Close first store ───────────────────────────────────────────
	if err := s1.Close(); err != nil {
		t.Fatalf("Close(1): %v", err)
	}

	// ── Reopen from same path ───────────────────────────────────────
	s2, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore(2): %v", err)
	}
	is2 := assertInvitationStore(t, s2)

	// ── Validate: invitation exists, not consumed, hash preserved ──
	got, err := is2.Get(ctx, "test-restart-1")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil after restart")
	}
	if got.Consumed {
		t.Fatal("invitation unexpectedly consumed after restart")
	}
	if got.SecretHash != hash {
		t.Fatal("SecretHash mismatch after restart")
	}
	if len(got.BootstrapEndpoints) != 1 || got.BootstrapEndpoints[0].URL != "relay://core.example.com:51820" {
		t.Fatalf("BootstrapEndpoints corrupted after restart: got %+v", got.BootstrapEndpoints)
	}

	// ── Secret hash matches original secret (no plaintext leak) ────
	if invitation.HashSecret(secret) != got.SecretHash {
		t.Fatal("HashSecret(secret) does not match stored hash")
	}

	// ── Consume ────────────────────────────────────────────────────
	ok, err := is2.Consume(ctx, "test-restart-1")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !ok {
		t.Fatal("Consume returned false, expected true")
	}

	// ── Close second store ──────────────────────────────────────────
	if err := s2.Close(); err != nil {
		t.Fatalf("Close(2): %v", err)
	}

	// ── Open THIRD store from same path ────────────────────────────
	s3, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore(3): %v", err)
	}
	is3 := assertInvitationStore(t, s3)

	got3, err := is3.Get(ctx, "test-restart-1")
	if err != nil {
		t.Fatalf("Get after third restart: %v", err)
	}
	if got3 == nil {
		t.Fatal("Get returned nil after third restart")
	}
	if !got3.Consumed {
		t.Fatal("invitation should be consumed after third restart, but is not")
	}

	// ── Consume again must fail ────────────────────────────────────
	if _, err := is3.Consume(ctx, "test-restart-1"); err == nil {
		t.Fatal("second Consume should have failed, but succeeded")
	}

	// ── Close third store ───────────────────────────────────────────
	if err := s3.Close(); err != nil {
		t.Fatalf("Close(3): %v", err)
	}
}

func TestInvitationDurableNoPlaintextSecret(t *testing.T) {
	ctx := context.Background()
	p := dbPath(t)

	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	is := assertInvitationStore(t, s)

	secret, hash := createTestInvitation()
	err = is.Create(ctx, &invitation.Invitation{
		ID:                 "test-nosecret-1",
		SecretHash:         hash,
		ExpiresAt:          time.Now().Add(1 * time.Hour),
		Consumed:           false,
		BootstrapEndpoints: dn.Endpoints{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify the raw database file does NOT contain the plaintext secret.
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", p, err)
	}
	if containsBytes(data, []byte(secret)) {
		t.Fatal("plaintext secret found in raw database file")
	}

	s.Close()
}

func TestInvitationDurableExpiryAfterReopen(t *testing.T) {
	ctx := context.Background()
	p := dbPath(t)

	s1, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore(1): %v", err)
	}
	is1 := assertInvitationStore(t, s1)

	// Create an already-expired invitation.
	_, hash := createTestInvitation()
	expired := time.Now().Add(-1 * time.Hour)
	err = is1.Create(ctx, &invitation.Invitation{
		ID:                 "test-expired-1",
		SecretHash:         hash,
		ExpiresAt:          expired,
		Consumed:           false,
		BootstrapEndpoints: dn.Endpoints{},
	})
	if err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	s1.Close()

	// Reopen and verify expiry is preserved.
	// The stored time is serialized as RFC3339 which loses sub-second
	// precision and monotonic clock readings.
	s2, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore(2): %v", err)
	}
	is2 := assertInvitationStore(t, s2)

	got, err := is2.Get(ctx, "test-expired-1")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil after reopen")
	}
	if !got.ExpiresAt.Before(time.Now()) {
		t.Fatal("invitation should be expired after reopen, but ExpiresAt is in the future")
	}
	if !got.ExpiresAt.Truncate(time.Second).Equal(expired.Truncate(time.Second).UTC()) {
		t.Fatalf("expiry mismatch: got %v, want %v", got.ExpiresAt, expired)
	}
	s2.Close()
}

func TestInvitationDurableNotFound(t *testing.T) {
	ctx := context.Background()
	p := dbPath(t)

	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	is := assertInvitationStore(t, s)

	got, err := is.Get(ctx, "nonexistent-id")
	if err != nil {
		t.Fatalf("Get on nonexistent: %v", err)
	}
	if got != nil {
		t.Fatal("Get on nonexistent should return nil, nil")
	}

	s.Close()
}

func TestInvitationDurableAtomicConsume(t *testing.T) {
	ctx := context.Background()
	p := dbPath(t)

	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	is := assertInvitationStore(t, s)

	_, hash := createTestInvitation()
	err = is.Create(ctx, &invitation.Invitation{
		ID:                 "test-atomic-1",
		SecretHash:         hash,
		ExpiresAt:          time.Now().Add(1 * time.Hour),
		Consumed:           false,
		BootstrapEndpoints: dn.Endpoints{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Consume once.
	ok, err := is.Consume(ctx, "test-atomic-1")
	if err != nil {
		t.Fatalf("Consume(1): %v", err)
	}
	if !ok {
		t.Fatal("Consume(1) returned false, expected true")
	}

	// Second consume MUST fail — atomicity guarantee.
	if _, err := is.Consume(ctx, "test-atomic-1"); err == nil {
		t.Fatal("second Consume should have failed (already consumed)")
	}

	s.Close()
}

// ── helpers ──────────────────────────────────────────────────────────────────

// createTestInvitation generates a cryptographically strong secret and its hash
// for use in tests. Returns the plaintext secret and its SHA-256 hash.
func createTestInvitation() (string, [32]byte) {
	secret, err := invitation.GenerateSecret()
	if err != nil {
		panic(fmt.Sprintf("GenerateSecret: %v", err))
	}
	hash := invitation.HashSecret(secret)
	return secret, hash
}

// containsBytes reports whether the given byte slice contains the target bytes.
func containsBytes(data, target []byte) bool {
	if len(target) == 0 {
		return false
	}
	for i := 0; i <= len(data)-len(target); i++ {
		if matchBytes(data[i:], target) {
			return true
		}
	}
	return false
}

// matchBytes reports whether data[0:len(target)] == target.
func matchBytes(data, target []byte) bool {
	for i := range target {
		if data[i] != target[i] {
			return false
		}
	}
	return true
}
