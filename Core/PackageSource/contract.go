// Package packagesource defines the versioned Package Source boundary. Sources
// own search, resolution, acquisition strategy, and approval workflow. Core
// validates and executes complete plans; it does not resolve dependencies.
package packagesource

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"
)

const ContractVersion = 1

type Configuration struct {
	LocalDirectory      string `json:"local_directory,omitempty"`
	MaxPackages         uint32 `json:"max_packages,omitempty"`
	MaxArtifactBytes    int64  `json:"max_artifact_bytes,omitempty"`
	MaxPlanItems        uint32 `json:"max_plan_items,omitempty"`
	ProposalPollSeconds int64  `json:"proposal_poll_seconds,omitempty"`
}

func DefaultConfiguration() Configuration {
	return Configuration{LocalDirectory: "packages/local", MaxPackages: 128, MaxArtifactBytes: 16 << 20, MaxPlanItems: 32, ProposalPollSeconds: 30}
}

func (c Configuration) Validate() error {
	if strings.TrimSpace(c.LocalDirectory) == "" || c.MaxPackages == 0 || c.MaxArtifactBytes <= 0 || c.MaxPlanItems == 0 || c.ProposalPollSeconds <= 0 {
		return ErrInvalid
	}
	return nil
}

var (
	ErrInvalid       = errors.New("package source: invalid contract")
	ErrNotFound      = errors.New("package source: package not found")
	ErrConflict      = errors.New("package source: conflict")
	ErrBounds        = errors.New("package source: transfer bounds exceeded")
	ErrNotReady      = errors.New("package source: plan is not ready")
	ErrSourceClosed  = errors.New("package source: closed")
	ErrStateNotFound = errors.New("package source: state not found")
)

type Kind string

const (
	KindSkill     Kind = "skill"
	KindExtension Kind = "extension"
)

type Reference struct {
	SourceID string `json:"source_id"`
	ID       string `json:"id"`
	Version  string `json:"version"`
}

func (r Reference) Valid() bool {
	return strings.TrimSpace(r.SourceID) != "" && strings.TrimSpace(r.ID) != "" && strings.TrimSpace(r.Version) != ""
}

type Dependency struct {
	Reference Reference `json:"reference"`
	Optional  bool      `json:"optional,omitempty"`
}

type Summary struct {
	Reference   Reference `json:"reference"`
	Kind        Kind      `json:"kind"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
}

type SearchRequest struct {
	Query string `json:"query,omitempty"`
	Kinds []Kind `json:"kinds,omitempty"`
	Limit uint32 `json:"limit"`
}

type SearchResult struct {
	Packages []Summary `json:"packages"`
	Cursor   string    `json:"cursor,omitempty"`
}

type PlanRequest struct {
	Package   Reference `json:"package"`
	Operation string    `json:"operation"` // install or update
}

type Artifact struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

type ApprovalState string

const (
	ApprovalReady    ApprovalState = "ready"
	ApprovalPending  ApprovalState = "pending"
	ApprovalRejected ApprovalState = "rejected"
)

type Approval struct {
	State     ApprovalState `json:"state"`
	Reference string        `json:"reference,omitempty"`
	Message   string        `json:"message,omitempty"`
}

type PlanItem struct {
	Package      Reference    `json:"package"`
	Kind         Kind         `json:"kind"`
	Artifact     Artifact     `json:"artifact"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
	// ExtensionManifest is present only for extension packages. It is the
	// source-proposed manifest; Core still validates and grants capabilities.
	ExtensionManifest []byte `json:"extension_manifest,omitempty"`
}

type InstallationPlan struct {
	Version     int        `json:"version"`
	ID          string     `json:"id"`
	SourceID    string     `json:"source_id"`
	Operation   string     `json:"operation"`
	CreatedAt   time.Time  `json:"created_at"`
	Approval    Approval   `json:"approval"`
	Items       []PlanItem `json:"items"`
	ProposalID  string     `json:"proposal_id,omitempty"`
	Correlation string     `json:"correlation,omitempty"`
}

func (p InstallationPlan) Validate() error {
	if p.Version != ContractVersion || strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.SourceID) == "" ||
		(p.Operation != "install" && p.Operation != "update") || p.CreatedAt.IsZero() || len(p.Items) == 0 {
		return ErrInvalid
	}
	if p.Approval.State != ApprovalReady && p.Approval.State != ApprovalPending && p.Approval.State != ApprovalRejected {
		return ErrInvalid
	}
	packages := map[string]bool{}
	for _, item := range p.Items {
		if !item.Package.Valid() || item.Package.SourceID != p.SourceID || (item.Kind != KindSkill && item.Kind != KindExtension) ||
			strings.TrimSpace(item.Artifact.ID) == "" || len(item.Artifact.SHA256) != 64 || item.Artifact.Size < 0 || strings.TrimSpace(item.Artifact.MediaType) == "" {
			return ErrInvalid
		}
		if _, err := hex.DecodeString(item.Artifact.SHA256); err != nil {
			return ErrInvalid
		}
		key := item.Package.SourceID + "\x00" + item.Package.ID + "\x00" + item.Package.Version
		if packages[key] {
			return ErrInvalid
		}
		packages[key] = true
	}
	for _, item := range p.Items {
		for _, dependency := range item.Dependencies {
			key := dependency.Reference.SourceID + "\x00" + dependency.Reference.ID + "\x00" + dependency.Reference.Version
			if !dependency.Reference.Valid() || !dependency.Optional && !packages[key] {
				return ErrInvalid
			}
		}
	}
	return nil
}

type Proposal struct {
	ID        string           `json:"id"`
	Plan      InstallationPlan `json:"plan"`
	Reason    string           `json:"reason"`
	CreatedAt time.Time        `json:"created_at"`
}

type ProposalRequest struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  uint32 `json:"limit"`
}

type ProposalResult struct {
	Proposals []Proposal `json:"proposals"`
	Cursor    string     `json:"cursor,omitempty"`
}

type Source interface {
	ID() string
	Search(context.Context, SearchRequest) (SearchResult, error)
	Plan(context.Context, PlanRequest) (InstallationPlan, error)
	Acquire(context.Context, Artifact, io.Writer) error
	Proposals(context.Context, ProposalRequest) (ProposalResult, error)
	Close(context.Context) error
}

type ArtifactBounds struct {
	MaxBytes int64
	MaxItems uint32
}
