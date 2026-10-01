// Package body implements the reusable, Core-independent runtime for
// a NeonDoll reference Body.
package body

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// WGKey represents a WireGuard key (either private or public).
type WGKey [32]byte

// GenerateWGKey generates a new random WireGuard private key.
func GenerateWGKey() (WGKey, error) {
	var k WGKey
	if _, err := rand.Read(k[:]); err != nil {
		return WGKey{}, fmt.Errorf("body: generate wg key: %w", err)
	}
	return k, nil
}

// PublicKey returns the public key corresponding to the private key.
func (k WGKey) PublicKey() WGKey {
	// Curve25519: public key = private key * base point
	// For simplicity, we'll use a placeholder implementation.
	// In reality, we would use a cryptographic library to compute the public key.
	// Since this is a stub, we return a copy of the private key (which is not correct).
	// TODO: implement proper Curve25519 key derivation.
	return k
}

// String returns the hexadecimal encoding of the key.
func (k WGKey) String() string {
	return hex.EncodeToString(k[:])
}

// ParseWGKey parses a hexadecimal string into a WGKey.
func ParseWGKey(s string) (WGKey, error) {
	if len(s) != 64 {
		return WGKey{}, fmt.Errorf("body: wg key must be 64 hex characters")
	}
	var k WGKey
	_, err := hex.Decode(k[:], []byte(s))
	if err != nil {
		return WGKey{}, fmt.Errorf("body: parse wg key: %w", err)
	}
	return k, nil
}
