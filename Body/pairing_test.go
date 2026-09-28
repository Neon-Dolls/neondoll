package body

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

func mkMeta_() BodyMetadata {
	return BodyMetadata{Implementation: "neondoll-body", Platform: "linux", Arch: "amd64"}
}

func mkIdentity_() (*IdentityState, error) {
	return NewIdentityState("SparkBody", mkMeta_())
}

// TestPairingRequestContainsPublicKeyOnly: the serialized pairing request
// contains the WG public key and never the private key.
func TestPairingRequestContainsPublicKeyOnly(t *testing.T) {
	st, err := mkIdentity_()
	if err != nil {
		t.Fatalf("NewIdentityState: %v", err)
	}
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}

	req := BuildPairRequest("invitation_abc", "secret_xyz", st, kp)
	if err := req.ValidatePairRequest(); err != nil {
		t.Fatalf("ValidatePairRequest: %v", err)
	}
	wire, err := req.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}

	// Public key is present and correct.
	if !strings.Contains(wire, kp.PublicKeyBase64()) {
		t.Error("pairing request missing wireguard_public_key")
	}
	// The private key must not appear anywhere (it is never serialized).
	priv := kp.PrivateKeyBytes()
	privB64 := base64.StdEncoding.EncodeToString(priv)
	defer Wipe(priv)
	if strings.Contains(wire, privB64) {
		t.Error("pairing request leaked the WG private key!")
	}
	// Structural checks: the body carries identity info.
	if !strings.Contains(wire, `"wireguard_public_key"`) {
		t.Error("pairing request missing network.wireguard_public_key field")
	}
	if !strings.Contains(wire, `"body_id"`) {
		t.Error("pairing request missing body.body_id field")
	}
}

// TestPairingRequestCanonicalShape: the serialized shape matches the protocol.
func TestPairingRequestCanonicalShape(t *testing.T) {
	st, err := mkIdentity_()
	if err != nil {
		t.Fatalf("mkIdentity_: %v", err)
	}
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	req := BuildPairRequest("inv_1", "sec_1", st, kp)
	wire, err := req.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}

	// version, invitation_id, secret, body{}, network{} are all present.
	for _, key := range []string{"version", "invitation_id", "secret", "body", "network"} {
		if !strings.Contains(wire, `"`+key+`"`) {
			t.Errorf("pairing request missing top-level field %q", key)
		}
	}
	// body carries implementation/platform/arch.
	for _, key := range []string{"implementation", "platform", "arch"} {
		if !strings.Contains(wire, `"`+key+`"`) {
			t.Errorf("pairing request body missing field %q", key)
		}
	}
}

// TestBodyBuildMatchesIdentity: BuildPairingBody reflects the persisted
// identity (body_id + metadata), confirming the interface to Core.
func TestBodyBuildMatchesIdentity(t *testing.T) {
	st, err := mkIdentity_()
	if err != nil {
		t.Fatalf("mkIdentity_: %v", err)
	}
	body := BuildPairingBody(st)
	if body.BodyID != string(st.Identity.BodyID) {
		t.Errorf("body.body_id = %q, want %q", body.BodyID, st.Identity.BodyID)
	}
	if body.Implementation != "neondoll-body" {
		t.Errorf("body.implementation = %q", body.Implementation)
	}
	if body.Platform != "linux" || body.Arch != "amd64" {
		t.Errorf("body platform/arch mismatch")
	}
}

// TestPairRequestValidation: missing public key fails validation.
func TestPairRequestValidation(t *testing.T) {
	st, err := mkIdentity_()
	if err != nil {
		t.Fatalf("mkIdentity_: %v", err)
	}
	req := dollnetwork.PairRequest{
		Version:      dollnetwork.ProtocolVersion,
		InvitationID: "i",
		Secret:       "s",
		Body:         BuildPairingBody(st),
		Network:      dollnetwork.PairingNetwork{WireGuardPublicKey: ""},
	}
	if err := req.ValidatePairRequest(); err == nil {
		t.Fatal("expected validation error for missing public key")
	}
}

// TestPairRequestToJSONParseable: a real request round-trips as valid JSON.
func TestPairRequestToJSONParseable(t *testing.T) {
	st, err := mkIdentity_()
	if err != nil {
		t.Fatalf("mkIdentity_: %v", err)
	}
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	req := BuildPairRequest("inv", "sec", st, kp)
	wire, err := req.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(wire), &parsed); err != nil {
		t.Fatalf("serialized pairing request is not valid JSON: %v", err)
	}
}

// TestLeakCheckNeedsPrivateKey: the serialized request never contains the WG
// private key.
func TestLeakCheckNeedsPrivateKey(t *testing.T) {
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	priv := kp.PrivateKeyBytes()
	defer Wipe(priv)
	st, _ := mkIdentity_()
	req := BuildPairRequest("i", "s", st, kp)
	wire, err := req.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if strings.Contains(wire, base64.StdEncoding.EncodeToString(priv)) {
		t.Error("pairing request leaked the private key")
	}
}
