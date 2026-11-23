// Package local provides Core's always-available offline Package Source.
package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	packagesource "github.com/Neon-Dolls/neondoll/Core/PackageSource"
)

const SourceID = "local"

type manifest struct {
	Version           int                        `json:"version"`
	ID                string                     `json:"id"`
	PackageVersion    string                     `json:"package_version"`
	Kind              packagesource.Kind         `json:"kind"`
	Name              string                     `json:"name"`
	Description       string                     `json:"description"`
	ArtifactPath      string                     `json:"artifact_path"`
	ArtifactSHA256    string                     `json:"artifact_sha256"`
	ArtifactMediaType string                     `json:"artifact_media_type"`
	Dependencies      []packagesource.Dependency `json:"dependencies,omitempty"`
	ExtensionManifest json.RawMessage            `json:"extension_manifest,omitempty"`
	Proposal          bool                       `json:"proposal,omitempty"`
	ProposalReason    string                     `json:"proposal_reason,omitempty"`
}

type item struct {
	manifest manifest
	artifact packagesource.Artifact
	file     string
}

type Source struct {
	mu           sync.RWMutex
	root         string
	config       packagesource.Configuration
	items        map[string]item
	acknowledged map[string]bool
	workflows    map[string]packagesource.Workflow
	closed       bool
}

func Open(dataRoot string, configuration packagesource.Configuration) (*Source, error) {
	if strings.TrimSpace(dataRoot) == "" || configuration.Validate() != nil {
		return nil, packagesource.ErrInvalid
	}
	root, err := confinedRoot(dataRoot, configuration.LocalDirectory)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	source := &Source{root: root, config: configuration, items: map[string]item{}, acknowledged: map[string]bool{}, workflows: map[string]packagesource.Workflow{}}
	if err = source.refresh(); err != nil {
		return nil, err
	}
	return source, nil
}

func (s *Source) ID() string { return SourceID }

func (s *Source) refresh() error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	loaded := map[string]item{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if uint32(len(loaded)) >= s.config.MaxPackages {
			return packagesource.ErrBounds
		}
		data, err := readRootFile(s.root, entry.Name(), 256<<10)
		if err != nil {
			return err
		}
		var m manifest
		if json.Unmarshal(data, &m) != nil || m.Version != packagesource.ContractVersion || m.ID == "" || m.PackageVersion == "" ||
			(m.Kind != packagesource.KindSkill && m.Kind != packagesource.KindExtension) || m.Name == "" || m.Description == "" ||
			!safePath(m.ArtifactPath) || len(m.ArtifactSHA256) != 64 || m.ArtifactMediaType == "" {
			return fmt.Errorf("%w: %s", packagesource.ErrInvalid, entry.Name())
		}
		artifactData, err := readRootFile(s.root, filepath.FromSlash(m.ArtifactPath), s.config.MaxArtifactBytes)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(artifactData)
		if !strings.EqualFold(hex.EncodeToString(digest[:]), m.ArtifactSHA256) {
			return packagesource.ErrInvalid
		}
		key := m.ID + "@" + m.PackageVersion
		if _, exists := loaded[key]; exists {
			return packagesource.ErrConflict
		}
		loaded[key] = item{manifest: m, file: m.ArtifactPath, artifact: packagesource.Artifact{ID: key, SHA256: strings.ToLower(m.ArtifactSHA256), Size: int64(len(artifactData)), MediaType: m.ArtifactMediaType}}
	}
	s.mu.Lock()
	s.items = loaded
	s.mu.Unlock()
	return nil
}

func (s *Source) Search(ctx context.Context, request packagesource.SearchRequest) (packagesource.SearchResult, error) {
	if request.Limit == 0 {
		return packagesource.SearchResult{}, packagesource.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return packagesource.SearchResult{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return packagesource.SearchResult{}, packagesource.ErrSourceClosed
	}
	keys := make([]string, 0, len(s.items))
	for key := range s.items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	wanted := map[packagesource.Kind]bool{}
	for _, kind := range request.Kinds {
		wanted[kind] = true
	}
	query := strings.ToLower(strings.TrimSpace(request.Query))
	result := packagesource.SearchResult{}
	for _, key := range keys {
		item := s.items[key]
		if len(wanted) > 0 && !wanted[item.manifest.Kind] {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(item.manifest.ID+"\n"+item.manifest.Name+"\n"+item.manifest.Description), query) {
			continue
		}
		result.Packages = append(result.Packages, packagesource.Summary{Reference: packagesource.Reference{SourceID: SourceID, ID: item.manifest.ID, Version: item.manifest.PackageVersion}, Kind: item.manifest.Kind, Name: item.manifest.Name, Description: item.manifest.Description})
		if uint32(len(result.Packages)) == request.Limit {
			break
		}
	}
	return result, nil
}

func (s *Source) Plan(ctx context.Context, request packagesource.PlanRequest) (packagesource.InstallationPlan, error) {
	if err := ctx.Err(); err != nil {
		return packagesource.InstallationPlan{}, err
	}
	if !request.Package.Valid() || request.Package.SourceID != SourceID || (request.Operation != "install" && request.Operation != "update") {
		return packagesource.InstallationPlan{}, packagesource.ErrInvalid
	}
	s.mu.RLock()
	selected, ok := s.items[request.Package.ID+"@"+request.Package.Version]
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return packagesource.InstallationPlan{}, packagesource.ErrSourceClosed
	}
	if !ok {
		return packagesource.InstallationPlan{}, packagesource.ErrNotFound
	}
	items := []packagesource.PlanItem{}
	visiting, resolved := map[string]bool{}, map[string]bool{}
	var resolve func(item) error
	resolve = func(candidate item) error {
		key := candidate.manifest.ID + "@" + candidate.manifest.PackageVersion
		if resolved[key] {
			return nil
		}
		if visiting[key] {
			return packagesource.ErrConflict
		}
		visiting[key] = true
		for _, dependency := range candidate.manifest.Dependencies {
			if dependency.Reference.SourceID != SourceID {
				if dependency.Optional {
					continue
				}
				return packagesource.ErrNotFound
			}
			next, exists := s.items[dependency.Reference.ID+"@"+dependency.Reference.Version]
			if !exists {
				if dependency.Optional {
					continue
				}
				return packagesource.ErrNotFound
			}
			if err := resolve(next); err != nil {
				return err
			}
		}
		visiting[key] = false
		resolved[key] = true
		items = append(items, packagesource.PlanItem{Package: packagesource.Reference{SourceID: SourceID, ID: candidate.manifest.ID, Version: candidate.manifest.PackageVersion}, Kind: candidate.manifest.Kind, Artifact: candidate.artifact, Dependencies: append([]packagesource.Dependency(nil), candidate.manifest.Dependencies...), ExtensionManifest: append([]byte(nil), candidate.manifest.ExtensionManifest...)})
		return nil
	}
	if err := resolve(selected); err != nil {
		return packagesource.InstallationPlan{}, err
	}
	plan := packagesource.InstallationPlan{Version: packagesource.ContractVersion, ID: SourceID + ":" + request.Operation + ":" + request.Package.ID + "@" + request.Package.Version, SourceID: SourceID, Operation: request.Operation, CreatedAt: time.Now().UTC(), Approval: packagesource.Approval{State: packagesource.ApprovalReady}, Items: items}
	return plan, plan.Validate()
}

func (s *Source) Acquire(ctx context.Context, artifact packagesource.Artifact, destination io.Writer) error {
	if destination == nil || artifact.Size < 0 || artifact.Size > s.config.MaxArtifactBytes {
		return packagesource.ErrBounds
	}
	s.mu.RLock()
	var found item
	ok := false
	for _, candidate := range s.items {
		if candidate.artifact.ID == artifact.ID {
			found, ok = candidate, true
			break
		}
	}
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return packagesource.ErrSourceClosed
	}
	if !ok || found.artifact != artifact {
		return packagesource.ErrNotFound
	}
	data, err := readRootFile(s.root, filepath.FromSlash(found.file), s.config.MaxArtifactBytes)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 || int64(len(data)) != artifact.Size {
		return packagesource.ErrConflict
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	_, err = destination.Write(data)
	return err
}

func (s *Source) Proposals(ctx context.Context, request packagesource.ProposalRequest) (packagesource.ProposalResult, error) {
	if request.Limit == 0 {
		return packagesource.ProposalResult{}, packagesource.ErrInvalid
	}
	search, err := s.Search(ctx, packagesource.SearchRequest{Limit: s.config.MaxPackages})
	if err != nil {
		return packagesource.ProposalResult{}, err
	}
	result := packagesource.ProposalResult{}
	for _, summary := range search.Packages {
		s.mu.RLock()
		candidate := s.items[summary.Reference.ID+"@"+summary.Reference.Version]
		s.mu.RUnlock()
		if !candidate.manifest.Proposal {
			continue
		}
		plan, err := s.Plan(ctx, packagesource.PlanRequest{Package: summary.Reference, Operation: "install"})
		if err != nil {
			return result, err
		}
		proposalID := "local:" + summary.Reference.ID + "@" + summary.Reference.Version
		s.mu.RLock()
		acknowledged := s.acknowledged[proposalID]
		s.mu.RUnlock()
		if acknowledged {
			continue
		}
		plan.ProposalID = proposalID
		result.Proposals = append(result.Proposals, packagesource.Proposal{ID: proposalID, Plan: plan, Reason: candidate.manifest.ProposalReason, CreatedAt: plan.CreatedAt})
		result.Cursor = proposalID
		if uint32(len(result.Proposals)) == request.Limit {
			break
		}
	}
	return result, nil
}

func (s *Source) AcknowledgeProposals(_ context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return packagesource.ErrSourceClosed
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return packagesource.ErrInvalid
		}
		s.acknowledged[id] = true
	}
	return nil
}

func (s *Source) RequestWorkflow(ctx context.Context, request packagesource.WorkflowRequest) (packagesource.Workflow, error) {
	plan, err := s.Plan(ctx, packagesource.PlanRequest{Package: request.Package, Operation: request.Operation})
	if err != nil {
		return packagesource.Workflow{}, err
	}
	plan.Correlation = request.Correlation
	workflow := packagesource.Workflow{ID: "local-workflow:" + plan.ID, State: packagesource.WorkflowReady, Plan: &plan, UpdatedAt: time.Now().UTC()}
	s.mu.Lock()
	s.workflows[workflow.ID] = workflow
	s.mu.Unlock()
	return workflow, nil
}

func (s *Source) WorkflowStatus(_ context.Context, id string) (packagesource.Workflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	workflow, ok := s.workflows[id]
	if !ok {
		return packagesource.Workflow{}, packagesource.ErrNotFound
	}
	return workflow, nil
}

func (s *Source) CancelWorkflow(_ context.Context, id string) (packagesource.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, ok := s.workflows[id]
	if !ok {
		return packagesource.Workflow{}, packagesource.ErrNotFound
	}
	if workflow.State == packagesource.WorkflowReady || workflow.State == packagesource.WorkflowPending {
		workflow.State = packagesource.WorkflowCancelled
		workflow.Plan = nil
		workflow.UpdatedAt = time.Now().UTC()
		s.workflows[id] = workflow
	}
	return workflow, nil
}

func (s *Source) Close(context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}
