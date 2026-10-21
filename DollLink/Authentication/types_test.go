package authentication

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

func TestAuthenticationSignaturesBindCanonicalTranscripts(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bodyNonce := make([]byte, 32)
	coreNonce := make([]byte, 32)
	_, _ = rand.Read(bodyNonce)
	_, _ = rand.Read(coreNonce)
	challenge := Challenge{
		Version: Version, Type: TypeChallenge, CoreID: "core-1", BodyID: "body-1", SessionID: "session-1",
		BodyNonce: dollnetwork.EncodeRawURL(bodyNonce), CoreNonce: dollnetwork.EncodeRawURL(coreNonce),
		ExpiresAt: time.Date(2026, 11, 1, 12, 0, 30, 0, time.UTC), Algorithm: AlgorithmEd25519,
	}
	if err := SignChallenge(privateKey, &challenge); err != nil {
		t.Fatal(err)
	}
	if err := VerifyChallenge(publicKey, challenge); err != nil {
		t.Fatal(err)
	}
	tampered := challenge
	tampered.SessionID = "session-2"
	if err := VerifyChallenge(publicKey, tampered); err == nil {
		t.Fatal("tampered challenge verified")
	}

	proof := Proof{
		Version: Version, Type: TypeProof, CoreID: challenge.CoreID, BodyID: challenge.BodyID,
		SessionID: challenge.SessionID, BodyNonce: challenge.BodyNonce, CoreNonce: challenge.CoreNonce,
		Algorithm: challenge.Algorithm,
	}
	if err := SignProof(privateKey, &proof); err != nil {
		t.Fatal(err)
	}
	if err := VerifyProof(publicKey, proof); err != nil {
		t.Fatal(err)
	}

	accept := Accept{Version: Version, Type: TypeAccept, CoreID: challenge.CoreID, BodyID: challenge.BodyID, SessionID: challenge.SessionID, Status: "authenticated"}
	if err := SignAccept(privateKey, &accept); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAccept(publicKey, accept); err != nil {
		t.Fatal(err)
	}
	accept.BodyID = "body-2"
	if err := VerifyAccept(publicKey, accept); err == nil {
		t.Fatal("tampered accept verified")
	}
}
