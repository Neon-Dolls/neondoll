package dollnetwork

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

const signingDomain = "NEONDOLL-SIGN-V1\x00"

const (
	maxSignedString = 256
	nonceSize       = 32
)

type SignedField struct {
	Value []byte
}

func StringField(value string) (SignedField, error) {
	if value == "" || len([]byte(value)) > maxSignedString {
		return SignedField{}, fmt.Errorf("dollnetwork: signed string length must be 1..%d bytes", maxSignedString)
	}
	return SignedField{Value: []byte(value)}, nil
}

func RawField(value []byte) (SignedField, error) {
	if len(value) == 0 || len(value) > maxSignedString {
		return SignedField{}, fmt.Errorf("dollnetwork: signed field length must be 1..%d bytes", maxSignedString)
	}
	return SignedField{Value: append([]byte(nil), value...)}, nil
}

// SignedMessage implements the portable v1 length-prefixed transcript. JSON
// bytes are never passed to this function or signed.
func SignedMessage(messageType string, fields ...SignedField) ([]byte, error) {
	typeField, err := StringField(messageType)
	if err != nil {
		return nil, err
	}
	total := len(signingDomain) + 4 + len(typeField.Value)
	for _, field := range fields {
		if len(field.Value) == 0 || len(field.Value) > maxSignedString {
			return nil, errors.New("dollnetwork: invalid signed field length")
		}
		total += 4 + len(field.Value)
	}
	message := make([]byte, 0, total)
	message = append(message, []byte(signingDomain)...)
	message = appendField(message, typeField.Value)
	for _, field := range fields {
		message = appendField(message, field.Value)
	}
	return message, nil
}

func appendField(dst, field []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(field)))
	dst = append(dst, length[:]...)
	return append(dst, field...)
}

func DecodeBodyPublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("dollnetwork: invalid Ed25519 public key")
	}
	return ed25519.PublicKey(raw), nil
}

func DecodeNonce(encoded string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != nonceSize {
		return nil, errors.New("dollnetwork: invalid nonce")
	}
	return raw, nil
}

func DecodeSignature(encoded string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return nil, errors.New("dollnetwork: invalid Ed25519 signature")
	}
	return raw, nil
}

func EncodeRawURL(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func BodyKeyFingerprint(publicKey ed25519.PublicKey) string {
	hash := sha256.Sum256(publicKey)
	return hex.EncodeToString(hash[:])
}

func VerifyEnrollmentBegin(request EnrollmentBeginRequest) error {
	if request.Version != EnrollmentVersion {
		return errors.New("dollnetwork: unsupported enrollment version")
	}
	publicKey, err := DecodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	wgKey, err := DecodeWgPublicKey(request.WGPublicKey)
	if err != nil {
		return err
	}
	nonce, err := DecodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	fields, err := enrollmentKeyProofFields(request.CoreID, request.RequestID, publicKey, wgKey, nonce)
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.begin", request.Signature, fields...)
}

func SignEnrollmentBegin(privateKey ed25519.PrivateKey, request *EnrollmentBeginRequest) error {
	publicKey, err := DecodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	wgKey, err := DecodeWgPublicKey(request.WGPublicKey)
	if err != nil {
		return err
	}
	nonce, err := DecodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	f, err := enrollmentKeyProofFields(request.CoreID, request.RequestID, publicKey, wgKey, nonce)
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.begin", &request.Signature, f...)
}

func VerifyEnrollmentRedeem(request EnrollmentRedeemRequest) error {
	if request.Version != EnrollmentVersion {
		return errors.New("dollnetwork: unsupported enrollment version")
	}
	publicKey, err := DecodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	wgKey, err := DecodeWgPublicKey(request.WGPublicKey)
	if err != nil {
		return err
	}
	nonce, err := DecodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	values := []any{request.CoreID, request.RequestID, request.InvitationID, request.InvitationSecret, publicKey, wgKey, nonce}
	fields, err := fields(values...)
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.redeem", request.Signature, fields...)
}

func SignEnrollmentRedeem(privateKey ed25519.PrivateKey, request *EnrollmentRedeemRequest) error {
	publicKey, err := DecodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	wgKey, err := DecodeWgPublicKey(request.WGPublicKey)
	if err != nil {
		return err
	}
	nonce, err := DecodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	f, err := fields(request.CoreID, request.RequestID, request.InvitationID, request.InvitationSecret, publicKey, wgKey, nonce)
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.redeem", &request.Signature, f...)
}

func VerifyEnrollmentResult(request EnrollmentResultRequest) error {
	if request.Version != EnrollmentVersion {
		return errors.New("dollnetwork: unsupported enrollment version")
	}
	publicKey, err := DecodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	nonce, err := DecodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	f, err := fields(request.CoreID, request.RequestID, publicKey, nonce)
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.result.request", request.Signature, f...)
}

func SignEnrollmentResult(privateKey ed25519.PrivateKey, request *EnrollmentResultRequest) error {
	publicKey, err := DecodeBodyPublicKey(request.BodyPublicKey)
	if err != nil {
		return err
	}
	nonce, err := DecodeNonce(request.BodyNonce)
	if err != nil {
		return err
	}
	f, err := fields(request.CoreID, request.RequestID, publicKey, nonce)
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.result.request", &request.Signature, f...)
}

func SignEnrollmentPending(privateKey ed25519.PrivateKey, response *EnrollmentPendingResponse) error {
	coreNonce, err := DecodeNonce(response.CoreNonce)
	if err != nil {
		return err
	}
	f, err := fields(response.CoreID, response.RequestID, response.BodyKeyFingerprint, coreNonce, response.ApprovalCode, response.ExpiresAt.UTC().Format(timeFormat))
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.pending", &response.Signature, f...)
}

func VerifyEnrollmentPending(publicKey ed25519.PublicKey, response EnrollmentPendingResponse) error {
	coreNonce, err := DecodeNonce(response.CoreNonce)
	if err != nil {
		return err
	}
	f, err := fields(response.CoreID, response.RequestID, response.BodyKeyFingerprint, coreNonce, response.ApprovalCode, response.ExpiresAt.UTC().Format(timeFormat))
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.pending", response.Signature, f...)
}

func SignEnrollmentReceipt(privateKey ed25519.PrivateKey, receipt *EnrollmentReceipt) error {
	f, err := fields(receipt.CoreID, receipt.RequestID, receipt.BodyID, receipt.BodyPublicKeyFingerprint, receipt.MembershipReference, receipt.IssuedAt.UTC().Format(timeFormat))
	if err != nil {
		return err
	}
	return sign(privateKey, "enrollment.receipt", &receipt.Signature, f...)
}

func VerifyEnrollmentReceipt(publicKey ed25519.PublicKey, receipt EnrollmentReceipt) error {
	f, err := fields(receipt.CoreID, receipt.RequestID, receipt.BodyID, receipt.BodyPublicKeyFingerprint, receipt.MembershipReference, receipt.IssuedAt.UTC().Format(timeFormat))
	if err != nil {
		return err
	}
	return verify(publicKey, "enrollment.receipt", receipt.Signature, f...)
}

const timeFormat = "2006-01-02T15:04:05Z07:00"

func enrollmentKeyProofFields(coreID, requestID string, publicKey, wgKey, nonce []byte) ([]SignedField, error) {
	return fields(coreID, requestID, publicKey, wgKey, nonce)
}

func fields(values ...any) ([]SignedField, error) {
	result := make([]SignedField, 0, len(values))
	for _, value := range values {
		var field SignedField
		var err error
		switch value := value.(type) {
		case string:
			field, err = StringField(value)
		case []byte:
			field, err = RawField(value)
		case ed25519.PublicKey:
			field, err = RawField(value)
		default:
			err = fmt.Errorf("dollnetwork: unsupported signed field %T", value)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, field)
	}
	return result, nil
}

func sign(privateKey ed25519.PrivateKey, messageType string, destination *string, fields ...SignedField) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("dollnetwork: invalid Ed25519 private key")
	}
	message, err := SignedMessage(messageType, fields...)
	if err != nil {
		return err
	}
	*destination = EncodeRawURL(ed25519.Sign(privateKey, message))
	return nil
}

func verify(publicKey ed25519.PublicKey, messageType, encodedSignature string, fields ...SignedField) error {
	signature, err := DecodeSignature(encodedSignature)
	if err != nil {
		return err
	}
	message, err := SignedMessage(messageType, fields...)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, message, signature) {
		return errors.New("dollnetwork: invalid proof")
	}
	return nil
}
