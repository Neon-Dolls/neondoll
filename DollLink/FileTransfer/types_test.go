package filetransfer

import (
	"testing"

	"github.com/google/uuid"
)

func TestFileTransferTypes_FileID(t *testing.T) {
	fid := FileID("test-file-v1")
	if string(fid) != "test-file-v1" {
		t.Fatalf("unexpected FileID: %q", fid)
	}
}

func TestFileTransferTypes_SameFileIDNewTransferID(t *testing.T) {
	fileID := FileID("immutable-byte-object-1")

	tid1 := TransferIDFromUUID(uuid.MustParse("a1b2c3d4-e5f6-7890-abcd-ef1234567890"))
	tid2 := TransferIDFromUUID(uuid.MustParse("00001111-2222-3333-4444-555566667777"))

	if tid1 == tid2 {
		t.Fatal("transfer IDs must be distinct")
	}

	o1 := Offer{FileID: fileID, TransferID: tid1, Size: 100, SHA256: validSHA256}
	o2 := Offer{FileID: fileID, TransferID: tid2, Size: 100, SHA256: validSHA256}

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

	o1 := Offer{FileID: fileID, TransferID: TransferIDFromUUID(uuid.New()), Size: 100, SHA256: validSHA256}
	o2 := Offer{FileID: fileID, TransferID: TransferIDFromUUID(uuid.New()), Size: 200, SHA256: validSHA256}

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
	s := EmptyTransferID.String()
	if _, err := uuid.Parse(s); err != nil {
		t.Fatalf("empty TransferID should still produce parseable UUID: %v", err)
	}
}

func TestFileTransferTypes_TransferIDMarshalText(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	text, err := tid.MarshalText()
	if err != nil {
		t.Fatalf("marshal text: %v", err)
	}
	if string(text) != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("got %q, want uuid string", string(text))
	}

	var parsed TransferID
	if err := parsed.UnmarshalText([]byte("550e8400-e29b-41d4-a716-446655440000")); err != nil {
		t.Fatalf("unmarshal text: %v", err)
	}
	if parsed != tid {
		t.Fatal("round trip mismatch")
	}
}

func TestFileTransferTypes_TransferIDJSON(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	data, err := tid.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	// Should be quoted UUID string
	if string(data) != `"550e8400-e29b-41d4-a716-446655440000"` {
		t.Fatalf("got %s, want quoted uuid", string(data))
	}

	var parsed TransferID
	if err := parsed.UnmarshalJSON(data); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if parsed != tid {
		t.Fatal("json round trip mismatch")
	}
}

func TestFileTransferTypes_InvalidOffer(t *testing.T) {
	validTID := TransferIDFromUUID(uuid.New())
	tests := []struct {
		name  string
		offer Offer
	}{
		{"empty file_id", Offer{FileID: "", TransferID: validTID, Size: 100, SHA256: validSHA256}},
		{"empty transfer_id", Offer{FileID: "f1", TransferID: EmptyTransferID, Size: 100, SHA256: validSHA256}},
		{"zero size", Offer{FileID: "f1", TransferID: validTID, Size: 0, SHA256: validSHA256}},
		{"negative size", Offer{FileID: "f1", TransferID: validTID, Size: -1, SHA256: validSHA256}},
		{"invalid sha256 length", Offer{FileID: "f1", TransferID: validTID, Size: 100, SHA256: "short"}},
		{"sha256 with non-hex", Offer{FileID: "f1", TransferID: validTID, Size: 100, SHA256: "zbcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"}},
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
		TransferID: TransferIDFromUUID(uuid.New()),
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
	validTID := TransferIDFromUUID(uuid.New())
	tests := []struct {
		name    string
		accept  Accept
		wantErr bool
	}{
		{"valid zero offset", Accept{FileID: "f1", TransferID: validTID, Offset: 0}, false},
		{"valid non-zero offset", Accept{FileID: "f1", TransferID: validTID, Offset: 1000}, false},
		{"valid offset at size", Accept{FileID: "f1", TransferID: validTID, Offset: 1024}, false},
		{"empty file_id", Accept{FileID: "", TransferID: validTID, Offset: 0}, true},
		{"empty transfer_id", Accept{FileID: "f1", TransferID: EmptyTransferID, Offset: 0}, true},
		{"negative offset", Accept{FileID: "f1", TransferID: validTID, Offset: -1}, true},
		{"offset exceeds size", Accept{FileID: "f1", TransferID: validTID, Offset: 9999}, true},
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
	validTID := TransferIDFromUUID(uuid.New())
	tests := []struct {
		name    string
		reject  Reject
		wantErr bool
	}{
		{"valid", Reject{FileID: "f1", TransferID: validTID, Reason: "declined"}, false},
		{"empty file_id", Reject{FileID: "", TransferID: validTID}, true},
		{"empty transfer_id", Reject{FileID: "f1", TransferID: EmptyTransferID}, true},
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
	validTID := TransferIDFromUUID(uuid.New())
	tests := []struct {
		name    string
		comp    Complete
		wantErr bool
	}{
		{"valid", Complete{FileID: "f1", TransferID: validTID}, false},
		{"empty file_id", Complete{FileID: "", TransferID: validTID}, true},
		{"empty transfer_id", Complete{FileID: "f1", TransferID: EmptyTransferID}, true},
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
	validTID := TransferIDFromUUID(uuid.New())
	tests := []struct {
		name     string
		received Received
		wantErr  bool
	}{
		{"valid", Received{FileID: "f1", TransferID: validTID}, false},
		{"empty file_id", Received{FileID: "", TransferID: validTID}, true},
		{"empty transfer_id", Received{FileID: "f1", TransferID: EmptyTransferID}, true},
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
	validTID := TransferIDFromUUID(uuid.New())
	tests := []struct {
		name    string
		cancel  Cancel
		wantErr bool
	}{
		{"valid", Cancel{FileID: "f1", TransferID: validTID, Reason: "cancelled"}, false},
		{"empty file_id", Cancel{FileID: "", TransferID: validTID}, true},
		{"empty transfer_id", Cancel{FileID: "f1", TransferID: EmptyTransferID}, true},
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
