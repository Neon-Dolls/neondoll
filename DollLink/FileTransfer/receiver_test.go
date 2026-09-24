package filetransfer

import (
	"crypto/rand"
	"io"
	"os"
	"testing"
)

func TestNewReceiveState_Valid(t *testing.T) {
	rs, err := NewReceiveState("test-file", MustParseTransferID("550e8400-e29b-41d4-a716-446655440000"), 100, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != nil {
		t.Fatalf("NewReceiveState failed: %v", err)
	}
	defer rs.Cleanup()

	if rs.FileID != "test-file" {
		t.Errorf("FileID = %q, want %q", rs.FileID, "test-file")
	}
	if rs.Size != 100 {
		t.Errorf("Size = %d, want 100", rs.Size)
	}
	if rs.Retained != 0 {
		t.Errorf("Retained = %d, want 0", rs.Retained)
	}
	if rs.IsCompleted() {
		t.Error("IsCompleted = true, want false")
	}
}

func TestNewReceiveState_Invalid(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	tests := []struct {
		name string
		fid  FileID
		tid  TransferID
		size int64
		sha  string
	}{
		{"empty file id", "", tid, 100, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"zero transfer id", "test", EmptyTransferID, 100, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"zero size", "test", tid, 0, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"negative size", "test", tid, -1, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"bad sha", "test", tid, 100, "not-a-sha256"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewReceiveState(tc.fid, tc.tid, tc.size, tc.sha)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestReceiveState_StreamingWrite(t *testing.T) {
	data := make([]byte, 1000)
	rand.Read(data)
	shaHex := SHA256Hex(data)

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("stream-test", tid, 1000, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	// Write in 256-byte chunks.
	for offset := 0; offset < len(data); offset += 256 {
		end := offset + 256
		if end > len(data) {
			end = len(data)
		}
		frame := &NDF1Frame{
			Header: NDF1Header{
				TransferID: tid,
				Offset:     uint64(offset),
				Length:     uint32(end - offset),
			},
			Payload: data[offset:end],
		}
		if err := rs.WriteFrame(frame); err != nil {
			t.Fatalf("WriteFrame at offset %d: %v", offset, err)
		}
	}

	if rs.Retained != 1000 {
		t.Fatalf("Retained = %d, want 1000", rs.Retained)
	}

	// Complete.
	if err := rs.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !rs.IsCompleted() {
		t.Error("IsCompleted = false after Complete")
	}

	// Verify file content.
	got, err := os.ReadFile(rs.FilePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Error("file content mismatch")
	}

	// Verify duplicate Complete fails.
	if err := rs.Complete(); err == nil {
		t.Error("expected error on duplicate Complete")
	}
}

func TestReceiveState_WrongOffset(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("offset-test", tid, 100, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	// Write at wrong offset.
	frame := &NDF1Frame{
		Header: NDF1Header{
			TransferID: tid,
			Offset:     50, // should be 0
			Length:     10,
		},
		Payload: make([]byte, 10),
	}
	if err := rs.WriteFrame(frame); err == nil {
		t.Error("expected error for wrong offset")
	}
}

func TestReceiveState_CompleteSizeMismatch(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("size-test", tid, 100, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	// Write only 50 bytes.
	frame := &NDF1Frame{
		Header: NDF1Header{
			TransferID: tid,
			Offset:     0,
			Length:     50,
		},
		Payload: make([]byte, 50),
	}
	if err := rs.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	if err := rs.Complete(); err == nil {
		t.Error("expected error for size mismatch")
	}
}

func TestReceiveState_CompleteChecksumMismatch(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	// Wrong SHA.
	rs, err := NewReceiveState("csum-test", tid, 10, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	frame := &NDF1Frame{
		Header: NDF1Header{
			TransferID: tid,
			Offset:     0,
			Length:     10,
		},
		Payload: make([]byte, 10),
	}
	if err := rs.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	if err := rs.Complete(); err == nil {
		t.Error("expected error for checksum mismatch")
	}
}

func TestOpenRetained_NoExistingFile(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := OpenRetained("new-file", tid, 100, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != nil {
		t.Fatalf("OpenRetained: %v", err)
	}
	defer rs.Cleanup()

	if rs.Retained != 0 {
		t.Errorf("Retained = %d, want 0", rs.Retained)
	}
}

func TestOpenRetained_WithExistingPartial(t *testing.T) {
	data := make([]byte, 500)
	rand.Read(data)
	shaHex := SHA256Hex(data)

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	// Create initial state and write partial data.
	rs1, err := NewReceiveState("resume-test", tid, 500, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs1.Cleanup()

	frame := &NDF1Frame{
		Header: NDF1Header{
			TransferID: tid,
			Offset:     0,
			Length:     200,
		},
		Payload: data[:200],
	}
	if err := rs1.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	if rs1.Retained != 200 {
		t.Fatalf("Retained = %d, want 200", rs1.Retained)
	}

	// Now simulate reconnect with OpenRetained.
	rs2, err := OpenRetained("resume-test", tid, 500, shaHex)
	if err != nil {
		t.Fatalf("OpenRetained: %v", err)
	}
	defer rs2.Cleanup()

	if rs2.Retained != 200 {
		t.Fatalf("Retained after reopen = %d, want 200", rs2.Retained)
	}

	// Write the remaining 300 bytes.
	for offset := 200; offset < 500; offset += 100 {
		end := offset + 100
		if end > 500 {
			end = 500
		}
		frame2 := &NDF1Frame{
			Header: NDF1Header{
				TransferID: tid,
				Offset:     uint64(offset),
				Length:     uint32(end - offset),
			},
			Payload: data[offset:end],
		}
		if err := rs2.WriteFrame(frame2); err != nil {
			t.Fatalf("WriteFrame at offset %d: %v", offset, err)
		}
	}

	if err := rs2.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Verify entire file content.
	got, err := os.ReadFile(rs2.FilePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Error("file content mismatch after resume")
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple.txt", "simple.txt"},
		{"path/../file", "path_.._file"},
		{"a:b:c", "a_b_c"},
	}
	for _, tc := range tests {
		got := sanitizeName(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestReceiveStateWriteFrameFromReader(t *testing.T) {
	data := make([]byte, 100)
	rand.Read(data)
	shaHex := SHA256Hex(data)

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("reader-test", tid, 100, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	// Write using WriteFrameFromReader with a bytes reader.
	frame := &NDF1Frame{
		Header: NDF1Header{
			TransferID: tid,
			Offset:     0,
			Length:     100,
		},
	}
	r := &byteReader{data: data}
	if err := rs.WriteFrameFromReader(frame, r); err != nil {
		t.Fatalf("WriteFrameFromReader: %v", err)
	}

	if rs.Retained != 100 {
		t.Fatalf("Retained = %d, want 100", rs.Retained)
	}

	if err := rs.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got, err := os.ReadFile(rs.FilePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Error("file content mismatch")
	}
}

// byteReader is a simple io.Reader over a byte slice.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
