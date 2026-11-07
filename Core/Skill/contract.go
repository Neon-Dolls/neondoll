// Package skill defines Core's portable Agent Skills catalog contract. It
// owns no installation, selection, execution, or authorization runtime.
package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
)

const SchemaVersion = 1

var (
	ErrInvalidSkill   = errors.New("skill: invalid contract")
	ErrIntegrity      = errors.New("skill: integrity mismatch")
	ErrTransferBounds = errors.New("skill: transfer bounds exceeded")
)

type ID string

type Dependency struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Constraint string `json:"constraint,omitempty"`
}

type Compatibility struct {
	Contract string `json:"contract"`
	Minimum  int    `json:"minimum"`
	Maximum  int    `json:"maximum,omitempty"`
}

// Descriptor is concise Core catalog metadata. SKILL.md remains the canonical
// Agent Skills metadata and instructions; these fields do not replace it.
type Descriptor struct {
	SchemaVersion int             `json:"schema_version"`
	ID            ID              `json:"id"`
	Revision      uint64          `json:"revision"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Directory     string          `json:"directory"`
	Dependencies  []Dependency    `json:"dependencies,omitempty"`
	Compatibility []Compatibility `json:"compatibility,omitempty"`
	DesiredActive bool            `json:"desired_active"`
}

func (d Descriptor) Validate() error {
	if d.SchemaVersion != SchemaVersion || strings.TrimSpace(string(d.ID)) == "" || d.Revision == 0 ||
		strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Description) == "" || !SafeRelativePath(d.Directory) {
		return ErrInvalidSkill
	}
	seen := map[string]bool{}
	for _, dependency := range d.Dependencies {
		if strings.TrimSpace(dependency.ID) == "" || strings.TrimSpace(dependency.Kind) == "" || seen[dependency.Kind+"\x00"+dependency.ID] {
			return ErrInvalidSkill
		}
		seen[dependency.Kind+"\x00"+dependency.ID] = true
	}
	for _, requirement := range d.Compatibility {
		key := requirement.Contract
		if strings.TrimSpace(requirement.Contract) == "" || requirement.Minimum <= 0 || requirement.Maximum != 0 && requirement.Maximum < requirement.Minimum || seen["compat\x00"+key] {
			return ErrInvalidSkill
		}
		seen["compat\x00"+key] = true
	}
	return nil
}

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"data"`
}

func NewFile(name string, data []byte) File {
	digest := sha256.Sum256(data)
	return File{Path: name, SHA256: hex.EncodeToString(digest[:]), Data: append([]byte(nil), data...)}
}

type Package struct {
	Descriptor Descriptor `json:"descriptor"`
	Files      []File     `json:"files"`
}

func (p Package) Validate() error {
	if err := p.Descriptor.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	hasInstructions := false
	for _, file := range p.Files {
		if !SafeRelativePath(file.Path) || seen[file.Path] {
			return ErrInvalidSkill
		}
		seen[file.Path] = true
		if file.Path == "SKILL.md" {
			hasInstructions = true
		}
		digest := sha256.Sum256(file.Data)
		if len(file.SHA256) != 64 || !strings.EqualFold(file.SHA256, hex.EncodeToString(digest[:])) {
			return ErrIntegrity
		}
	}
	if !hasInstructions {
		return fmt.Errorf("%w: SKILL.md is required", ErrInvalidSkill)
	}
	return nil
}

func SafeRelativePath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

type Activation string

const (
	ActivationActive  Activation = "active"
	ActivationDormant Activation = "dormant"
)

type InstalledState struct {
	Installed  bool         `json:"installed"`
	Activation Activation   `json:"activation"`
	Missing    []Dependency `json:"missing_dependencies,omitempty"`
}

type Availability interface{ Available(Dependency) bool }
type AvailabilityFunc func(Dependency) bool

func (f AvailabilityFunc) Available(dependency Dependency) bool { return f(dependency) }

func ResolveInstalledState(descriptor Descriptor, available Availability) (InstalledState, error) {
	if err := descriptor.Validate(); err != nil || available == nil {
		return InstalledState{}, ErrInvalidSkill
	}
	state := InstalledState{Installed: true, Activation: ActivationActive}
	for _, dependency := range descriptor.Dependencies {
		if !available.Available(dependency) {
			state.Missing = append(state.Missing, dependency)
		}
	}
	if !descriptor.DesiredActive || len(state.Missing) > 0 {
		state.Activation = ActivationDormant
	}
	return state, nil
}

type TransferBounds struct {
	MaxSkills uint64
	MaxFiles  uint64
	MaxBytes  uint64
}

func ValidatePackages(packages []Package, bounds TransferBounds) error {
	if bounds.MaxSkills == 0 || bounds.MaxFiles == 0 || bounds.MaxBytes == 0 || uint64(len(packages)) > bounds.MaxSkills {
		return ErrTransferBounds
	}
	var files, bytes uint64
	directories := map[string]bool{}
	ids := map[ID]bool{}
	for _, item := range packages {
		if err := item.Validate(); err != nil {
			return err
		}
		if directories[item.Descriptor.Directory] || ids[item.Descriptor.ID] {
			return ErrInvalidSkill
		}
		directories[item.Descriptor.Directory], ids[item.Descriptor.ID] = true, true
		for _, file := range item.Files {
			files++
			fileBytes := uint64(len(file.Data))
			if files > bounds.MaxFiles || fileBytes > bounds.MaxBytes-bytes {
				return ErrTransferBounds
			}
			bytes += fileBytes
		}
	}
	return nil
}

type Surface string

const (
	SurfaceInteraction Surface = "interaction"
	SurfaceOrient      Surface = "orient"
	SurfacePlan        Surface = "plan"
	SurfaceReflection  Surface = "reflection"
)

type Reference struct {
	ID       ID     `json:"id"`
	Revision uint64 `json:"revision"`
}
type Summary struct {
	Reference   Reference  `json:"reference"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Activation  Activation `json:"activation"`
}
type DiscoverRequest struct {
	Query   string
	Surface Surface
	Limit   uint32
}
type LoadRequest struct {
	Reference Reference
	Resources []string
}
type Loaded struct {
	Descriptor   Descriptor
	Instructions []byte
	Resources    []File
}

// Catalog makes discovery and loading explicit so Core never injects every
// installed Skill into every cognitive context.
type Catalog interface {
	Discover(context.Context, DiscoverRequest) ([]Summary, error)
	Load(context.Context, LoadRequest) (Loaded, error)
}

type SelectionRecord struct {
	SchemaVersion int         `json:"schema_version"`
	TaskReference string      `json:"task_reference"`
	Surface       Surface     `json:"surface"`
	Selected      *Reference  `json:"selected,omitempty"`
	Alternatives  []Reference `json:"alternatives,omitempty"`
	Summary       string      `json:"summary"`
	Outcome       string      `json:"outcome,omitempty"`
}

func (r SelectionRecord) Validate() error {
	if r.SchemaVersion != SchemaVersion || strings.TrimSpace(r.TaskReference) == "" || strings.TrimSpace(r.Summary) == "" || !validSurface(r.Surface) {
		return ErrInvalidSkill
	}
	if r.Selected != nil && (r.Selected.ID == "" || r.Selected.Revision == 0) {
		return ErrInvalidSkill
	}
	for _, alternative := range r.Alternatives {
		if alternative.ID == "" || alternative.Revision == 0 {
			return ErrInvalidSkill
		}
	}
	return nil
}

func validSurface(surface Surface) bool {
	return surface == SurfaceInteraction || surface == SurfaceOrient || surface == SurfacePlan || surface == SurfaceReflection
}
