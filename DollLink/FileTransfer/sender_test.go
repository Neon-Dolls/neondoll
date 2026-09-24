package filetransfer

import (
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestNewSenderState_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := make([]byte, 1000)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	s, err := NewSenderState("file1", tid, path, 1000, 256)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}
	defer s.Close()

	if s.FileID != "file1" {
		t.Errorf("FileID = %q, want %q", s.FileID, "file1")
	}
	if s.Size != 1000 {
		t.Errorf("Size = %d, want 1000", s.Size)
	}
	if s.Sent() != 0 {
		t.Errorf("Sent = %d, want 0", s.Sent())
	}
}

func TestNewSenderState_Invalid(t *testing.T) {
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	tests := []struct {
		name      string
		fid       FileID
		tid       TransferID
		path      string
		size      int64
		chunkSize int
	}{
		{"empty file id", "", tid, "", 100, 256},
		{"empty transfer id", "f", EmptyTransferID, "", 100, 256},
		{"zero size", "f", tid, "", 0, 256},
		{"negative size", "f", tid, "", -1, 256},
		{"chunk size 0", "f", tid, "", 100, 0},
		{"chunk size too big", "f", tid, "", 100, MaxNDF1Payload + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSenderState(tc.fid, tc.tid, tc.path, tc.size, tc.chunkSize)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestNewSenderState_FileSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := make([]byte, 100)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	// Claim size 200, but file is 100.
	_, err := NewSenderState("f", tid, path, 200, 256)
	if err == nil {
		t.Error("expected error for file size mismatch, got nil")
	}
}

func TestSenderState_NextFrame_ReadsAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := make([]byte, 1000)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	s, err := NewSenderState("f", tid, path, 1000, 256)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}
	defer s.Close()

	var allBytes []byte
	for {
		frame, err := s.NextFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		allBytes = append(allBytes, frame.Payload...)
	}

	if len(allBytes) != 1000 {
		t.Errorf("total bytes read = %d, want 1000", len(allBytes))
	}
	if string(allBytes) != string(data) {
		t.Error("data mismatch")
	}
	if s.Sent() != 1000 {
		t.Errorf("Sent = %d, want 1000", s.Sent())
	}

	// Verify SHA-256 matches.
	if s.SHA256Hex() != SHA256Hex(data) {
		t.Errorf("SHA256 mismatch: got %s, want %s", s.SHA256Hex(), SHA256Hex(data))
	}
}

func TestSenderState_ResumeSeek(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := make([]byte, 1000)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")

	// Send first 400 bytes from new sender.
	s1, err := NewSenderState("f", tid, path, 1000, 256)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}
	for i := 0; i < 2; i++ {
		frame, err := s1.NextFrame()
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		_ = frame // just consume
	}
	if s1.Sent() != 512 {
		t.Fatalf("expected 512 sent, got %d", s1.Sent())
	}
	s1.Close()

	// Create a new sender and seek to 512 (simulating resume).
	s2, err := NewSenderState("f", tid, path, 1000, 256)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}
	defer s2.Close()

	if err := s2.SeekTo(512); err != nil {
		t.Fatalf("SeekTo: %v", err)
	}
	if s2.Sent() != 512 {
		t.Errorf("Sent after seek = %d, want 512", s2.Sent())
	}

	// Read remaining bytes.
	var remainingBytes []byte
	for {
		frame, err := s2.NextFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		remainingBytes = append(remainingBytes, frame.Payload...)
	}

	if len(remainingBytes) != 488 {
		t.Errorf("remaining bytes = %d, want 488", len(remainingBytes))
	}
	if string(remainingBytes) != string(data[512:]) {
		t.Error("remaining data mismatch")
	}
	if s2.Sent() != 1000 {
		t.Errorf("Sent = %d, want 1000", s2.Sent())
	}
}

func TestSenderState_SeekInBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := make([]byte, 100)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	s, err := NewSenderState("f", tid, path, 100, 256)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}
	defer s.Close()

	// Seek to end (100) — no more frames.
	if err := s.SeekTo(100); err != nil {
		t.Fatalf("SeekTo(100): %v", err)
	}
	_, err = s.NextFrame()
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestSenderState_SeekOutOfBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := make([]byte, 100)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	s, err := NewSenderState("f", tid, path, 100, 256)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}
	defer s.Close()

	if err := s.SeekTo(101); err == nil {
		t.Error("expected error for out-of-bounds seek")
	}
	if err := s.SeekTo(-1); err == nil {
		t.Error("expected error for negative seek")
	}
}
