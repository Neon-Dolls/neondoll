package modeltest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// expectedSHA256 is the SHA-256 of the committed spark-test-brain fixture.
// If you update model.gguf, also update README.md and this constant.
const expectedSHA256 = "9cda598c5ee0708eceae5385fd6cd51386daa1d9fd7f11564a37c811f1c62489"

const maxSize int64 = 40 << 20 // 40 MiB upper bound

func fixturePath(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file path")
	}
	return filepath.Join(filepath.Dir(testFile), "..", "..", "testdata", "models", "spark-test-brain", "model.gguf")
}

func TestSparkTestBrainExists(t *testing.T) {
	path := fixturePath(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("model.gguf not found at %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatal("model.gguf is empty")
	}
	if info.Size() > maxSize {
		t.Fatalf("model.gguf is %d bytes, expected < %d", info.Size(), maxSize)
	}
	t.Logf("model.gguf size: %d bytes", info.Size())
}

func TestSparkTestBrainGGUFMagic(t *testing.T) {
	path := fixturePath(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open model.gguf: %v", err)
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		t.Fatalf("failed to read magic bytes: %v", err)
	}
	if string(magic) != "GGUF" {
		t.Fatalf("bad GGUF magic: got %q, want \"GGUF\"", string(magic))
	}
}

func TestSparkTestBrainSHA256(t *testing.T) {
	path := fixturePath(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open model.gguf: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("failed to compute SHA-256: %v", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		t.Fatalf("SHA-256 mismatch:\n  got:  %s\n  want: %s\n(Update expectedSHA256 in test code and README.md)", got, expectedSHA256)
	}
}
