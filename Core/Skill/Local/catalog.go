// Package local provides Core's local/offline Agent Skills catalog. The
// configured directory is the durable installation source; Core does not copy
// it into a second registry or grant authority from its contents.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

type Options struct{ DataRoot string }

type entry struct {
	descriptor skill.Descriptor
	state      skill.InstalledState
	directory  string
	digest     [sha256.Size]byte
}

type Catalog struct {
	mu            sync.RWMutex
	root          string
	configuration skill.Configuration
	entries       map[skill.ID]entry
	ordered       []skill.ID
}

func Open(configuration skill.Configuration, options Options) (*Catalog, error) {
	if !configuration.Enabled() {
		return nil, nil
	}
	if err := configuration.Validate(); err != nil {
		return nil, err
	}
	root, err := resolveRoot(options.DataRoot, configuration.Directory)
	if err != nil {
		return nil, err
	}
	directories, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("skill local: read catalog: %w", err)
	}
	catalog := &Catalog{root: root, configuration: configuration, entries: make(map[skill.ID]entry)}
	for _, directory := range directories {
		if !directory.IsDir() || directory.Type()&os.ModeSymlink != 0 {
			continue
		}
		if uint32(len(catalog.entries)) >= configuration.MaxSkills {
			return nil, fmt.Errorf("%w: local catalog exceeds max_skills", skill.ErrTransferBounds)
		}
		loaded, loadErr := inspectDirectory(root, directory.Name(), configuration)
		if loadErr != nil {
			return nil, loadErr
		}
		if err := catalog.add(loaded); err != nil {
			return nil, err
		}
	}
	sort.Slice(catalog.ordered, func(i, j int) bool { return catalog.ordered[i] < catalog.ordered[j] })
	return catalog, nil
}

func (catalog *Catalog) add(loaded entry) error {
	if _, duplicate := catalog.entries[loaded.descriptor.ID]; duplicate {
		return fmt.Errorf("%w: duplicate ID %q", skill.ErrInvalidSkill, loaded.descriptor.ID)
	}
	catalog.entries[loaded.descriptor.ID] = loaded
	catalog.ordered = append(catalog.ordered, loaded.descriptor.ID)
	return nil
}

func (catalog *Catalog) Count() int {
	if catalog == nil {
		return 0
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	return len(catalog.entries)
}

func (catalog *Catalog) Discover(ctx context.Context, request skill.DiscoverRequest) ([]skill.Summary, error) {
	if catalog == nil || request.Limit == 0 {
		return nil, skill.ErrInvalidSkill
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	query := strings.ToLower(strings.TrimSpace(request.Query))
	limit := int(request.Limit)
	result := make([]skill.Summary, 0, min(limit, len(catalog.entries)))
	for _, id := range catalog.ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := catalog.entries[id]
		haystack := strings.ToLower(string(id) + "\n" + entry.descriptor.Name + "\n" + entry.descriptor.Description)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		result = append(result, skill.Summary{Reference: skill.Reference{ID: id, Revision: entry.descriptor.Revision}, Name: entry.descriptor.Name, Description: entry.descriptor.Description, Activation: entry.state.Activation, Source: "local", Directory: entry.descriptor.Directory})
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (catalog *Catalog) Load(ctx context.Context, request skill.LoadRequest) (skill.Loaded, error) {
	if catalog == nil || request.Reference.ID == "" || request.Reference.Revision == 0 || uint32(len(request.Resources)) > catalog.configuration.MaxResources {
		return skill.Loaded{}, skill.ErrInvalidSkill
	}
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	entry, ok := catalog.entries[request.Reference.ID]
	if !ok || entry.descriptor.Revision != request.Reference.Revision {
		return skill.Loaded{}, skill.ErrNotFound
	}
	if entry.state.Activation != skill.ActivationActive {
		return skill.Loaded{}, skill.ErrUnavailable
	}
	instructions, err := secureRead(ctx, entry.directory, "SKILL.md", catalog.configuration.MaxInstructionBytes)
	if err != nil {
		return skill.Loaded{}, err
	}
	if sha256.Sum256(instructions) != entry.digest {
		return skill.Loaded{}, fmt.Errorf("%w: SKILL.md changed after discovery", skill.ErrIntegrity)
	}
	loaded := skill.Loaded{Descriptor: entry.descriptor, Instructions: instructions}
	if loadResponseSize(loaded) > catalog.configuration.MaxLoadBytes {
		return skill.Loaded{}, skill.ErrTransferBounds
	}
	seen := make(map[string]bool, len(request.Resources))
	for _, name := range request.Resources {
		if !skill.SafeRelativePath(name) || name == "SKILL.md" || seen[name] {
			return skill.Loaded{}, skill.ErrInvalidSkill
		}
		seen[name] = true
		data, readErr := secureRead(ctx, entry.directory, filepath.FromSlash(name), catalog.configuration.MaxResourceBytes)
		if readErr != nil {
			return skill.Loaded{}, readErr
		}
		resource := skill.NewFile(name, data)
		candidate := loaded
		candidate.Resources = append(append([]skill.File(nil), loaded.Resources...), resource)
		if loadResponseSize(candidate) > catalog.configuration.MaxLoadBytes {
			return skill.Loaded{}, skill.ErrTransferBounds
		}
		loaded.Resources = append(loaded.Resources, resource)
	}
	return loaded, nil
}

func secureRead(ctx context.Context, root, relative string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("skill local: open root: %w", err)
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, skill.ErrNotFound
		}
		return nil, fmt.Errorf("%w: resource escapes or is inaccessible", skill.ErrInvalidSkill)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, skill.ErrNotFound
	}
	if info.Size() > maxBytes {
		return nil, skill.ErrTransferBounds
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("skill local: read %q: %w", relative, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, skill.ErrTransferBounds
	}
	return data, nil
}

// loadResponseSize returns the exact JSON size of the successful skill tool
// result. It is evaluated incrementally, before DollMind constructs the full
// response, so encoded binary and escaped text cannot bypass MaxLoadBytes.
func loadResponseSize(loaded skill.Loaded) int64 {
	resources := make([]map[string]any, 0, len(loaded.Resources))
	for _, resource := range loaded.Resources {
		item := map[string]any{"path": resource.Path, "sha256": resource.SHA256, "size": len(resource.Data)}
		if utf8.Valid(resource.Data) {
			item["encoding"], item["content"] = "utf-8", string(resource.Data)
		} else {
			item["encoding"], item["content_base64"] = "base64", base64.StdEncoding.EncodeToString(resource.Data)
		}
		resources = append(resources, item)
	}
	payload, err := json.Marshal(map[string]any{
		"descriptor": loaded.Descriptor, "instructions": string(loaded.Instructions), "resources": resources,
		"authority": "contextual_guidance_only", "instructions_loaded": true,
	})
	if err != nil {
		return int64(^uint64(0) >> 1)
	}
	return int64(len(payload))
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
