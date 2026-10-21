// Package authentication defines the canonical Body Authentication v1 frames.
package authentication

import (
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

const (
	Version          = 1
	AlgorithmEd25519 = "Ed25519"
	TypeHello        = "body.auth.hello"
	TypeChallenge    = "body.auth.challenge"
	TypeProof        = "body.auth.proof"
	TypeAccept       = "body.auth.accept"
)

type Hello struct {
	Version    int      `json:"version"`
	Type       string   `json:"type"`
	BodyID     string   `json:"body_id"`
	BodyNonce  string   `json:"body_nonce"`
	Algorithms []string `json:"algorithms"`
}
type Challenge struct {
	Version   int       `json:"version"`
	Type      string    `json:"type"`
	CoreID    string    `json:"core_id"`
	BodyID    string    `json:"body_id"`
	SessionID string    `json:"session_id"`
	BodyNonce string    `json:"body_nonce"`
	CoreNonce string    `json:"core_nonce"`
	ExpiresAt time.Time `json:"expires_at"`
	Algorithm string    `json:"algorithm"`
	Signature string    `json:"signature"`
}
type Proof struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	CoreID    string `json:"core_id"`
	BodyID    string `json:"body_id"`
	SessionID string `json:"session_id"`
	BodyNonce string `json:"body_nonce"`
	CoreNonce string `json:"core_nonce"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}
type Accept struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	CoreID    string `json:"core_id"`
	BodyID    string `json:"body_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Signature string `json:"signature"`
}

func challengeFields(value Challenge) ([]dollnetwork.SignedField, error) {
	return authFields(value.CoreID, value.BodyID, value.SessionID, value.BodyNonce, value.CoreNonce, value.ExpiresAt.UTC().Format(time.RFC3339), value.Algorithm)
}
func proofFields(value Proof) ([]dollnetwork.SignedField, error) {
	return authFields(value.CoreID, value.BodyID, value.SessionID, value.BodyNonce, value.CoreNonce, value.Algorithm)
}
func acceptFields(value Accept) ([]dollnetwork.SignedField, error) {
	return authFields(value.CoreID, value.BodyID, value.SessionID, value.Status)
}

func authFields(values ...string) ([]dollnetwork.SignedField, error) {
	fields := make([]dollnetwork.SignedField, 0, len(values))
	for index, value := range values {
		var field dollnetwork.SignedField
		var err error
		if (len(values) == 7 && (index == 3 || index == 4)) || (len(values) == 6 && (index == 3 || index == 4)) {
			var raw []byte
			raw, err = dollnetwork.DecodeNonce(value)
			if err == nil {
				field, err = dollnetwork.RawField(raw)
			}
		} else {
			field, err = dollnetwork.StringField(value)
		}
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func SignChallenge(key ed25519.PrivateKey, value *Challenge) error {
	fields, err := challengeFields(*value)
	if err != nil {
		return err
	}
	return sign(key, TypeChallenge, &value.Signature, fields)
}
func VerifyChallenge(key ed25519.PublicKey, value Challenge) error {
	fields, err := challengeFields(value)
	if err != nil {
		return err
	}
	return verify(key, TypeChallenge, value.Signature, fields)
}
func SignProof(key ed25519.PrivateKey, value *Proof) error {
	fields, err := proofFields(*value)
	if err != nil {
		return err
	}
	return sign(key, TypeProof, &value.Signature, fields)
}
func VerifyProof(key ed25519.PublicKey, value Proof) error {
	fields, err := proofFields(value)
	if err != nil {
		return err
	}
	return verify(key, TypeProof, value.Signature, fields)
}
func SignAccept(key ed25519.PrivateKey, value *Accept) error {
	fields, err := acceptFields(*value)
	if err != nil {
		return err
	}
	return sign(key, TypeAccept, &value.Signature, fields)
}
func VerifyAccept(key ed25519.PublicKey, value Accept) error {
	fields, err := acceptFields(value)
	if err != nil {
		return err
	}
	return verify(key, TypeAccept, value.Signature, fields)
}

func sign(key ed25519.PrivateKey, kind string, destination *string, fields []dollnetwork.SignedField) error {
	message, err := dollnetwork.SignedMessage(kind, fields...)
	if err != nil {
		return err
	}
	*destination = dollnetwork.EncodeRawURL(ed25519.Sign(key, message))
	return nil
}
func verify(key ed25519.PublicKey, kind, signature string, fields []dollnetwork.SignedField) error {
	raw, err := dollnetwork.DecodeSignature(signature)
	if err != nil {
		return err
	}
	message, err := dollnetwork.SignedMessage(kind, fields...)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, message, raw) {
		return errors.New("authentication: invalid signature")
	}
	return nil
}
