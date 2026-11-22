package local

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

func maliciousZIP(t *testing.T, name string, mode os.FileMode) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	header := &zip.FileHeader{Name: name}
	header.SetMode(mode)
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("payload"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestInstallRejectsArchiveTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, "skills")
	if err := os.Mkdir(skills, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := skill.DefaultConfiguration()
	configuration.Directory = "skills"
	catalog, err := Open(configuration, Options{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	for name, artifact := range map[string][]byte{"traversal": maliciousZIP(t, "../outside", 0o600), "symlink": maliciousZIP(t, "SKILL.md", os.ModeSymlink|0o777)} {
		t.Run(name, func(t *testing.T) {
			if err := catalog.Install(context.Background(), "unsafe", "install", artifact); !errors.Is(err, skill.ErrInvalidSkill) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside path created: %v", err)
	}
}
