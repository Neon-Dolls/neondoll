// Package filetransfer implements Doll Link File Transfer v1 — a
// symmetric, resumable, bounded-memory mechanism for moving immutable byte
// objects between Doll Link peers.
//
// Architectural invariant (Central Separation):
//
//	Doll Link transfers immutable bytes. It does not decide what those bytes
//	mean or authorize their use.
package filetransfer

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ── Identity Types ──────────────────────────────────────────────────────

// FileID identifies one immutable byte object.
type FileID string

// TransferID identifies one attempt to move a file between peers.
type TransferID [16]byte

// String returns the UUID-string representation of the transfer ID.
func (tid TransferID) String() string {
	return uuid.UUID(tid).String()
}

// MarshalText implements encoding.TextMarshaler.
func (tid TransferID) MarshalText() ([]byte, error) {
	return []byte(tid.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (tid *TransferID) UnmarshalText(text []byte) error {
	u, err := uuid.Parse(string(text))
	if err != nil {
		return fmt.Errorf("filetransfer: invalid transfer_id: %w", err)
	}
	copy(tid[:], u[:])
	return nil
}

// TransferIDFromUUID creates a TransferID from a uuid.UUID.
func TransferIDFromUUID(u uuid.UUID) TransferID {
	var tid TransferID
	copy(tid[:], u[:])
	return tid
}

// TransferIDFromBytes creates a TransferID from a 16-byte slice.
func TransferIDFromBytes(b [16]byte) TransferID {
	return TransferID(b)
}

// EmptyTransferID is a zero-value transfer ID.
var EmptyTransferID TransferID

// ── Canonical Cancel/Failure Reasons ────────────────────────────────────

// CancelReason is a canonical file-cancel reason.
type CancelReason string

const (
	ReasonDeclined          CancelReason = "declined"
	ReasonInvalidOffer      CancelReason = "invalid_offer"
	ReasonInvalidOffset     CancelReason = "invalid_offset"
	ReasonProtocolError     CancelReason = "protocol_error"
	ReasonSizeMismatch      CancelReason = "size_mismatch"
	ReasonChecksumMismatch  CancelReason = "checksum_mismatch"
	ReasonInsufficientStore CancelReason = "insufficient_storage"
	ReasonTooLarge          CancelReason = "too_large"
	ReasonUnsupported       CancelReason = "unsupported"
	ReasonCancelled         CancelReason = "cancelled"
	ReasonIOError           CancelReason = "io_error"
)

// ── Control Message Payloads ────────────────────────────────────────────

// Offer is the payload of file.offer.
type Offer struct {
	FileID     FileID            `json:"file_id"`
	TransferID string            `json:"transfer_id"`
	Size       int64             `json:"size"`
	SHA256     string            `json:"sha256"`
	Name       string            `json:"name,omitempty"`
	MediaType  string            `json:"media_type,omitempty"`
	Purpose    string            `json:"purpose,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Accept is the payload of file.accept.
type Accept struct {
	FileID     FileID `json:"file_id"`
	TransferID string `json:"transfer_id"`
	Offset     int64  `json:"offset"`
}

// Reject is the payload of file.reject.
type Reject struct {
	FileID     FileID `json:"file_id"`
	TransferID string `json:"transfer_id"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
}

// Complete is the payload of file.complete.
type Complete struct {
	FileID     FileID `json:"file_id"`
	TransferID string `json:"transfer_id"`
}

// Received is the payload of file.received.
type Received struct {
	FileID     FileID `json:"file_id"`
	TransferID string `json:"transfer_id"`
}

// Cancel is the payload of file.cancel.
type Cancel struct {
	FileID     FileID `json:"file_id"`
	TransferID string `json:"transfer_id"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
}

// ── Validation ──────────────────────────────────────────────────────────

// Validate returns an error if the offer payload has malformed or
// inconsistent fields.
func (o Offer) Validate() error {
	if o.FileID == "" {
		return fmt.Errorf("filetransfer: offer: file_id is required")
	}
	if o.TransferID == "" {
		return fmt.Errorf("filetransfer: offer: transfer_id is required")
	}
	if _, err := uuid.Parse(o.TransferID); err != nil {
		return fmt.Errorf("filetransfer: offer: invalid transfer_id: %w", err)
	}
	if o.Size <= 0 {
		return fmt.Errorf("filetransfer: offer: size must be positive, got %d", o.Size)
	}
	if err := validateSHA256(o.SHA256); err != nil {
		return fmt.Errorf("filetransfer: offer: %w", err)
	}
	return nil
}

// Validate returns an error if the accept payload has invalid fields.
func (a Accept) Validate(bodySize int64) error {
	if a.FileID == "" {
		return fmt.Errorf("filetransfer: accept: file_id is required")
	}
	if a.TransferID == "" {
		return fmt.Errorf("filetransfer: accept: transfer_id is required")
	}
	if a.Offset < 0 {
		return fmt.Errorf("filetransfer: accept: offset must be non-negative, got %d", a.Offset)
	}
	if a.Offset > bodySize {
		return fmt.Errorf("filetransfer: accept: offset %d exceeds size %d", a.Offset, bodySize)
	}
	return nil
}

// Validate returns an error if the reject payload has invalid fields.
func (r Reject) Validate() error {
	if r.FileID == "" {
		return fmt.Errorf("filetransfer: reject: file_id is required")
	}
	if r.TransferID == "" {
		return fmt.Errorf("filetransfer: reject: transfer_id is required")
	}
	return nil
}

// Validate returns an error if the complete payload has invalid fields.
func (c Complete) Validate() error {
	if c.FileID == "" {
		return fmt.Errorf("filetransfer: complete: file_id is required")
	}
	if c.TransferID == "" {
		return fmt.Errorf("filetransfer: complete: transfer_id is required")
	}
	return nil
}

// Validate returns an error if the received payload has invalid fields.
func (r Received) Validate() error {
	if r.FileID == "" {
		return fmt.Errorf("filetransfer: received: file_id is required")
	}
	if r.TransferID == "" {
		return fmt.Errorf("filetransfer: received: transfer_id is required")
	}
	return nil
}

// Validate returns an error if the cancel payload has invalid fields.
func (c Cancel) Validate() error {
	if c.FileID == "" {
		return fmt.Errorf("filetransfer: cancel: file_id is required")
	}
	if c.TransferID == "" {
		return fmt.Errorf("filetransfer: cancel: transfer_id is required")
	}
	return nil
}

// ── NDF1 Binary Frame ───────────────────────────────────────────────────

// NDF1Magic is the expected magic bytes for a v1 file data frame.
const NDF1Magic = "NDF1"

// MaxNDF1Payload is the maximum allowed payload per NDF1 frame (1 MiB).
const MaxNDF1Payload = 1_048_576 // 1 MiB

// RecommendedChunkSize is the recommended sender payload size (256 KiB).
const RecommendedChunkSize = 262_144 // 256 KiB

// NDF1HeaderLen is the fixed header size of an NDF1 frame: 4+16+8+4 = 32.
const NDF1HeaderLen = 32

// NDF1Header represents the parsed header of an NDF1 binary data frame.
//
// Wire format (32 bytes, big-endian):
//
//	 0 ─ 3   magic        "NDF1"
//	 4 ─ 19  transfer_id  raw 128-bit transfer identifier
//	20 ─ 27  offset       uint64 network byte order
//	28 ─ 31  length       uint32 network byte order
//	32 ─     payload      length bytes
type NDF1Header struct {
	TransferID TransferID
	Offset     uint64
	Length     uint32
}

// Encode serialises the header to a 32-byte big-endian byte slice.
func (h *NDF1Header) Encode() []byte {
	buf := make([]byte, NDF1HeaderLen)
	copy(buf[0:4], NDF1Magic)
	copy(buf[4:20], h.TransferID[:])
	binary.BigEndian.PutUint64(buf[20:28], h.Offset)
	binary.BigEndian.PutUint32(buf[28:32], h.Length)
	return buf
}

// DecodeNDF1Header parses a 32-byte header. It returns an error for
// unknown magic or truncated input.
func DecodeNDF1Header(raw []byte) (*NDF1Header, error) {
	if len(raw) < NDF1HeaderLen {
		return nil, fmt.Errorf("filetransfer: NDF1 header too short: %d < %d", len(raw), NDF1HeaderLen)
	}
	if string(raw[0:4]) != NDF1Magic {
		return nil, fmt.Errorf("filetransfer: unknown NDF1 magic: %q", string(raw[0:4]))
	}
	var tid TransferID
	copy(tid[:], raw[4:20])
	return &NDF1Header{
		TransferID: tid,
		Offset:     binary.BigEndian.Uint64(raw[20:28]),
		Length:     binary.BigEndian.Uint32(raw[28:32]),
	}, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

func validateSHA256(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("sha256 must be 64 hex characters, got %d", len(s))
	}
	s = strings.ToLower(s)
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return fmt.Errorf("sha256 contains non-hex character: %q", c)
		}
	}
	return nil
}

// SHA256Hex returns the lowercase hex SHA-256 of data.
func SHA256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
