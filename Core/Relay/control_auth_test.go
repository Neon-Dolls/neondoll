package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// ── A2: Registration Token Verification ────────────────────────────────
//
// These tests verify the server-side registration token verification
// added in Relay A2. The VerifyRegistrationToken function is checked
// with both valid and invalid tokens.

// TestVerifyRegistrationToken_EmptyCredentials verifies that when no
// credential verifiers are configured, any token is rejected (fail-closed).
func TestVerifyRegistrationToken_EmptyCredentials(t *testing.T) {
	t.Parallel()

	err := VerifyRegistrationToken("my-token", []string{})
	if err == nil {
		t.Fatal("empty credentials + non-empty token: err = nil; expected auth error")
	}
}

// TestVerifyRegistrationToken_ValidToken verifies that a token whose
// SHA-256 hash matches a configured credential is accepted.
func TestVerifyRegistrationToken_ValidToken(t *testing.T) {
	t.Parallel()

	token := "test-token-1"
	hash := sha256.Sum256([]byte(token))
	credHash := hex.EncodeToString(hash[:])

	err := VerifyRegistrationToken(token, []string{credHash})
	if err != nil {
		t.Fatalf("valid token: err = %v; want nil", err)
	}
}

// TestVerifyRegistrationToken_InvalidToken verifies that a token whose
// SHA-256 hash does not match any configured credential is rejected.
func TestVerifyRegistrationToken_InvalidToken(t *testing.T) {
	t.Parallel()

	token := "wrong-token"
	authorized := "test-token-1"
	hash := sha256.Sum256([]byte(authorized))
	credHash := hex.EncodeToString(hash[:])

	err := VerifyRegistrationToken(token, []string{credHash})
	if err == nil {
		t.Fatal("invalid token: err = nil; expected auth error")
	}
}

// TestVerifyRegistrationToken_EmptyToken verifies that an empty token
// fails when credentials are configured.
func TestVerifyRegistrationToken_EmptyToken(t *testing.T) {
	t.Parallel()

	hash := sha256.Sum256([]byte("anything"))
	credHash := hex.EncodeToString(hash[:])

	err := VerifyRegistrationToken("", []string{credHash})
	if err == nil {
		t.Fatal("empty token with credentials: err = nil; expected auth error")
	}
}

// TestVerifyRegistrationToken_MultipleCredentials verifies that a token
// matching any one of several configured credentials is accepted.
func TestVerifyRegistrationToken_MultipleCredentials(t *testing.T) {
	t.Parallel()

	token1 := "alpha"
	token2 := "beta"
	hash1 := sha256.Sum256([]byte(token1))
	hash2 := sha256.Sum256([]byte(token2))

	creds := []string{
		hex.EncodeToString(hash1[:]),
		hex.EncodeToString(hash2[:]),
	}

	// Both tokens should pass
	err := VerifyRegistrationToken(token1, creds)
	if err != nil {
		t.Fatalf("token1 with matching creds: err = %v; want nil", err)
	}
	err = VerifyRegistrationToken(token2, creds)
	if err != nil {
		t.Fatalf("token2 with matching creds: err = %v; want nil", err)
	}

	// A different token should fail
	err = VerifyRegistrationToken("gamma", creds)
	if err == nil {
		t.Fatal("unlisted token: err = nil; expected auth error")
	}
}
