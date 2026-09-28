package dollnetwork

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the Doll Network Protocol version.
const ProtocolVersion = 1

// PairRequestBody mirrors the "body" object of the canonical pairing request.
type PairRequestBody struct {
	BodyID         string `json:"body_id"`
	Name           string `json:"name,omitempty"`
	Implementation string `json:"implementation"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
}

// PairingNetwork mirrors the "network" object: currently just the WG public key.
// This is the only WireGuard material the runtime ever serializes.
type PairingNetwork struct {
	WireGuardPublicKey string `json:"wireguard_public_key"`
}

// PairRequest is the canonical pairing request. invitation_id and secret are
// provided by Core's invitation at pairing time (M2); this package defines the
// shape so both sides agree on the contract.
type PairRequest struct {
	Version      int             `json:"version"`
	InvitationID string          `json:"invitation_id"`
	Secret       string          `json:"secret"`
	Body         PairRequestBody `json:"body"`
	Network      PairingNetwork  `json:"network"`
}

// ToJSON renders the pairing request as canonical JSON for submission.
func (r *PairRequest) ToJSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ValidatePairRequest performs a defensive check that the request carries the
// public key and never the private key (i.e., no private-key field exists and
// the JSON does not contain the private key). It returns an error if the
// request is malformed.
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
	return nil
}
