package dollnetwork

import (
	"strings"
	"testing"
)

// ValidWgPubKeyBase64 is a 32-byte X25519 public key in the shared wire
// encoding (standard base64), for exercising protocol validation.
const ValidWgPubKeyBase64 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

// TestPairRequestValidation: a well-formed request validates.
func TestPairRequestValidation(t *testing.T) {
	r := PairRequest{
		Version:      ProtocolVersion,
		InvitationID: "inv",
		Secret:       "sec",
		Body:         PairRequestBody{BodyID: "body_x", Implementation: "neondoll-body", Platform: "linux", Arch: "amd64"},
		Network:      PairingNetwork{WireGuardPublicKey: ValidWgPubKeyBase64},
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

// TestPairRequestValidationRejectsNonWirePublicKey: a malformed / non-wire
// representation (here a hex-encoded key) must be rejected at the boundary
// and never reach the wire, since the shared wire encoding is base64 of a
// 32-byte key.
func TestPairRequestValidationRejectsNonWirePublicKey(t *testing.T) {
	hexForm := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	if len(hexForm) != 64 {
		t.Fatal("test hex fixture must be 64 chars")
	}
	r := PairRequest{
		Version:      ProtocolVersion,
		InvitationID: "inv",
		Secret:       "sec",
		Body:         PairRequestBody{BodyID: "body_x", Implementation: "i", Platform: "p", Arch: "a"},
		Network:      PairingNetwork{WireGuardPublicKey: hexForm},
	}
	if err := r.ValidatePairRequest(); err == nil {
		t.Fatal("expected validation error for hex-encoded wg public key")
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
		Network:      PairingNetwork{WireGuardPublicKey: ValidWgPubKeyBase64},
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
		Network:      PairingNetwork{WireGuardPublicKey: ValidWgPubKeyBase64},
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
