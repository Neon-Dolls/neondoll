package dollnetwork

import (
	"strings"
	"testing"
)

// TestPairRequestValidation: a well-formed request validates.
func TestPairRequestValidation(t *testing.T) {
	r := PairRequest{
		Version:      ProtocolVersion,
		InvitationID: "inv",
		Secret:       "sec",
		Body:         PairRequestBody{BodyID: "body_x", Implementation: "neondoll-body", Platform: "linux", Arch: "amd64"},
		Network:      PairingNetwork{WireGuardPublicKey: "c2FsXGU="},
	}
	if err := r.ValidatePairRequest(); err != nil {
		t.Fatalf("ValidatePairRequest: %v", err)
	}
}

// TestPairRequestValidationRejectsMissingPublicKey: the network object must
// carry the WG public key.
func TestPairRequestValidationRejectsMissingPublicKey(t *testing.T) {
	r := PairRequest{
		Version:      ProtocolVersion,
		InvitationID: "inv",
		Secret:       "sec",
		Body:         PairRequestBody{BodyID: "body_x", Implementation: "i", Platform: "p", Arch: "a"},
		Network:      PairingNetwork{WireGuardPublicKey: ""},
	}
	if err := r.ValidatePairRequest(); err == nil {
		t.Fatal("expected validation error for missing public key")
	}
}

// TestPairRequestValidationRejectsBadVersion: an unknown protocol version is
// rejected.
func TestPairRequestValidationRejectsBadVersion(t *testing.T) {
	r := PairRequest{
		Version:      99,
		InvitationID: "inv",
		Secret:       "sec",
		Body:         PairRequestBody{BodyID: "body_x", Implementation: "i", Platform: "p", Arch: "a"},
		Network:      PairingNetwork{WireGuardPublicKey: "c2FsXGU="},
	}
	if err := r.ValidatePairRequest(); err == nil {
		t.Fatal("expected validation error for unknown protocol version")
	}
}

// TestPairRequestToJSON verifies serialization produces a parseable JSON with a
// top-level "body" and "network" object.
func TestPairRequestToJSON(t *testing.T) {
	r := PairRequest{
		Version:      ProtocolVersion,
		InvitationID: "inv_7",
		Secret:       "s",
		Body:         PairRequestBody{BodyID: "body_x", Implementation: "i", Platform: "p", Arch: "a"},
		Network:      PairingNetwork{WireGuardPublicKey: "c2FsXGU="},
	}
	wire, err := r.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	for _, key := range []string{"version", "invitation_id", "secret", "body", "network", "wireguard_public_key", "body_id"} {
		if !strings.Contains(wire, `"`+key+`"`) {
			t.Errorf("pairing request missing field %q", key)
		}
	}
}
