package filetransfer

import (
	"fmt"
	"io"
	"os"
)

// ── Sender ──────────────────────────────────────────────────────────────

// SenderState tracks an outbound file transfer. It reads the file in
// chunks, computes SHA-256 on the fly, and creates NDF1 frames at the
// correct sequential offsets.
type SenderState struct {
	FileID     FileID
	TransferID TransferID
	Size       int64
	ChunkSize  int

	f      *os.File
	sent   int64
	hasher *sha256Hasher
}

// NewSenderState opens the file at path and prepares to send it. The file
// must hold exactly size bytes. chunkSize controls the NDF1 payload size
// (max 1 MiB). A reader is NOT kept open — the caller supplies a reader
// per chunk via SendNext.
func NewSenderState(fid FileID, tid TransferID, path string, size int64, chunkSize int) (*SenderState, error) {
	if fid == "" {
		return nil, fmt.Errorf("filetransfer: sender: empty file id")
	}
	if tid == EmptyTransferID {
		return nil, fmt.Errorf("filetransfer: sender: empty transfer id")
	}
	if size <= 0 {
		return nil, fmt.Errorf("filetransfer: sender: size must be positive, got %d", size)
	}
	if chunkSize <= 0 || chunkSize > MaxNDF1Payload {
		return nil, fmt.Errorf("filetransfer: sender: chunk size %d out of range (1..%d)", chunkSize, MaxNDF1Payload)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("filetransfer: sender: open %q: %w", path, err)
	}

	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("filetransfer: sender: stat %q: %w", path, err)
	}
	if fi.Size() != size {
		f.Close()
		return nil, fmt.Errorf("filetransfer: sender: file size %d does not match expected %d", fi.Size(), size)
	}

	return &SenderState{
		FileID:     fid,
		TransferID: tid,
		Size:       size,
		ChunkSize:  chunkSize,
		f:          f,
		hasher:     newSHA256Hasher(),
	}, nil
}

// SeekTo moves the read cursor to offset. Used for resume.
func (s *SenderState) SeekTo(offset int64) error {
	if offset < 0 {
		return fmt.Errorf("filetransfer: sender: negative seek offset %d", offset)
	}
	if offset > s.Size {
		return fmt.Errorf("filetransfer: sender: seek offset %d exceeds file size %d", offset, s.Size)
	}
	// Reset hasher and hash what we already sent.
	// For resume, the receiver already has the prefix, so we only hash what comes next.
	// The sender does NOT need to re-hash previously sent bytes for the final SHA;
	// the final SHA is known from the Offer. The sender just sends bytes.
	s.sent = offset
	_, err := s.f.Seek(offset, io.SeekStart)
	return err
}

// Sent returns how many bytes have been sent so far.
func (s *SenderState) Sent() int64 { return s.sent }

// NextFrame reads the next chunk and returns an NDF1Frame. Returns
// io.EOF when all bytes have been read.
func (s *SenderState) NextFrame() (*NDF1Frame, error) {
	if s.sent >= s.Size {
		return nil, io.EOF
	}

	remaining := s.Size - s.sent
	readSize := s.ChunkSize
	if int64(readSize) > remaining {
		readSize = int(remaining)
	}

	buf := make([]byte, readSize)
	n, err := io.ReadFull(s.f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("filetransfer: sender: read: %w", err)
	}
	buf = buf[:n]

	s.hasher.Write(buf)
	s.sent += int64(n)

	frame := NewNDF1Frame(s.TransferID, uint64(s.sent-int64(n)), buf)
	return frame, nil
}

// Close closes the underlying file.
func (s *SenderState) Close() error {
	if s.f != nil {
		return s.f.Close()
	}
	return nil
}

// SHA256Hex returns the SHA-256 hex of all bytes read so far (for
// verification before the file is fully sent).
func (s *SenderState) SHA256Hex() string {
	return fmt.Sprintf("%x", s.hasher.Sum())
}
