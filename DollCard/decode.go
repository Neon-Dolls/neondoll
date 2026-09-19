package dollcard

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Neon-Dolls/neondoll/DollState"
)

// requiredFiles lists files that must exist in a valid DollCard.
var requiredFiles = []string{
	"card.json",
	"identity.json",
	"soul.md",
	"owner/owner.md",
}

// Decode reads a .dollcard ZIP file and decodes it into a DollState.
func Decode(zipPath string) (*dollstate.DollState, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("dollcard: open %q: %w", zipPath, err)
	}
	defer r.Close()
	return decodeZip(&r.Reader)
}

// DecodeFromReader reads a .dollcard ZIP from an io.ReaderAt with known size.
func DecodeFromReader(r io.ReaderAt, size int64) (*dollstate.DollState, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("dollcard: open reader: %w", err)
	}
	return decodeZip(zr)
}

// decodeZip is the shared implementation for both public Decode functions.
func decodeZip(zr *zip.Reader) (*dollstate.DollState, error) {
	// Index files by name (normalised to forward slashes).
	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		name := strings.TrimLeft(strings.ReplaceAll(f.Name, "\\", "/"), "/")
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("dollcard: read %q: %w", name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("dollcard: read %q: %w", name, err)
		}
		files[name] = data
	}

	// Validate required files exist.
	for _, name := range requiredFiles {
		if _, ok := files[name]; !ok {
			return nil, fmt.Errorf("dollcard: missing required file %q", name)
		}
	}

	// 1. card.json — format version metadata.
	card, err := parseJSON[cardHeader](files["card.json"])
	if err != nil {
		return nil, fmt.Errorf("dollcard: card.json: %w", err)
	}
	if card.Version != currentCardVersion {
		return nil, fmt.Errorf("dollcard: unsupported card version %d (expected %d)", card.Version, currentCardVersion)
	}

	// 2. identity.json — Doll identity.
	type identityFields struct {
		DollID        string `json:"doll_id"`
		CanonicalName string `json:"canonical_name"`
	}
	ident, err := parseJSON[identityFields](files["identity.json"])
	if err != nil {
		return nil, fmt.Errorf("dollcard: identity.json: %w", err)
	}
	if ident.DollID == "" {
		return nil, fmt.Errorf("dollcard: identity.json: missing doll_id")
	}
	if ident.CanonicalName == "" {
		return nil, fmt.Errorf("dollcard: identity.json: missing canonical_name")
	}

	// 3. soul.md — plain markdown, trimmed, non-empty.
	soulContent := strings.TrimSpace(string(files["soul.md"]))
	if soulContent == "" {
		return nil, fmt.Errorf("dollcard: soul.md: empty")
	}

	// 4. owner/owner.md — plain markdown, trimmed, non-empty.
	ownerContent := strings.TrimSpace(string(files["owner/owner.md"]))
	if ownerContent == "" {
		return nil, fmt.Errorf("dollcard: owner/owner.md: empty")
	}

	// Build DollState.
	state := dollstate.NewDollState()
	state.Identity = dollstate.Identity{
		DollID:        ident.DollID,
		CanonicalName: ident.CanonicalName,
	}
	state.Soul = dollstate.Soul{
		Revision: 1,
		Content:  soulContent,
	}
	state.Owner = dollstate.Owner{
		Content: ownerContent,
	}

	return &state, nil
}

// parseJSON unmarshals data into T from raw bytes.
func parseJSON[T any](data []byte) (T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	return v, nil
}
