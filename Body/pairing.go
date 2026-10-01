// Package body implements the reusable, Core-independent runtime for
// a NeonDoll reference Body.
package body

import (
	"errors"
	"net"
)

// Endpoint represents a Core endpoint that a Body can connect to.
type Endpoint struct {
	// UDPAddr is the Core's WireGuard UDP endpoint.
	UDPAddr *net.UDPAddr
	// PublicKey is the Core's WireGuard public key.
	PublicKey [32]byte
}

// FromBootstrapURL parses a bootstrap URL string into an Endpoint.
// It supports the M2 URL format: <scheme>://<host>:<port>?pubkey=<hex>
// where scheme is either "http" or "https", and the pubkey is the Core's
// WireGuard public key in hexadecimal encoding.
// If the URL does not contain a pubkey parameter, it returns an error.
func FromBootstrapURL(raw string) (*Endpoint, error) {
	// TODO: implement proper URL parsing.
	// For now, we return a stub to satisfy the compiler.
	return nil, errors.New("not implemented")
}
