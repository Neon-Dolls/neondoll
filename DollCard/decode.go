package dollcard

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
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

// indexZIP extracts and normalises all file entries from a ZIP reader.
func indexZIP(zr *zip.Reader) (map[string][]byte, error) {
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
	return files, nil
}

// decodeZip is the shared implementation for both public Decode functions.
func decodeZip(zr *zip.Reader) (*dollstate.DollState, error) {
	files, err := indexZIP(zr)
	if err != nil {
		return nil, err
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
		TemplateRef   string `json:"template_ref,omitempty"`
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

	// Build DollState with required fields.
	state := dollstate.NewDollState()
	state.Identity = dollstate.Identity{
		DollID:        ident.DollID,
		CanonicalName: ident.CanonicalName,
		TemplateRef:   ident.TemplateRef,
	}
	state.Soul = dollstate.Soul{
		Revision: 1,
		Content:  soulContent,
	}
	state.Owner = dollstate.Owner{
		Content: ownerContent,
	}

	// 5. self/self.json — optional structured Self.
	if data, ok := files["self/self.json"]; ok {
		self, err := parseJSON[dollstate.Self](data)
		if err != nil {
			return nil, fmt.Errorf("dollcard: self/self.json: %w", err)
		}
		state.Self = self
	}

	// 6. memories/memories-*.jsonl — optional Memory items as JSONL.
	memories, err := decodeMemories(files)
	if err != nil {
		return nil, err
	}
	if memories != nil {
		state.Memories = *memories
	}

	// 7. drives/drives.json — optional Drives.
	if data, ok := files["drives/drives.json"]; ok {
		drives, err := parseJSON[dollstate.Drives](data)
		if err != nil {
			return nil, fmt.Errorf("dollcard: drives/drives.json: %w", err)
		}
		state.Drives = drives
	}

	// 8. goals/goals.json — optional Goals.
	if data, ok := files["goals/goals.json"]; ok {
		goals, err := parseJSON[dollstate.Goals](data)
		if err != nil {
			return nil, fmt.Errorf("dollcard: goals/goals.json: %w", err)
		}
		state.Goals = goals
	}

	// 9. intentions/intentions.json — optional Intentions.
	if data, ok := files["intentions/intentions.json"]; ok {
		intentions, err := parseJSON[dollstate.Intentions](data)
		if err != nil {
			return nil, fmt.Errorf("dollcard: intentions/intentions.json: %w", err)
		}
		state.Intentions = intentions
	}

	return &state, nil
}

// decodeMemories reads all memories/memories-*.jsonl segment files in
// lexicographic filename order (per Doll Card v1 contract) and returns the
// combined Memories, or nil if no memory segments exist.
func decodeMemories(files map[string][]byte) (*dollstate.Memories, error) {
	// Collect and sort segment names — map iteration is nondeterministic.
	var segNames []string
	for name := range files {
		if strings.HasPrefix(name, "memories/") && strings.HasSuffix(name, ".jsonl") {
			segNames = append(segNames, name)
		}
	}
	if segNames == nil {
		return nil, nil
	}
	sort.Strings(segNames)

	var items []dollstate.MemoryItem
	for _, name := range segNames {
		data := files[name]
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var item dollstate.MemoryItem
			if err := json.Unmarshal([]byte(line), &item); err != nil {
				return nil, fmt.Errorf("dollcard: %q: %w", name, err)
			}
			items = append(items, item)
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("dollcard: %q: scanner: %w", name, err)
		}
	}
	if items == nil {
		return nil, nil
	}
	return &dollstate.Memories{Items: items}, nil
}

// parseJSON unmarshals data into T from raw bytes.
func parseJSON[T any](data []byte) (T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	return v, nil
}