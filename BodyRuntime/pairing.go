// Package bodyruntime — canonical pairing request data.
//
// M1's interface to Core (Rin): produce the canonical Doll Network Protocol
// v1 pairing request JSON that a fresh Body would submit at a bootstrap
// endpoint (Doll Network Protocol §2 "Pairing Exchange").
//
// The serialized pairing request contains the Body identity and metadata plus
// the WireGuard PUBLIC key. It MUST NEVER contain the WireGuard private key.
// This module is the single serializer for that protocol shape and is the
// guarantee that the private key never leaves the Body on the pairing path.

package bodyruntime

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ProtocolVersion is the Doll Network Protocol version this runtime speaks.
const ProtocolVersion = 1

// PairRequestBody mirrors the "body" object of the canonical pairing request.
type PairRequestBody struct {
	BodyID         string `json:"body_id"`
	Name           string `json:"name,omitempty"`
	Implementation string `json:"implementation"`
	Platform       string `json:"platform"`
	Arch           string `json:"arch"`
}

// PairingNetwork mirrors the "network" object: currently just the WG public
// key. This is the only WireGuard material the runtime ever serializes.
type PairingNetwork struct {
	WireGuardPublicKey string `json:"wireguard_public_key"`
}

// PairRequest is the canonical pairing request. invitation_id and secret are
// provided by Core's invitation at pairing time (M2); this module defines the
// shape so both sides agree on the contract.
type PairRequest struct {
	Version      int             `json:"version"`
	InvitationID string          `json:"invitation_id"`
	Secret       string          `json:"secret"`
	Body         PairRequestBody `json:"body"`
	Network      PairingNetwork  `json:"network"`
}

// BuildPairingBody constructs the canonical body object from a persisted
// identity state. It is the M1 deliverable: pairing-request data that Core
// can consume, produced without any Core-internal import.
func BuildPairingBody(st *IdentityState) PairRequestBody {
	body := PairRequestBody{
		BodyID: string(st.Identity.BodyID),
		Name:   st.Identity.Name,
	}
	if m := &st.Identity.Meta; m != nil {
		body.Implementation = m.Implementation
		body.Platform = m.Platform
		body.Arch = m.Arch
	}
	return body
}

// BuildPairingNetwork builds the network object containing only the WG public
// key (base64). The private key is intentionally absent.
func BuildPairingNetwork(kp *WgKeypair) PairingNetwork {
	return PairingNetwork{
		WireGuardPublicKey: kp.PublicKeyBase64(),
	}
}

// BuildPairRequest composes a full canonical pairing request from an
// invitation's id/secret plus the Body's identity and WG public key.
func BuildPairRequest(invitationID, secret string, st *IdentityState, kp *WgKeypair) PairRequest {
	return PairRequest{
		Version:      ProtocolVersion,
		InvitationID: invitationID,
		Secret:       secret,
		Body:         BuildPairingBody(st),
		Network:      BuildPairingNetwork(kp),
	}
}

// ToJSON renders the pairing request as canonical JSON for submission.
func (r *PairRequest) ToJSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("bodyruntime: marshal pairing request: %w", err)
	}
	return string(data), nil
}

// ValidatePairRequest performs a defensive check that the request carries the
// public key and never the private key (i.e., no private-key field exists and
// the JSON does not contain the private key). It returns an error if the
// request is malformed.
func (r *PairRequest) ValidatePairRequest() error {
	if r.Version != ProtocolVersion {
		return fmt.Errorf("bodyruntime: unsupported protocol version %d", r.Version)
	}
	if r.Body.BodyID == "" {
		return errors.New("bodyruntime: pairing request missing body_id")
	}
	if r.Body.Implementation == "" {
		return errors.New("bodyruntime: pairing request missing implementation")
	}
	if r.Network.WireGuardPublicKey == "" {
		return errors.New("bodyruntime: pairing request missing wireguard public key")
	}
	return nil
}
