// Package dollnetwork defines the canonical wire-protocol types for the
// NeonDoll network. These types are shared between Core and Body
// implementations — no Core-internal types or secrets appear here.
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
// The shared protocol owns this encoding. Anything that is not standard
// base64 of exactly 32 bytes is not valid wire material; encode via this
// function instead of relying on package-local representations.
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
// (see EncodeWgPublicKey); other representations are invalid on the wire.
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

// ValidationError carries the reason string that HandlePairing maps to
// an error response reason. It wraps the underlying validation error.
type ValidationError struct {
	Reason string
	err    error
}

func (e *ValidationError) Error() string { return e.err.Error() }
func (e *ValidationError) Unwrap() error { return e.err }

// ValidatePairRequest performs a defensive check that the request carries the
// public key in the shared wire encoding (base64 of 32 bytes) and never the
// private key (i.e., no private-key field exists). Requiring DecodeWgPublicKey
// here means a malformed or non-wire representation is rejected at the
// protocol boundary instead of leaking onto the wire. It returns an error if
// the request is malformed.
func (r *PairRequest) ValidatePairRequest() error {
	if r.Version != ProtocolVersion {
		return &ValidationError{Reason: "unsupported_version", err: fmt.Errorf("dollnetwork: unsupported protocol version %d", r.Version)}
	}
	if r.Body.BodyID == "" {
		return &ValidationError{Reason: "missing_body_id", err: fmt.Errorf("dollnetwork: pairing request missing body_id")}
	}
	if r.Body.Implementation == "" {
		return &ValidationError{Reason: "missing_implementation", err: fmt.Errorf("dollnetwork: pairing request missing implementation")}
	}
	if r.Network.WireGuardPublicKey == "" {
		return &ValidationError{Reason: "missing_wireguard_key", err: fmt.Errorf("dollnetwork: pairing request missing wireguard public key")}
	}
	if _, err := DecodeWgPublicKey(r.Network.WireGuardPublicKey); err != nil {
		return &ValidationError{Reason: "invalid_wg_public_key", err: fmt.Errorf("dollnetwork: invalid wireguard public key: %w", err)}
	}
	return nil
}

// ── PairResponse (Core → Body, success) ──────────────────────────────────────

// PairResponse is the canonical successful pairing response from Core.
// It carries the information a Body needs to establish WireGuard and
// Doll Link connectivity.
//
// CoreEndpoints may be empty; an empty slice is valid (the Body will
// discover endpoints out of band or through a relay later).
type PairResponse struct {
	Version         int       `json:"version"`
	NetworkID       string    `json:"network_id"`
	BodyPeerID      string    `json:"body_peer_id"`
	BodyAddresses   []string  `json:"body_addresses"`
	CorePeerID      string    `json:"core_peer_id"`
	CoreWGPublicKey string    `json:"core_wg_public_key"`
	CoreAddresses   []string  `json:"core_addresses"`
	CoreEndpoints   Endpoints `json:"core_endpoints"`
}

// ── PairErrorResponse (Core → Body, error/denial) ────────────────────────────

// PairErrorResponse is the canonical error or denial response from Core.
// Consumed indicates whether the invitation was consumed as part of the
// attempt (true for well-formed but denied/replayed requests, false for
// malformed or unknown-invitation errors).
type PairErrorResponse struct {
	Version  int    `json:"version"`
	Error    string `json:"error"`
	Reason   string `json:"reason"`
	Consumed bool   `json:"consumed"`
}

// ── Invitation (out-of-band document) ────────────────────────────────────────

// Invitation is the out-of-band invitation document that Core creates and
// delivers to a Body through some side channel (QR code, file, CLI output).
// The secret is only revealed once and MUST be kept confidential.
type Invitation struct {
	Version            int       `json:"version"`
	InvitationID       string    `json:"invitation_id"`
	InvitationSecret   string    `json:"invitation_secret"`
	ExpiresAt          string    `json:"expires_at"`
	BootstrapEndpoints Endpoints `json:"bootstrap_endpoints"`
}

// ── BootstrapEndpoint descriptor ─────────────────────────────────────────────

// BootstrapEndpoint describes a reachable address where the Body can contact
// Core to begin the pairing flow.
type BootstrapEndpoint struct {
	// URL is the full URL (e.g. "https://core.example.com:8443").
	//
	// RL: transport is inferred from the URL scheme (http/https = direct,
	// relay:// = via a relay). This is sufficient for M2, where only direct
	// bootstrap is supported. It becomes insufficient once Relay bootstrap can
	// itself speak HTTPS/WSS (a relay reachable at https:// or wss:// is
	// ambiguous with a direct endpoint). Do NOT widen this shape now — the
	// transport/relay distinction is a Relay-milestone protocol decision, not
	// an M2 pairing concern. Recorded for the Relay milestone.
	URL string `json:"url"`
}

// Endpoints is a convenience alias for a slice of BootstrapEndpoint.
type Endpoints []BootstrapEndpoint
