package dollnetwork

import "time"

const EnrollmentVersion = 1

const (
	EnrollmentStatusPending  = "pending"
	EnrollmentStatusDenied   = "denied"
	EnrollmentStatusExpired  = "expired"
	EnrollmentStatusEnrolled = "enrolled"
)

type EnrollmentMetadata map[string]string

type EnrollmentBeginRequest struct {
	Version       int                `json:"version"`
	RequestID     string             `json:"request_id"`
	CoreID        string             `json:"core_id"`
	BodyPublicKey string             `json:"body_public_key"`
	WGPublicKey   string             `json:"wg_public_key"`
	BodyNonce     string             `json:"body_nonce"`
	Metadata      EnrollmentMetadata `json:"metadata,omitempty"`
	Signature     string             `json:"signature"`
}

type EnrollmentRedeemRequest struct {
	Version          int                `json:"version"`
	RequestID        string             `json:"request_id"`
	CoreID           string             `json:"core_id"`
	InvitationID     string             `json:"invitation_id"`
	InvitationSecret string             `json:"invitation_secret"`
	BodyPublicKey    string             `json:"body_public_key"`
	WGPublicKey      string             `json:"wg_public_key"`
	BodyNonce        string             `json:"body_nonce"`
	Metadata         EnrollmentMetadata `json:"metadata,omitempty"`
	Signature        string             `json:"signature"`
}

type EnrollmentPendingResponse struct {
	Version            int       `json:"version"`
	RequestID          string    `json:"request_id"`
	CoreID             string    `json:"core_id"`
	BodyKeyFingerprint string    `json:"body_key_fingerprint"`
	CoreNonce          string    `json:"core_nonce"`
	ApprovalCode       string    `json:"approval_code"`
	ExpiresAt          time.Time `json:"expires_at"`
	Signature          string    `json:"signature"`
}

type EnrollmentResultRequest struct {
	Version       int    `json:"version"`
	RequestID     string `json:"request_id"`
	CoreID        string `json:"core_id"`
	BodyPublicKey string `json:"body_public_key"`
	BodyNonce     string `json:"body_nonce"`
	Signature     string `json:"signature"`
}

type EnrollmentReceipt struct {
	Version                  int       `json:"version"`
	RequestID                string    `json:"request_id"`
	Status                   string    `json:"status"`
	BodyID                   string    `json:"body_id"`
	CoreID                   string    `json:"core_id"`
	BodyPublicKeyFingerprint string    `json:"body_public_key_fingerprint"`
	Permissions              []string  `json:"permissions"`
	MembershipReference      string    `json:"membership_reference"`
	IssuedAt                 time.Time `json:"issued_at"`
	Signature                string    `json:"signature"`
}

type EnrollmentResultResponse struct {
	Version   int                `json:"version"`
	RequestID string             `json:"request_id"`
	Status    string             `json:"status"`
	Receipt   *EnrollmentReceipt `json:"receipt,omitempty"`
}

type EnrollmentError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type EnrollmentErrorResponse struct {
	Version int             `json:"version"`
	Error   EnrollmentError `json:"error"`
}
