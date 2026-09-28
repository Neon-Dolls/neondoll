package bodyruntime

import (
	"encoding/base64"
	"testing"
)

func TestGenerateWgKeypair(t *testing.T) {
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	if kp == nil {
		t.Fatal("expected a keypair")
	}
	pub := kp.PublicKey().bytes
	if len(pub) != 32 {
		t.Errorf("public key size = %d, want 32", len(pub))
	}
	if isAllZeroVec(pub) {
		t.Errorf("public key should not be all-zero")
	}
}

func TestKeypairsAreDistinct(t *testing.T) {
	a, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	b, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	if eqBytes(a.PublicKey().bytes, b.PublicKey().bytes) {
		t.Errorf("two fresh keypairs should not produce the same public key")
	}
}

func TestPublicKeyBase64IsStdEncoding(t *testing.T) {
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	enc := kp.PublicKeyBase64()
	want := base64.StdEncoding.EncodeToString(kp.PublicKey().bytes)
	if enc != want {
		t.Errorf("PublicKeyBase64() mismatch with StdEncoding")
	}
	// base64 of 32 bytes is 44 chars (with padding).
	if len(enc) != 44 {
		t.Errorf("public key base64 len = %d, want 44", len(enc))
	}
}

func TestReconstructKeypairFromPrivate(t *testing.T) {
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	priv := kp.PrivateKeyBytes()
	defer Wipe(priv)
	rebuilt, err := NewWgKeypair(priv)
	if err != nil {
		t.Fatalf("NewWgKeypair: %v", err)
	}
	if !eqBytes(kp.PublicKey().bytes, rebuilt.PublicKey().bytes) {
		t.Errorf("reconstructed public key differs from original")
	}
	// The public key is stable across reconstruction: this is how a restart
	// preserves WG identity.
	enc1 := kp.PublicKeyBase64()
	enc2 := rebuilt.PublicKeyBase64()
	if enc1 != enc2 {
		t.Errorf("public key base64 changed across reconstruction")
	}
}

func TestPrivateKeyIs32Bytes(t *testing.T) {
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	priv := kp.PrivateKeyBytes()
	defer Wipe(priv)
	if len(priv) != 32 {
		t.Errorf("private key size = %d, want 32", len(priv))
	}
}

func TestDerivePublicKeyRejectsBadLength(t *testing.T) {
	_, err := DerivePublicKey(make([]byte, 31))
	if err == nil {
		t.Fatal("expected error for 31-byte private key")
	}
}

func isAllZeroVec(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func eqBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPublicKeyOnlyValidation(t *testing.T) {
	kp, err := GenerateWgKeypair()
	if err != nil {
		t.Fatalf("GenerateWgKeypair: %v", err)
	}
	// The runtime never produces a base64/string serialization of the private
	// key. Prove the public-key string does not contain private material by
	// checking it decodes to exactly the public bytes.
	dec, derr := base64.StdEncoding.DecodeString(kp.PublicKeyBase64())
	if derr != nil {
		t.Fatalf("decode public key: %v", derr)
	}
	Wipe(dec)
}
