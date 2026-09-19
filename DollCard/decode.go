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

	// Index files by name (normalised to forward slashes).
	files := make(map[string][]byte, len(r.File))
	for _, f := range r.File {
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

	// Validate required files.
	for _, name := range requiredFiles {
		if _, ok := files[name]; !ok {
			return nil, fmt.Errorf("dollcard: missing required file %q", name)
		}
	}

	// 1. Parse card.json.
	card, err := parseJSON[cardHeader](files["card.json"])
	if err != nil {
		return nil, fmt.Errorf("dollcard: card.json: %w", err)
	}
	if card.Version != currentCardVersion {
		return nil, fmt.Errorf("dollcard: unsupported card version %d (expected %d)", card.Version, currentCardVersion)
	}

	// 2. Parse identity.json.
	type identityFields struct {
		DollID       string `json:"doll_id"`
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

	// 3. Parse soul.md (plain text, trimmed).
	soulContent := strings.TrimSpace(string(files["soul.md"]))
	if soulContent == "" {
		return nil, fmt.Errorf("dollcard: soul.md: empty")
	}

	// 4. Parse owner/owner.md.
	type ownerFields struct {
		OwnerID string `json:"owner_id"`
		Name    string `json:"name"`
		Content string `json:"content,omitempty"`
	}
	owner, err := parseJSON[ownerFields](files["owner/owner.md"])
	if err != nil {
		// owner/owner.md may be plain text rather than JSON — accept either.
		owner = ownerFields{
			Content: strings.TrimSpace(string(files["owner/owner.md"])),
		}
	}
	if owner.Content == "" {
		owner.Content = strings.TrimSpace(string(files["owner/owner.md"]))
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
		OwnerID: owner.OwnerID,
		Name:    owner.Name,
		Content: owner.Content,
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

// DecodeFromReader reads a .dollcard ZIP from an io.ReaderAt with known size.
// Useful for tests that embed the card in memory.
func DecodeFromReader(r io.ReaderAt, size int64) (*dollstate.DollState, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("dollcard: open reader: %w", err)
	}

	// Index files by name.
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

	// Validate required files.
	for _, name := range requiredFiles {
		if _, ok := files[name]; !ok {
			return nil, fmt.Errorf("dollcard: missing required file %q", name)
		}
	}

	// 1. Parse card.json.
	card, err := parseJSON[cardHeader](files["card.json"])
	if err != nil {
		return nil, fmt.Errorf("dollcard: card.json: %w", err)
	}
	if card.Version != currentCardVersion {
		return nil, fmt.Errorf("dollcard: unsupported card version %d (expected %d)", card.Version, currentCardVersion)
	}

	// 2. Parse identity.json.
	type identityFields struct {
		DollID       string `json:"doll_id"`
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

	// 3. Parse soul.md.
	soulContent := strings.TrimSpace(string(files["soul.md"]))
	if soulContent == "" {
		return nil, fmt.Errorf("dollcard: soul.md: empty")
	}

	// 4. Parse owner/owner.md.
	type ownerFields struct {
		OwnerID string `json:"owner_id"`
		Name    string `json:"name"`
		Content string `json:"content,omitempty"`
	}
	owner, err := parseJSON[ownerFields](files["owner/owner.md"])
	if err != nil {
		owner = ownerFields{
			Content: strings.TrimSpace(string(files["owner/owner.md"])),
		}
	}
	if owner.Content == "" {
		owner.Content = strings.TrimSpace(string(files["owner/owner.md"]))
	}

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
		OwnerID: owner.OwnerID,
		Name:    owner.Name,
		Content: owner.Content,
	}

	return &state, nil
}