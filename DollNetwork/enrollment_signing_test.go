package dollnetwork

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"testing"
)

func TestSignedMessageUsesExactPortableLengthPrefixEncoding(t *testing.T) {
	message, err := SignedMessage("example", SignedField{Value: []byte("abc")}, SignedField{Value: []byte{0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte("NEONDOLL-SIGN-V1\x00"), 0, 0, 0, 7)
	want = append(want, "example"...)
	want = append(want, 0, 0, 0, 3)
	want = append(want, "abc"...)
	want = append(want, 0, 0, 0, 2, 0, 1)
	if !bytes.Equal(message, want) {
		t.Fatalf("signed bytes\n got: %x\nwant: %x", message, want)
	}
	if got := binary.BigEndian.Uint32(message[len(signingDomain):]); got != 7 {
		t.Fatalf("message type length = %d", got)
	}
}

func TestEnrollmentBeginSignatureBindsCoreRequestKeysAndNonce(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wg := make([]byte, 32)
	nonce := make([]byte, 32)
	_, _ = rand.Read(wg)
	_, _ = rand.Read(nonce)
	request := EnrollmentBeginRequest{
		Version: 1, RequestID: "request", CoreID: "core",
		BodyPublicKey: EncodeRawURL(publicKey), WGPublicKey: base64.StdEncoding.EncodeToString(wg), BodyNonce: EncodeRawURL(nonce),
	}
	if err := SignEnrollmentBegin(privateKey, &request); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEnrollmentBegin(request); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*EnrollmentBeginRequest){
		func(value *EnrollmentBeginRequest) { value.CoreID = "other-core" },
		func(value *EnrollmentBeginRequest) { value.RequestID = "other-request" },
		func(value *EnrollmentBeginRequest) {
			value.WGPublicKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
		},
	}
	for index, mutate := range mutations {
		changed := request
		mutate(&changed)
		if err := VerifyEnrollmentBegin(changed); err == nil {
			t.Fatalf("mutation %d verified", index)
		}
	}
}

func TestSignedFieldsRejectUnpaddedAndWrongLengthKeyMaterial(t *testing.T) {
	request := EnrollmentBeginRequest{
		Version: 1, RequestID: "request", CoreID: "core", BodyPublicKey: "bad",
		WGPublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32)), BodyNonce: EncodeRawURL(make([]byte, 32)), Signature: "bad",
	}
	if err := VerifyEnrollmentBegin(request); err == nil {
		t.Fatal("malformed public key accepted")
	}
	if _, err := DecodeNonce(base64.URLEncoding.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("padded nonce accepted")
	}
}
