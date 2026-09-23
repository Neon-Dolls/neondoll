package dollcard

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Neon-Dolls/neondoll/DollState"
)

// Encode writes a DollState into a .dollcard ZIP container to dst.
//
// The Card contains only canonical Doll State — no Core-local persistence,
// runtime fields, database IDs, or scheduler machinery.
func Encode(state *dollstate.DollState, dst io.Writer) error {
	zw := zip.NewWriter(dst)

	// ── card.json — format version ──
	if err := writeJSON(zw, "card.json", cardHeader{Version: currentCardVersion}); err != nil {
		return fmt.Errorf("card.json: %w", err)
	}

	// ── identity.json — structured identity ──
	type identityFields struct {
		DollID        string `json:"doll_id"`
		CanonicalName string `json:"canonical_name"`
		TemplateRef   string `json:"template_ref,omitempty"`
	}
	if err := writeJSON(zw, "identity.json", identityFields{
		DollID:        state.Identity.DollID,
		CanonicalName: state.Identity.CanonicalName,
		TemplateRef:   state.Identity.TemplateRef,
	}); err != nil {
		return fmt.Errorf("identity.json: %w", err)
	}

	// ── soul.md — prose Soul ──
	if err := writeText(zw, "soul.md", state.Soul.Content); err != nil {
		return fmt.Errorf("soul.md: %w", err)
	}

	// ── self/self.json — structured Self (optional) ──
	if state.Self.DisplayName != "" || state.Self.Pronouns != "" || state.Self.Tagline != "" {
		if err := writeJSON(zw, "self/self.json", state.Self); err != nil {
			return fmt.Errorf("self/self.json: %w", err)
		}
	}

	// ── owner/owner.md — prose Owner ──
	if err := writeText(zw, "owner/owner.md", state.Owner.Content); err != nil {
		return fmt.Errorf("owner/owner.md: %w", err)
	}

	// ── memories/memories-NNNNNN.jsonl — Memory items as JSONL ──
	if err := writeMemories(zw, state.Memories.Items); err != nil {
		return err
	}

	// ── drives/drives.json (optional) ──
	if len(state.Drives.Items) > 0 {
		if err := writeJSON(zw, "drives/drives.json", state.Drives); err != nil {
			return fmt.Errorf("drives/drives.json: %w", err)
		}
	}

	// ── goals/goals.json (optional) ──
	if len(state.Goals.Items) > 0 {
		if err := writeJSON(zw, "goals/goals.json", state.Goals); err != nil {
			return fmt.Errorf("goals/goals.json: %w", err)
		}
	}

	// ── intentions/intentions.json (optional) ──
	if len(state.Intentions.Items) > 0 {
		if err := writeJSON(zw, "intentions/intentions.json", state.Intentions); err != nil {
			return fmt.Errorf("intentions/intentions.json: %w", err)
		}
	}

	// Close (finalise) the ZIP — discard deferred close so errors are surfaced.
	return zw.Close()
}

// writeJSON marshals v and writes it as a file entry in the ZIP.
func writeJSON(zw *zip.Writer, name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %q: %w", name, err)
	}
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create %q: %w", name, err)
	}
	_, err = w.Write(data)
	return err
}

// writeText writes a string as a file entry in the ZIP.
func writeText(zw *zip.Writer, name, content string) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create %q: %w", name, err)
	}
	_, err = io.WriteString(w, content)
	return err
}

// writeMemories writes MemoryItem records as JSONL segment files.
// Multiple segments are used only when a single segment would be very large.
// For version 1, a single segment is sufficient for typical Core 1 state.
func writeMemories(zw *zip.Writer, items []dollstate.MemoryItem) error {
	if len(items) == 0 {
		return nil
	}

	// Single segment — named for lexicographic ordering.
	var buf bytes.Buffer
	for _, item := range items {
		line, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal memory item %q: %w", item.ID, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	w, err := zw.Create("memories/memories-000001.jsonl")
	if err != nil {
		return fmt.Errorf("create memories/memories-000001.jsonl: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write memories/memories-000001.jsonl: %w", err)
	}
	return nil
}

// EncodeToBytes is a convenience wrapper that returns the ZIP as a byte slice.
func EncodeToBytes(state *dollstate.DollState) ([]byte, error) {
	var buf bytes.Buffer
	if err := Encode(state, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
