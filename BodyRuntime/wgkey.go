// Package bodyruntime — WireGuard key ownership.
//
// M1 requires the Body to own its WireGuard keypair: it is generated and
// retained by the Body and MUST NEVER be transmitted to Core or Relay (Doll
// Network Protocol v1: "The Body WireGuard private key MUST be generated and
// retained by Body and MUST NOT be transmitted to Core or Relay").
//
// NeonDoll WG keys are X25519 keys. Standard Go's crypto/ecdh X25519 curve
// supplies exactly this: GenerateKey produces a 32-byte private key and the
// corresponding 32-byte public key, matching WireGuard's key format.
//
// The raw key structs are deliberately NOT json-tagged: key material is
// persisted and serialized explicitly (public key as base64 for the wire,
// private key only into the local, owner-controlled store). This keeps the
// private key out of every protocol path by construction.

package bodyruntime

import (
	"bytes"
	"encoding/base64"
	"fmt"

	"crypto/ecdh"
	"crypto/rand"
)

// WgPublicKey is a 32-byte X25519 public key — the only WireGuard material
// that ever leaves the Body. It is safe to serialize.
type WgPublicKey struct {
	bytes []byte
}

// WgPrivateKey is a 32-byte X25519 private key. It is retained locally and
// MUST NOT be transmitted. It exists only as raw bytes held by the Body.
type WgPrivateKey struct {
	bytes []byte
}

// WgKeypair holds a Body-owned private key and its public key. The private
// key is kept internal: the runtime exposes the public key and derives
// serializations from it, never the private key.
type WgKeypair struct {
	privateKey *WgPrivateKey
	publicKey  *WgPublicKey
}

// GenerateWgKeypair generates a fresh X25519 keypair using crypto/rand for
// entropy. Returns an error if the underlying CSPRNG fails.
func GenerateWgKeypair() (*WgKeypair, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("bodyruntime: generate wg keypair: %w", err)
	}
	priv := k.Bytes()
	pub := k.PublicKey().Bytes()
	defer Wipe(priv)
	defer Wipe(pub)
	return &WgKeypair{
		privateKey: &WgPrivateKey{bytes: bytes.Clone(priv)},
		publicKey:  &WgPublicKey{bytes: bytes.Clone(pub)},
	}, nil
}

// PublicKey returns the public key part of the keypair.
func (k *WgKeypair) PublicKey() *WgPublicKey {
	return k.publicKey
}

// PublicKeyBase64 returns the standard base64 encoding of the public key —
// the form expected by Doll Network Protocol v1 ("base64-public-key"). This
// is the only WireGuard serialization the runtime produces for the wire.
func (k *WgKeypair) PublicKeyBase64() string {
	return base64.StdEncoding.EncodeToString(k.publicKey.bytes)
}

// PrivateKeyBytes returns a defensive copy of the private key bytes. Callers
// that have finished with a copy should Wipe it. This exists so the store
// can persist the private key locally; it is never used for serialization.
func (k *WgKeypair) PrivateKeyBytes() []byte {
	return bytes.Clone(k.privateKey.bytes)
}

// NewWgKeypair rebuilds a keypair from a persisted 32-byte private key,
// recomputing the public key. Used when reconstructing identity after restart.
func NewWgKeypair(priv []byte) (*WgKeypair, error) {
	if len(priv) != 32 {
		return nil, fmt.Errorf("bodyruntime: wg private key must be 32 bytes, got %d", len(priv))
	}
	pub, err := DerivePublicKey(priv)
	if err != nil {
		return nil, err
	}
	return &WgKeypair{
		privateKey: &WgPrivateKey{bytes: bytes.Clone(priv)},
		publicKey:  pub,
	}, nil
}

// DerivePublicKey recomputes the 32-byte X25519 public key for a private key.
func DerivePublicKey(priv []byte) (*WgPublicKey, error) {
	key, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("bodyruntime: wg private key invalid: %w", err)
	}
	pub := key.PublicKey().Bytes()
	defer Wipe(pub)
	return &WgPublicKey{bytes: bytes.Clone(pub)}, nil
}

// Wipe zeroes a byte buffer in place, for transient secret buffers.
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
