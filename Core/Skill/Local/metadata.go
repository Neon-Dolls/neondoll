package local

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
	"gopkg.in/yaml.v3"
)

type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"`
}

func inspectDirectory(root, name string, configuration skill.Configuration) (entry, error) {
	directory := filepath.Join(root, name)
	data, err := secureReadNoContext(directory, "SKILL.md", configuration.MaxInstructionBytes)
	if err != nil {
		return entry{}, fmt.Errorf("skill local: %s: %w", name, err)
	}
	metadata, err := parseSkillDocument(data)
	if err != nil {
		return entry{}, fmt.Errorf("skill local: %s: %w", name, err)
	}
	if metadata.Name != name {
		return entry{}, fmt.Errorf("%w: name %q must match directory %q", skill.ErrInvalidSkill, metadata.Name, name)
	}
	revision := uint64(1)
	if raw := strings.TrimSpace(metadata.Metadata["neondoll.revision"]); raw != "" {
		revision, err = strconv.ParseUint(raw, 10, 64)
		if err != nil || revision == 0 {
			return entry{}, fmt.Errorf("%w: invalid neondoll.revision", skill.ErrInvalidSkill)
		}
	}
	descriptor := skill.Descriptor{SchemaVersion: skill.SchemaVersion, ID: skill.ID(metadata.Name), Revision: revision, Name: metadata.Name, Description: metadata.Description, Directory: name, DesiredActive: true}
	if err := descriptor.Validate(); err != nil {
		return entry{}, err
	}
	state, err := skill.ResolveInstalledState(descriptor, skill.AvailabilityFunc(func(skill.Dependency) bool { return true }))
	if err != nil {
		return entry{}, err
	}
	return entry{descriptor: descriptor, state: state, directory: directory, digest: sha256.Sum256(data)}, nil
}

func parseSkillDocument(data []byte) (frontmatter, error) {
	if !utf8.Valid(data) || len(data) < 8 {
		return frontmatter{}, skill.ErrInvalidSkill
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return frontmatter{}, fmt.Errorf("%w: YAML frontmatter is required", skill.ErrInvalidSkill)
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return frontmatter{}, fmt.Errorf("%w: unterminated YAML frontmatter", skill.ErrInvalidSkill)
	}
	var metadata frontmatter
	decoder := yaml.NewDecoder(strings.NewReader(text[4 : 4+end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&metadata); err != nil {
		return frontmatter{}, fmt.Errorf("%w: frontmatter: %v", skill.ErrInvalidSkill, err)
	}
	if !validSkillName(metadata.Name) || strings.TrimSpace(metadata.Description) == "" || utf8.RuneCountInString(metadata.Description) > 1024 || !utf8.ValidString(metadata.Description) || utf8.RuneCountInString(metadata.Compatibility) > 500 {
		return frontmatter{}, skill.ErrInvalidSkill
	}
	return metadata, nil
}

func validSkillName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] == '-' || name[len(name)-1] == '-' || strings.Contains(name, "--") {
		return false
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func secureReadNoContext(root, relative string, maxBytes int64) ([]byte, error) {
	return secureRead(context.Background(), root, relative, maxBytes)
}
