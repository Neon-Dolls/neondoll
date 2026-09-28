package dollnetwork

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the Doll Network Protocol version.
const ProtocolVersion = 1

// WgPublicKeySize is the byte length of an X25519/WireGuard public key.
const WgPublicKeySize = 32

// EncodeWgPublicKey returns the Doll Network wire representation of a WG
// public key: standard (RFC 4648) base64 of the 32-byte X25519 key.
//
// The shared protocol owns this encoding. Neither Body's nor Core's internal
// or diagnostic formats are valid on the wire — in particular Core's
// WireGuardPublicKey.String() (hex, for diagnostics) MUST NOT be written to
// the wire: encode via this function instead.
func EncodeWgPublicKey(pk []byte) (string, error) {
	if len(pk) != WgPublicKeySize {
		return "", fmt.Errorf("dollnetwork: wg public key must be %d bytes, got %d", WgPublicKeySize, len(pk))
	}
	return base64.StdEncoding.EncodeToString(pk), nil
}

// DecodeWgPublicKey parses the Doll Network wire form (standard base64) of a
// WG public key back into the raw 32 bytes. Used to validate wire material
// and to map a wire field into Body/Core representations for M2.
func DecodeWgPublicKey(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("dollnetwork: wg public key is not valid base64: %w", err)
	}
	if len(raw) != WgPublicKeySize {
		return nil, fmt.Errorf("dollnetwork: wg public key must decode to %d bytes, got %d", WgPublicKeySize, len(raw))
	}
	return raw, nil
}

// PairRequestBody mirrors the "body" object of the current M1 pairing request.
// The exact wire shape is finalized during M2; today this is the agreed M1
// field set submitted toward Core.
type PairRequestBody struct {
	BodyID         string `json:"body_id"`
	Name           string `json:"name,omitempty"`
	Implementation string `json:"implementation"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
}

// PairingNetwork mirrors the "network" object: currently just the WG public key.
// This is the only WireGuard material the runtime ever serializes. The public
// key uses the shared wire encoding, standard base64-of-32-bytes
// (see EncodeWgPublicKey). Do not write Core's hex String() form here.
type PairingNetwork struct {
	WireGuardPublicKey string `json:"wireguard_public_key"`
}

// PairRequest is the pairing request carried to Core. invitation_id and secret
// are provided by Core's invitation at pairing time (M2); this package defines the
// M1 serialization shape that submission will build on.
type PairRequest struct {
	Version      int             `json:"version"`
	InvitationID string          `json:"invitation_id"`
	Secret       string          `json:"secret"`
	Body         PairRequestBody `json:"body"`
	Network      PairingNetwork  `json:"network"`
}

// ToJSON renders the pairing request as JSON for submission.
func (r *PairRequest) ToJSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ValidatePairRequest performs a defensive check that the request carries the
// public key in the shared wire encoding (base64 of 32 bytes) and never the
// private key (i.e., no private-key field exists). Requiring DecodeWgPublicKey
// here means a hex-encoded key — e.g. Core's WireGuardPublicKey.String()
// diagnostic form — is rejected at the protocol boundary instead of leaking
// onto the wire. It returns an error if the request is malformed.
func (r *PairRequest) ValidatePairRequest() error {
	if r.Version != ProtocolVersion {
		return fmt.Errorf("dollnetwork: unsupported protocol version %d", r.Version)
	}
	if r.Body.BodyID == "" {
		return fmt.Errorf("dollnetwork: pairing request missing body_id")
	}
	if r.Body.Implementation == "" {
		return fmt.Errorf("dollnetwork: pairing request missing implementation")
	}
	if r.Network.WireGuardPublicKey == "" {
		return fmt.Errorf("dollnetwork: pairing request missing wireguard public key")
	}
	if _, err := DecodeWgPublicKey(r.Network.WireGuardPublicKey); err != nil {
		return fmt.Errorf("dollnetwork: invalid wireguard public key: %w", err)
	}
	return nil
}
