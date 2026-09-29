package dollnetwork

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"net/netip"
)

// ProtocolVersion is the Doll Network Protocol version.
const ProtocolVersion = 1

// WgPublicKeySize is the byte length of an X25519/WireGuard public key.
const WgPublicKeySize = 32

// PairingPath is the canonical path a direct HTTPS bootstrap endpoint exposes
// for the pairing handshake. The Body POSTs the PairRequest here (see also
// BootstrapEndpoint.Type "direct").
const PairingPath = "/pair"

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

// PairRequestBody mirrors the "body" object of the pairing request.
type PairRequestBody struct {
	BodyID         string `json:"body_id"`
	Name           string `json:"name,omitempty"`
	Implementation string `json:"implementation"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
}

// PairingNetwork mirrors the "network" object: currently just the WG public key.
// This is the only WireGuard material the runtime ever serializes. The public
// key uses the shared wire encoding (base64-of-32-bytes); other
// representations are invalid on the wire.
type PairingNetwork struct {
	WireGuardPublicKey string `json:"wireguard_public_key"`
}

// PairRequest is the pairing request carried to Core. invitation_id and secret
// are provided by Core's invitation at pairing time.
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
// private key. It returns an error if the request is malformed.
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

// EndpointType enumerates the advertised bootstrap endpoint kinds.
type EndpointType string

const (
	// EndpointDirect is a direct HTTP(S) bootstrap: the Body POSTs the
	// PairRequest to BootstrapURL + PairingPath. This is the only endpoint
	// transport the Body implements as of M2.
	EndpointDirect EndpointType = "direct"
	// EndpointRelay is a relay bootstrap endpoint. It is representable and
	// dispatchable, but Relay transport is NOT implemented yet: the Body fails
	// explicitly ("unsupported") rather than silently behaving like direct.
	EndpointRelay EndpointType = "relay"
)

// BootstrapEndpoint describes how the Body can reach Core for pairing.
type BootstrapEndpoint struct {
	Type         EndpointType `json:"type"`
	// BootstrapURL is the endpoint base URL. For "direct" this is an
	// http(s) origin (the PairRequest is POSTed to BootstrapURL + PairingPath);
	// for "relay" it is the relay origin.
	BootstrapURL string       `json:"bootstrap_url"`
}

// ValidBootstrapEndpoint returns an error if the endpoint is not structurally
// valid for its declared type. For "direct" it requires a non-empty, http(s)
// origin; for "relay" it requires a non-empty origin (transport itself is
// unsupported until the Relay milestone, which the pairing client enforces).
func (e *BootstrapEndpoint) ValidBootstrapEndpoint() error {
	if e.BootstrapURL == "" {
		return fmt.Errorf("dollnetwork: bootstrap endpoint missing bootstrap_url")
	}
	if e.Type == EndpointDirect {
		if !strings.HasPrefix(e.BootstrapURL, "https://") && !strings.HasPrefix(e.BootstrapURL, "http://") {
			return fmt.Errorf("dollnetwork: direct bootstrap endpoint must be http(s) URL")
		}
		return nil
	}
	if e.Type == EndpointRelay {
		return nil
	}
	return fmt.Errorf("dollnetwork: unknown bootstrap endpoint type %q", string(e.Type))
}

// Invitation is the pairing invitation document issued by Core. It contains
// the invitation id/secret, expiry, and advertised bootstrap endpoints.
type Invitation struct {
	Version           int                `json:"version"`
	ID                string             `json:"invitation_id"`
	Secret            string             `json:"secret"`
	ExpiresAt         int64              `json:"expires_at"`     // Unix epoch seconds
	BootstrapEndpoints []BootstrapEndpoint `json:"bootstrap_endpoints"`
}

// FromJSON parses an Invitation from JSON, validating version and required
// fields. Expiry check is left to the caller (it needs a current time) via
// ValidInvitationAt.
func (i *Invitation) FromJSON(data []byte) error {
	if err := json.Unmarshal(data, i); err != nil {
		return fmt.Errorf("dollnetwork: failed to parse invitation: %w", err)
	}
	if i.Version != ProtocolVersion {
		return fmt.Errorf("dollnetwork: unsupported invitation version %d", i.Version)
	}
	if i.ID == "" {
		return fmt.Errorf("dollnetwork: invitation missing invitation_id")
	}
	if i.Secret == "" {
		return fmt.Errorf("dollnetwork: invitation missing secret")
	}
	if len(i.BootstrapEndpoints) == 0 {
		return fmt.Errorf("dollnetwork: invitation missing bootstrap endpoints")
	}
	for _, e := range i.BootstrapEndpoints {
		if err := e.ValidBootstrapEndpoint(); err != nil {
			return err
		}
	}
	return nil
}

// ValidInvitationAt returns an error if the invitation is expired as of the
// given Unix epoch `now` seconds. It must be called before relying on the
// invitation for bootstrap.
func (i *Invitation) ValidInvitationAt(now int64) error {
	if i.ExpiresAt <= now {
		return fmt.Errorf("dollnetwork: pairing invitation expired")
	}
	return nil
}

// AllEndpointsValidate returns nil only if every advertised endpoint is
// structurally valid for its declared type.
func (i *Invitation) AllEndpointsValidate() error {
	for _, e := range i.BootstrapEndpoints {
		if err := e.ValidBootstrapEndpoint(); err != nil {
			return err
		}
	}
	return nil
}

// PairResponse is the successful pairing response returned by Core. It carries
// the Body's assigned network identity and Core's identity. The Body must
// treat this as untrusted network input and validate it fully (FromJSON) before
// persisting anything.
type PairResponse struct {
	Version      int    `json:"version"`          // must equal ProtocolVersion
	NetworkID    string `json:"network_id"`       // Doll Network ID
	BodyPeerID   string `json:"body_peer_id"`     // Body's peer ID in the network (assigned by Core)
	BodyIPv6     string `json:"body_ipv6"`        // Body's overlay IPv6 address/prefix (assigned by Core)
	CorePeerID   string `json:"core_peer_id"`     // Core's peer ID in the network
	CoreWGKeyB64 string `json:"core_wg_key_b64"`  // Core's WireGuard public key (base64)
	CoreIPv6     string `json:"core_ipv6"`        // Core's overlay IPv6 address/prefix
	// Endpoints are the endpoints Core advertises for the Body to reach it.
	// They are runtime topology, NOT durable Body identity: the Body validates
	// them structurally but must not persist them as identity.
	Endpoints    []BootstrapEndpoint `json:"endpoints,omitempty"`
}

// ValidateIPv6AddressOrPrefix returns an error if `s` is not a valid IPv6
// address, or an IPv6 address with a prefix length.
func ValidateIPv6AddressOrPrefix(s string) error {
	if s == "" {
		return fmt.Errorf("dollnetwork: empty ipv6 address")
	}
	// Address may carry a prefix ("addr" or "addr/len").
	var addr netip.Addr
	if strings.Contains(s, "/") {
		pre, err := netip.ParsePrefix(s)
		if err != nil {
			return fmt.Errorf("dollnetwork: invalid ipv6 prefix %q: %w", s, err)
		}
		addr = pre.Addr()
	} else {
		var err error
		addr, err = netip.ParseAddr(s)
		if err != nil {
			return fmt.Errorf("dollnetwork: invalid ipv6 address %q: %w", s, err)
		}
	}
	if !addr.Is6() {
		return fmt.Errorf("dollnetwork: %q is not an ipv6 address", s)
	}
	return nil
}

// FromJSON parses a PairResponse from JSON and validates every required field:
// protocol version, non-empty network_id, non-empty Body peer_id / Core
// peer_id, a valid Body and Core IPv6 address/prefix, a valid Core WG public
// key (canonical DecodeWgPublicKey), and structurally-valid endpoints. A
// malformed response is rejected atomically; the caller must not persist any
// partial state.
func (r *PairResponse) FromJSON(data []byte) error {
	if err := json.Unmarshal(data, r); err != nil {
		return fmt.Errorf("dollnetwork: failed to parse pairing response: %w", err)
	}
	if r.Version != ProtocolVersion {
		return fmt.Errorf("dollnetwork: unsupported protocol version %d", r.Version)
	}
	if r.NetworkID == "" {
		return fmt.Errorf("dollnetwork: missing network_id in pairing response")
	}
	if r.BodyPeerID == "" {
		return fmt.Errorf("dollnetwork: missing body_peer_id in pairing response")
	}
	if r.CorePeerID == "" {
		return fmt.Errorf("dollnetwork: missing core_peer_id in pairing response")
	}
	if err := ValidateIPv6AddressOrPrefix(r.BodyIPv6); err != nil {
		return fmt.Errorf("dollnetwork: invalid body overlay ipv6: %w", err)
	}
	if err := ValidateIPv6AddressOrPrefix(r.CoreIPv6); err != nil {
		return fmt.Errorf("dollnetwork: invalid core overlay ipv6: %w", err)
	}
	if _, err := DecodeWgPublicKey(r.CoreWGKeyB64); err != nil {
		return fmt.Errorf("dollnetwork: invalid core wg public key: %w", err)
	}
	if err := r.AllEndpointsValidate(); err != nil {
		return fmt.Errorf("dollnetwork: invalid endpoint in pairing response: %w", err)
	}
	return nil
}

// AllEndpointsValidate returns nil only if every endpoint is structurally
// valid for its declared type.
func (r *PairResponse) AllEndpointsValidate() error {
	for _, e := range r.Endpoints {
		if err := e.ValidBootstrapEndpoint(); err != nil {
			return err
		}
	}
	return nil
}

// PairingError represents a structured error returned by Core for a failed
// pairing request (non-2xx HTTP response). The body is expected to be a JSON
// object of this type.
type PairingError struct {
	Version int    `json:"version"` // should equal ProtocolVersion
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// FromJSON parses a PairingError from JSON.
func (e *PairingError) FromJSON(data []byte) error {
	if err := json.Unmarshal(data, e); err != nil {
		return fmt.Errorf("dollnetwork: failed to parse pairing error: %w", err)
	}
	return nil
}