package filetransfer

import (
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestResumeMatch_Valid(t *testing.T) {
	data := make([]byte, 1000)
	rand.Read(data)
	shaHex := SHA256Hex(data)

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("file1", tid, 1000, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	// Write 400 bytes.
	frame := &NDF1Frame{
		Header: NDF1Header{
			TransferID: tid,
			Offset:     0,
			Length:     400,
		},
		Payload: data[:400],
	}
	if err := rs.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	offer := &Offer{
		FileID:     "file1",
		TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440001"), // new transfer_id
		Size:       1000,
		SHA256:     shaHex,
	}

	offset, err := ResumeMatch(rs, offer)
	if err != nil {
		t.Fatalf("ResumeMatch: %v", err)
	}
	if offset != 400 {
		t.Errorf("offset = %d, want 400", offset)
	}
}

func TestResumeMatch_EmptyRetained(t *testing.T) {
	data := make([]byte, 1000)
	rand.Read(data)
	shaHex := SHA256Hex(data)

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("file1", tid, 1000, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	offer := &Offer{
		FileID:     "file1",
		TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440001"),
		Size:       1000,
		SHA256:     shaHex,
	}

	offset, err := ResumeMatch(rs, offer)
	if err != nil {
		t.Fatalf("ResumeMatch: %v", err)
	}
	if offset != 0 {
		t.Errorf("offset = %d, want 0", offset)
	}
}

func TestResumeMatch_FileIDMismatch(t *testing.T) {
	shaHex := SHA256Hex(make([]byte, 100))
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("file1", tid, 100, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	offer := &Offer{
		FileID:     "file2", // different
		TransferID: tid,
		Size:       100,
		SHA256:     shaHex,
	}

	_, err = ResumeMatch(rs, offer)
	if err == nil {
		t.Error("expected error for file_id mismatch")
	}
}

func TestResumeMatch_SizeMismatch(t *testing.T) {
	shaHex := SHA256Hex(make([]byte, 100))
	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("file1", tid, 100, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	offer := &Offer{
		FileID:     "file1",
		TransferID: tid,
		Size:       200, // different
		SHA256:     shaHex,
	}

	_, err = ResumeMatch(rs, offer)
	if err == nil {
		t.Error("expected error for size mismatch")
	}
}

func TestResumeMatch_SHA256Mismatch(t *testing.T) {
	data := make([]byte, 100)
	rand.Read(data)
	shaHex1 := SHA256Hex(data)

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("file1", tid, 100, shaHex1)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	shaHex2 := SHA256Hex(make([]byte, 100)) // different
	offer := &Offer{
		FileID:     "file1",
		TransferID: tid,
		Size:       100,
		SHA256:     shaHex2, // different
	}

	_, err = ResumeMatch(rs, offer)
	if err == nil {
		t.Error("expected error for SHA-256 mismatch")
	}
}

func TestResumeMatch_AlreadyCompleted(t *testing.T) {
	data := make([]byte, 100)
	rand.Read(data)
	shaHex := SHA256Hex(data)

	tid := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	rs, err := NewReceiveState("file1", tid, 100, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}
	defer rs.Cleanup()

	frame := &NDF1Frame{
		Header:  NDF1Header{TransferID: tid, Offset: 0, Length: 100},
		Payload: data,
	}
	if err := rs.WriteFrame(frame); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	if err := rs.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	offer := &Offer{
		FileID:     "file1",
		TransferID: MustParseTransferID("550e8400-e29b-41d4-a716-446655440001"),
		Size:       100,
		SHA256:     shaHex,
	}

	_, err = ResumeMatch(rs, offer)
	if err == nil {
		t.Error("expected error for already completed")
	}
}

func TestResumeMatch_NilRetained(t *testing.T) {
	offer := &Offer{
		FileID: "f",
	}
	_, err := ResumeMatch(nil, offer)
	if err == nil {
		t.Error("expected error for nil retained")
	}
}

func TestResumeMatch_NilOffer(t *testing.T) {
	_, err := ResumeMatch(&ReceiveState{}, nil)
	if err == nil {
		t.Error("expected error for nil offer")
	}
}

// ── Full resume flow integration test ─────────────────────────────────

func TestFullResumeFlow(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.bin")

	data := make([]byte, 5000)
	rand.Read(data)
	if err := os.WriteFile(srcPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	shaHex := SHA256Hex(data)

	tid1 := MustParseTransferID("550e8400-e29b-41d4-a716-446655440000")
	tid2 := MustParseTransferID("550e8400-e29b-41d4-a716-446655440001")

	// ── Phase 1: Sender sends the first 2000 bytes ──
	s1, err := NewSenderState("file1", tid1, srcPath, 5000, 1024)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}

	rs1, err := NewReceiveState("file1", tid1, 5000, shaHex)
	if err != nil {
		t.Fatalf("NewReceiveState: %v", err)
	}

	// Send first 2 frames (2048 bytes).
	for i := 0; i < 2; i++ {
		frame, err := s1.NextFrame()
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		if err := rs1.WriteFrame(frame); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}

	sent := s1.Sent()
	retained := rs1.Retained
	if sent != 2048 || retained != 2048 {
		t.Fatalf("sent=%d retained=%d, want both=2048", sent, retained)
	}

	s1.Close()
	// Do NOT close rs1 — simulate transport loss, receiver keeps the partial.

	// ── Phase 2: Simulate reconnect with new transfer_id ──
	s2, err := NewSenderState("file1", tid2, srcPath, 5000, 1024)
	if err != nil {
		t.Fatalf("NewSenderState: %v", err)
	}
	defer s2.Close()

	// Re-open retained state.
	rs2, err := OpenRetained("file1", tid2, 5000, shaHex)
	if err != nil {
		t.Fatalf("OpenRetained: %v", err)
	}
	defer rs2.Cleanup()

	if rs2.Retained != 2048 {
		t.Fatalf("expected retained=2048, got %d", rs2.Retained)
	}

	// Resume match check.
	offer := &Offer{
		FileID:     "file1",
		TransferID: tid2,
		Size:       5000,
		SHA256:     shaHex,
	}
	offset, err := ResumeMatch(rs2, offer)
	if err != nil {
		t.Fatalf("ResumeMatch: %v", err)
	}
	if offset != 2048 {
		t.Fatalf("ResumeMatch offset = %d, want 2048", offset)
	}

	// Sender seeks to resume offset.
	if err := s2.SeekTo(offset); err != nil {
		t.Fatalf("SeekTo: %v", err)
	}

	// Send remaining bytes.
	totalFrames := 0
	for {
		frame, err := s2.NextFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		totalFrames++
		if err := rs2.WriteFrame(frame); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}

	if rs2.Retained != 5000 {
		t.Fatalf("retained=%d after resume, want 5000", rs2.Retained)
	}

	// Complete.
	if err := rs2.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !rs2.IsCompleted() {
		t.Fatal("not completed after Complete()")
	}

	// Verify file content.
	got, err := os.ReadFile(rs2.FilePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatal("file content mismatch after resume")
	}

	_ = totalFrames // unused but informative
}
