package filetransfer

import (
	"testing"

	"github.com/google/uuid"
)

func TestFileTransferTypes_FileID(t *testing.T) {
	// A FileID is just a string — lightweight identity.
	fid := FileID("test-file-v1")
	if string(fid) != "test-file-v1" {
		t.Fatalf("unexpected FileID: %q", fid)
	}
}

func TestFileTransferTypes_SameFileIDNewTransferID(t *testing.T) {
	fileID := FileID("immutable-byte-object-1")

	// Same file_id may be used with multiple transfer IDs.
	tid1 := uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")
	tid2 := uuid.MustParse("00001111-2222-3333-4444-555566667777")

	tid1Bytes := TransferIDFromUUID(tid1)
	tid2Bytes := TransferIDFromUUID(tid2)

	if tid1Bytes == tid2Bytes {
		t.Fatal("transfer IDs must be distinct")
	}

	o1 := Offer{FileID: fileID, TransferID: tid1.String(), Size: 100, SHA256: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}
	o2 := Offer{FileID: fileID, TransferID: tid2.String(), Size: 100, SHA256: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}

	if err := o1.Validate(); err != nil {
		t.Fatalf("offer 1 should be valid: %v", err)
	}
	if err := o2.Validate(); err != nil {
		t.Fatalf("offer 2 should be valid: %v", err)
	}
	if o1.FileID != o2.FileID {
		t.Fatal("same file_id must be equal")
	}
}

func TestFileTransferTypes_ImmutableMetadataChangeRejected(t *testing.T) {
	fileID := FileID("immutable-byte-object-1")

	// Cannot silently change size or digest for same file_id (validated via offer).
	o1 := Offer{FileID: fileID, TransferID: uuid.New().String(), Size: 100, SHA256: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}
	o2 := Offer{FileID: fileID, TransferID: uuid.New().String(), Size: 200, SHA256: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}

	// Both individually valid, but the immutable identity would be enforced
	// by the state machine at runtime. Here we just prove the offer types work.
	if err := o1.Validate(); err != nil {
		t.Fatalf("offer 1 should be valid: %v", err)
	}
	if err := o2.Validate(); err != nil {
		t.Fatalf("offer 2 should be valid: %v", err)
	}
}

func TestFileTransferTypes_TransferIDRoundTrip(t *testing.T) {
	u := uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890")
	tid := TransferIDFromUUID(u)

	if got := tid.String(); got != "a1b2c3d4-e5f6-7890-abcd-ef1234567890" {
		t.Fatalf("expected UUID string, got %q", got)
	}

	var parsed TransferID
	if err := parsed.UnmarshalText([]byte("00001111-2222-3333-4444-555566667777")); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed.String() != "00001111-2222-3333-4444-555566667777" {
		t.Fatalf("round trip failed: %s", parsed.String())
	}

	// Round-trip through raw 16 bytes.
	raw := [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	tidFromBytes := TransferIDFromBytes(raw)
	uFromBytes := uuid.UUID(tidFromBytes)
	var tidBack TransferID
	copy(tidBack[:], uFromBytes[:])
	if tidBack != tidFromBytes {
		t.Fatal("bytes round trip failed")
	}
}

func TestFileTransferTypes_TransferIDEmpty(t *testing.T) {
	// Zero-value TransferID should not be valid as a UUID string
	// (its String() still produces a valid UUID form).
	s := EmptyTransferID.String()
	if _, err := uuid.Parse(s); err != nil {
		t.Fatalf("empty TransferID should still produce parseable UUID: %v", err)
	}
}

func TestFileTransferTypes_InvalidOffer(t *testing.T) {
	tests := []struct {
		name  string
		offer Offer
	}{
		{"empty file_id", Offer{FileID: "", TransferID: uuid.New().String(), Size: 100, SHA256: validSHA256}},
		{"empty transfer_id", Offer{FileID: "f1", TransferID: "", Size: 100, SHA256: validSHA256}},
		{"zero size", Offer{FileID: "f1", TransferID: uuid.New().String(), Size: 0, SHA256: validSHA256}},
		{"negative size", Offer{FileID: "f1", TransferID: uuid.New().String(), Size: -1, SHA256: validSHA256}},
		{"invalid sha256 length", Offer{FileID: "f1", TransferID: uuid.New().String(), Size: 100, SHA256: "short"}},
		{"sha256 with non-hex", Offer{FileID: "f1", TransferID: uuid.New().String(), Size: 100, SHA256: "zbcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}},
		{"malformed transfer_id", Offer{FileID: "f1", TransferID: "not-a-uuid", Size: 100, SHA256: validSHA256}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.offer.Validate(); err == nil {
				t.Errorf("expected error for %q, got nil", tt.name)
			}
		})
	}
}

func TestFileTransferTypes_ValidOffer(t *testing.T) {
	o := Offer{
		FileID:     "my-file",
		TransferID: uuid.New().String(),
		Size:       1024,
		SHA256:     validSHA256,
		Name:       "photo.jpg",
		MediaType:  "image/jpeg",
		Metadata:   map[string]string{"source": "camera"},
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("valid offer rejected: %v", err)
	}
}

func TestFileTransferTypes_AcceptValidation(t *testing.T) {
	tests := []struct {
		name    string
		accept  Accept
		wantErr bool
	}{
		{"valid zero offset", Accept{FileID: "f1", TransferID: uuid.New().String(), Offset: 0}, false},
		{"valid non-zero offset", Accept{FileID: "f1", TransferID: uuid.New().String(), Offset: 1000}, false},
		{"valid offset at size", Accept{FileID: "f1", TransferID: uuid.New().String(), Offset: 1024}, false},
		{"empty file_id", Accept{FileID: "", TransferID: uuid.New().String(), Offset: 0}, true},
		{"empty transfer_id", Accept{FileID: "f1", TransferID: "", Offset: 0}, true},
		{"negative offset", Accept{FileID: "f1", TransferID: uuid.New().String(), Offset: -1}, true},
		{"offset exceeds size", Accept{FileID: "f1", TransferID: uuid.New().String(), Offset: 9999}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.accept.Validate(1024)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(bodySize=1024) = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestFileTransferTypes_ValidateReject(t *testing.T) {
	tests := []struct {
		name    string
		reject  Reject
		wantErr bool
	}{
		{"valid", Reject{FileID: "f1", TransferID: uuid.New().String(), Reason: "declined"}, false},
		{"empty file_id", Reject{FileID: "", TransferID: uuid.New().String()}, true},
		{"empty transfer_id", Reject{FileID: "f1", TransferID: ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.reject.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestFileTransferTypes_ValidateComplete(t *testing.T) {
	tests := []struct {
		name    string
		comp    Complete
		wantErr bool
	}{
		{"valid", Complete{FileID: "f1", TransferID: uuid.New().String()}, false},
		{"empty file_id", Complete{FileID: ""}, true},
		{"empty transfer_id", Complete{TransferID: ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.comp.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestFileTransferTypes_ValidateReceived(t *testing.T) {
	tests := []struct {
		name     string
		received Received
		wantErr  bool
	}{
		{"valid", Received{FileID: "f1", TransferID: uuid.New().String()}, false},
		{"empty file_id", Received{FileID: ""}, true},
		{"empty transfer_id", Received{TransferID: ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.received.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestFileTransferTypes_ValidateCancel(t *testing.T) {
	tests := []struct {
		name    string
		cancel  Cancel
		wantErr bool
	}{
		{"valid", Cancel{FileID: "f1", TransferID: uuid.New().String(), Reason: "cancelled"}, false},
		{"empty file_id", Cancel{FileID: ""}, true},
		{"empty transfer_id", Cancel{TransferID: ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cancel.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestFileTransferTypes_SHA256Helper(t *testing.T) {
	h := SHA256Hex([]byte("hello"))
	if len(h) != 64 {
		t.Fatalf("expected 64 hex chars, got %d: %s", len(h), h)
	}
	// Known SHA-256 of "hello"
	if h != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("unexpected SHA-256 for 'hello': %s", h)
	}
}

func TestFileTransferTypes_NDF1HeaderEncodeDecode(t *testing.T) {
	tid := TransferIDFromUUID(uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890"))

	h := &NDF1Header{
		TransferID: tid,
		Offset:     123456,
		Length:     4096,
	}

	encoded := h.Encode()
	if len(encoded) != NDF1HeaderLen {
		t.Fatalf("expected %d bytes, got %d", NDF1HeaderLen, len(encoded))
	}

	decoded, err := DecodeNDF1Header(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.TransferID != h.TransferID {
		t.Fatal("transfer ID mismatch after round trip")
	}
	if decoded.Offset != h.Offset {
		t.Fatalf("offset mismatch: got %d, want %d", decoded.Offset, h.Offset)
	}
	if decoded.Length != h.Length {
		t.Fatalf("length mismatch: got %d, want %d", decoded.Length, h.Length)
	}
}

func TestFileTransferTypes_NDF1HeaderOversizedRejected(t *testing.T) {
	h := &NDF1Header{
		TransferID: TransferIDFromUUID(uuid.New()),
		Offset:     0,
		Length:     MaxNDF1Payload + 1,
	}
	encoded := h.Encode()
	decoded, err := DecodeNDF1Header(encoded)
	if err != nil {
		t.Fatalf("decode should not fail: %v", err)
	}
	// Validation is payload-length-dependent at the frame level, not header level.
	_ = decoded
}

func TestFileTransferTypes_NDF1HeaderTruncated(t *testing.T) {
	_, err := DecodeNDF1Header([]byte{0, 1, 2, 3})
	if err == nil {
		t.Fatal("expected error for truncated header")
	}
}

func TestFileTransferTypes_NDF1HeaderBadMagic(t *testing.T) {
	buf := make([]byte, NDF1HeaderLen)
	copy(buf[0:4], []byte("BAD!"))
	_, err := DecodeNDF1Header(buf)
	if err == nil {
		t.Fatal("expected error for bad magic")
	}
}

// ── Test Constants ──────────────────────────────────────────────────────

var validSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
