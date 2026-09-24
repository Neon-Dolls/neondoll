package filetransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

// ── Receiver ────────────────────────────────────────────────────────────

// DefaultStorageDir is the default directory for storing partial and
// completed file transfers.
var DefaultStorageDir = os.TempDir()

// ReceiveState tracks the bounded-memory state of one file reception.
type ReceiveState struct {
	FileID     FileID
	TransferID TransferID
	Size       int64
	SHA256     string // lowercase hex SHA-256 of expected complete file
	Retained   int64  // bytes retained so far
	completed  bool
	filePath   string // path of the completed file (after renaming)
	tmpPath    string // path of the partial file being written
	hasher     io.Writer
	hasherObj  *sha256Hasher
}

// sha256Hasher wraps hash.Hash so we can get Sum after streaming.
type sha256Hasher struct{ h hash.Hash }

func newSHA256Hasher() *sha256Hasher {
	h := sha256.New()
	return &sha256Hasher{h: h}
}

func (s *sha256Hasher) Write(p []byte) (int, error) { return s.h.Write(p) }
func (s *sha256Hasher) Sum() []byte                 { return s.h.Sum(nil) }

// NewReceiveState creates a new receive state for an offered file.
// It prepares a temporary file path for streaming writes.
func NewReceiveState(fileID FileID, tid TransferID, size int64, sha256Hex string) (*ReceiveState, error) {
	if fileID == "" {
		return nil, errors.New("filetransfer: file_id is required")
	}
	if tid.IsZero() {
		return nil, errors.New("filetransfer: zero transfer id")
	}
	if size <= 0 {
		return nil, fmt.Errorf("filetransfer: invalid size %d", size)
	}
	if err := validateSHA256(sha256Hex); err != nil {
		return nil, fmt.Errorf("filetransfer: invalid sha256: %w", err)
	}

	storageDir := filepath.Join(DefaultStorageDir, "neondoll-transfer")
	if err := os.MkdirAll(storageDir, 0700); err != nil {
		return nil, fmt.Errorf("filetransfer: create storage dir: %w", err)
	}

	safeName := sanitizeName(string(fileID))

	return &ReceiveState{
		FileID:     fileID,
		TransferID: tid,
		Size:       size,
		SHA256:     sha256Hex,
		tmpPath:    filepath.Join(storageDir, safeName+".partial"),
		filePath:   filepath.Join(storageDir, safeName),
		hasherObj:  newSHA256Hasher(),
	}, nil
}

// WriteFrame writes the payload of one NDF1 frame to the partial file at the
// correct sequential offset. It streams through the hasher without copying
// to an intermediate buffer beyond the frame payload itself.
func (rs *ReceiveState) WriteFrame(frame *NDF1Frame) error {
	if rs.completed {
		return errors.New("filetransfer: receive already completed")
	}
	return rs.writeAt(frame.Header.Offset, frame.Payload)
}

// WriteFrameFromReader writes frame payload by reading from the given reader,
// avoiding allocating a []byte for the full payload. The frame's Length field
// controls how many bytes are read.
func (rs *ReceiveState) WriteFrameFromReader(frame *NDF1Frame, r io.Reader) error {
	if rs.completed {
		return errors.New("filetransfer: receive already completed")
	}
	if frame.Header.Offset != uint64(rs.Retained) {
		return fmt.Errorf("filetransfer: expected offset %d, got %d",
			rs.Retained, frame.Header.Offset)
	}
	expectedEnd := rs.Retained + int64(frame.Header.Length)
	if expectedEnd > rs.Size {
		return fmt.Errorf("filetransfer: frame would exceed file size %d (offset %d, len %d)",
			rs.Size, frame.Header.Offset, frame.Header.Length)
	}

	f, err := os.OpenFile(rs.tmpPath, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("filetransfer: open temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(int64(frame.Header.Offset), io.SeekStart); err != nil {
		return fmt.Errorf("filetransfer: seek: %w", err)
	}

	n, err := io.CopyN(io.MultiWriter(f, rs.hasherObj), r, int64(frame.Header.Length))
	if err != nil {
		return fmt.Errorf("filetransfer: copy frame: %w", err)
	}
	rs.Retained += n
	return nil
}

// writeAt is the common path for write operations.
func (rs *ReceiveState) writeAt(offset uint64, payload []byte) error {
	if offset != uint64(rs.Retained) {
		return fmt.Errorf("filetransfer: expected offset %d, got %d",
			rs.Retained, offset)
	}
	expectedEnd := rs.Retained + int64(len(payload))
	if expectedEnd > rs.Size {
		return fmt.Errorf("filetransfer: frame would exceed file size %d (offset %d, len %d)",
			rs.Size, offset, len(payload))
	}

	f, err := os.OpenFile(rs.tmpPath, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("filetransfer: open temp file: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return fmt.Errorf("filetransfer: seek: %w", err)
	}

	n, err := f.Write(payload)
	if err != nil {
		return fmt.Errorf("filetransfer: write: %w", err)
	}
	if _, err := rs.hasherObj.Write(payload); err != nil {
		return fmt.Errorf("filetransfer: hash write: %w", err)
	}
	rs.Retained += int64(n)
	return nil
}

// Complete finalizes the receive by verifying size and SHA-256, then
// promoting the partial file to completed. After success, the completed
// file is at FilePath().
func (rs *ReceiveState) Complete() error {
	if rs.completed {
		return errors.New("filetransfer: receive already completed")
	}
	if rs.Retained != rs.Size {
		return fmt.Errorf("filetransfer: retained %d bytes but expected %d",
			rs.Retained, rs.Size)
	}

	gotHex := hex.EncodeToString(rs.hasherObj.Sum())
	if gotHex != rs.SHA256 {
		return fmt.Errorf("filetransfer: sha256 mismatch: got %s, want %s",
			gotHex, rs.SHA256)
	}

	if err := os.Rename(rs.tmpPath, rs.filePath); err != nil {
		return fmt.Errorf("filetransfer: promote file: %w", err)
	}
	rs.completed = true
	return nil
}

// IsCompleted returns true after Complete succeeds.
func (rs *ReceiveState) IsCompleted() bool { return rs.completed }

// FilePath returns the path of the completed file. Only valid after Complete.
func (rs *ReceiveState) FilePath() string { return rs.filePath }

// TempPath returns the path of the partial file in progress.
func (rs *ReceiveState) TempPath() string { return rs.tmpPath }

// RetainedOffset returns the number of contiguous bytes received so far.
func (rs *ReceiveState) RetainedOffset() int64 { return rs.Retained }

// Cleanup removes partial and completed files.
func (rs *ReceiveState) Cleanup() {
	os.Remove(rs.tmpPath)
	os.Remove(rs.filePath)
}

// ── Resume ──────────────────────────────────────────────────────────────

// OpenRetained opens or creates a receive state, detecting an existing
// partial file for resume. The file_id, size, and SHA-256 must all match
// the retained data for resume to work. The hasher is re-seeded from the
// retained bytes so the final hash covers everything.
func OpenRetained(fileID FileID, tid TransferID, size int64, sha256Hex string) (*ReceiveState, error) {
	rs, err := NewReceiveState(fileID, tid, size, sha256Hex)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(rs.tmpPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rs, nil // no retained data
		}
		return nil, fmt.Errorf("filetransfer: stat partial: %w", err)
	}

	rs.Retained = info.Size()
	if rs.Retained > size {
		// Retained more than expected — corrupt; start fresh.
		os.Remove(rs.tmpPath)
		rs.Retained = 0
		rs.hasherObj = newSHA256Hasher()
		return rs, nil
	}

	if rs.Retained > 0 {
		f, err := os.Open(rs.tmpPath)
		if err != nil {
			return nil, fmt.Errorf("filetransfer: open retained: %w", err)
		}
		defer f.Close()

		rs.hasherObj = newSHA256Hasher()
		if _, err := io.CopyN(rs.hasherObj, f, rs.Retained); err != nil {
			return nil, fmt.Errorf("filetransfer: read retained for hash: %w", err)
		}
	}

	return rs, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

func sanitizeName(name string) string {
	result := make([]byte, 0, len(name))
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' {
			result = append(result, c)
		} else {
			result = append(result, '_')
		}
	}
	return string(result)
}
