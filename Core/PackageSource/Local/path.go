package local

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	packagesource "github.com/Neon-Dolls/neondoll/Core/PackageSource"
)

func confinedRoot(dataRoot, configured string) (string, error) {
	if filepath.IsAbs(configured) {
		return "", packagesource.ErrInvalid
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.Clean(configured))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", packagesource.ErrInvalid
	}
	return target, nil
}

func safePath(value string) bool {
	return value != "" && !filepath.IsAbs(value) && !strings.Contains(value, "\\") && filepath.Clean(value) == value && value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator))
}

func readRootFile(root, relative string, max int64) ([]byte, error) {
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	file, err := handle.Open(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, packagesource.ErrNotFound
		}
		return nil, packagesource.ErrInvalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, packagesource.ErrInvalid
	}
	if info.Size() > max {
		return nil, packagesource.ErrBounds
	}
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, fmt.Errorf("package source local: read: %w", err)
	}
	if int64(len(data)) > max {
		return nil, packagesource.ErrBounds
	}
	return data, nil
}
