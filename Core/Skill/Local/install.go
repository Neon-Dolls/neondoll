package local

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

// Install atomically installs a standard Skill ZIP whose entries are rooted at
// SKILL.md. Existing installations are replaced only by an explicit update.
func (catalog *Catalog) Install(ctx context.Context, id skill.ID, operation string, artifact []byte) error {
	if catalog == nil || strings.TrimSpace(string(id)) == "" || (operation != "install" && operation != "update") {
		return skill.ErrInvalidSkill
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, err := zip.NewReader(bytes.NewReader(artifact), int64(len(artifact)))
	if err != nil {
		return skill.ErrInvalidSkill
	}
	if uint32(len(reader.File)) > catalog.configuration.MaxResources+1 {
		return skill.ErrTransferBounds
	}
	stagingParent, err := os.MkdirTemp(filepath.Dir(catalog.root), ".skill-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stagingParent)
	staging := filepath.Join(stagingParent, string(id))
	if err = os.Mkdir(staging, 0o700); err != nil {
		return err
	}
	var total int64
	seen := map[string]bool{}
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := file.Name
		if strings.HasSuffix(name, "/") {
			continue
		}
		if !skill.SafeRelativePath(name) || seen[name] || file.Mode()&os.ModeSymlink != 0 {
			return skill.ErrInvalidSkill
		}
		seen[name] = true
		limit := catalog.configuration.MaxResourceBytes
		if name == "SKILL.md" {
			limit = catalog.configuration.MaxInstructionBytes
		}
		if int64(file.UncompressedSize64) > limit || int64(file.UncompressedSize64) > catalog.configuration.MaxLoadBytes-total {
			return skill.ErrTransferBounds
		}
		stream, err := file.Open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, limit+1))
		closeErr := stream.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(len(data)) > limit {
			return skill.ErrTransferBounds
		}
		total += int64(len(data))
		target := filepath.Join(staging, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err = os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
	}
	if !seen["SKILL.md"] {
		return skill.ErrInvalidSkill
	}
	loaded, err := inspectDirectory(stagingParent, string(id), catalog.configuration)
	if err != nil {
		return err
	}
	if loaded.descriptor.ID != id {
		return skill.ErrInvalidSkill
	}

	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	destination := filepath.Join(catalog.root, string(id))
	_, statErr := os.Stat(destination)
	if statErr == nil && operation != "update" {
		return skill.ErrIntegrity
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	backup := filepath.Join(stagingParent, "previous")
	_ = os.RemoveAll(backup)
	if statErr == nil {
		if err = os.Rename(destination, backup); err != nil {
			return err
		}
	}
	if err = os.Rename(staging, destination); err != nil {
		if statErr == nil {
			_ = os.Rename(backup, destination)
		}
		return err
	}
	rollback := func() {
		_ = os.RemoveAll(destination)
		if statErr == nil {
			_ = os.Rename(backup, destination)
		}
	}
	configuration := catalog.configuration
	configuration.Directory = catalog.root
	refreshed, err := Open(configuration, Options{})
	if err != nil {
		rollback()
		return err
	}
	catalog.entries, catalog.ordered = refreshed.entries, refreshed.ordered
	_ = os.RemoveAll(backup)
	return nil
}
