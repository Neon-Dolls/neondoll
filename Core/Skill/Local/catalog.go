// Package local provides Core's local/offline Agent Skills catalog. The
// configured directory is the durable installation source; Core does not copy
// it into a second registry or grant authority from its contents.
package local

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	return len(catalog.entries)
}

func (catalog *Catalog) Discover(ctx context.Context, request skill.DiscoverRequest) ([]skill.Summary, error) {
	if catalog == nil || request.Limit == 0 {
		return nil, skill.ErrInvalidSkill
	}
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
		loaded.Resources = append(loaded.Resources, skill.NewFile(name, data))
	}
	return loaded, nil
}

func secureRead(ctx context.Context, root, relative string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(root, relative)
	target, err = filepath.EvalSymlinks(target)
	if err != nil || !within(root, target) {
		if errors.Is(err, os.ErrNotExist) {
			return nil, skill.ErrNotFound
		}
		return nil, fmt.Errorf("%w: resource escapes or is inaccessible", skill.ErrInvalidSkill)
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return nil, skill.ErrNotFound
	}
	if info.Size() > maxBytes {
		return nil, skill.ErrTransferBounds
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("skill local: read %q: %w", relative, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, skill.ErrTransferBounds
	}
	return data, nil
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
