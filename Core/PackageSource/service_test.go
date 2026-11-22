package packagesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

type memoryState struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *memoryState) Get(_ context.Context, instance, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[instance+"/"+key]
	if !ok {
		return nil, ErrStateNotFound
	}
	return append([]byte(nil), v...), nil
}
func (s *memoryState) Put(_ context.Context, instance, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[instance+"/"+key] = append([]byte(nil), value...)
	return nil
}

type proposalSource struct {
	artifact []byte
	proposal Proposal
}

func (s *proposalSource) ID() string { return "proposal-source" }
func (*proposalSource) Search(context.Context, SearchRequest) (SearchResult, error) {
	return SearchResult{}, nil
}
func (*proposalSource) Plan(context.Context, PlanRequest) (InstallationPlan, error) {
	return InstallationPlan{}, nil
}
func (s *proposalSource) Acquire(_ context.Context, _ Artifact, w io.Writer) error {
	_, err := w.Write(s.artifact)
	return err
}
func (s *proposalSource) Proposals(_ context.Context, request ProposalRequest) (ProposalResult, error) {
	if request.Cursor >= s.proposal.ID {
		return ProposalResult{Cursor: request.Cursor}, nil
	}
	return ProposalResult{Proposals: []Proposal{s.proposal}, Cursor: s.proposal.ID}, nil
}
func (*proposalSource) Close(context.Context) error { return nil }

type countingSkillInstaller struct {
	mu       sync.Mutex
	calls    int
	artifact []byte
}

func (s *countingSkillInstaller) Install(_ context.Context, _ skill.ID, _ string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.artifact = append([]byte(nil), data...)
	return nil
}

func TestProposalsPersistDeduplicateAndExecuteOnce(t *testing.T) {
	ctx := context.Background()
	artifact := []byte("package")
	digest := sha256.Sum256(artifact)
	plan := InstallationPlan{Version: 1, ID: "plan-1", SourceID: "proposal-source", Operation: "install", CreatedAt: time.Now().UTC(), Approval: Approval{State: ApprovalReady}, ProposalID: "proposal-1", Items: []PlanItem{{Package: Reference{SourceID: "proposal-source", ID: "skill", Version: "1"}, Kind: KindSkill, Artifact: Artifact{ID: "artifact", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(artifact)), MediaType: "application/zip"}}}}
	source := &proposalSource{artifact: artifact, proposal: Proposal{ID: "proposal-1", Plan: plan, Reason: "update", CreatedAt: time.Now().UTC()}}
	registry := NewRegistry()
	if err := registry.Register(source); err != nil {
		t.Fatal(err)
	}
	installer := &countingSkillInstaller{}
	executor, _ := NewExecutor(registry, installer, nil, ArtifactBounds{MaxBytes: 1024, MaxItems: 2})
	state := &memoryState{values: map[string][]byte{}}
	service, err := NewService(ctx, registry, executor, state, "core")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if err = service.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if proposals := service.Proposals(); len(proposals) != 1 || proposals[0].State != ProposalPending {
		t.Fatalf("proposals=%+v", proposals)
	}
	report, err := service.Accept(ctx, "proposal-source", "proposal-1")
	if err != nil || !report.Completed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if _, err = service.Accept(ctx, "proposal-source", "proposal-1"); err != ErrConflict {
		t.Fatalf("repeat accept=%v", err)
	}
	if installer.calls != 1 || !bytes.Equal(installer.artifact, artifact) {
		t.Fatalf("calls=%d artifact=%q", installer.calls, installer.artifact)
	}
	restarted, err := NewService(ctx, registry, executor, state, "core")
	if err != nil {
		t.Fatal(err)
	}
	stored := restarted.Proposals()
	if len(stored) != 1 || stored[0].State != ProposalAccepted {
		t.Fatalf("restart=%+v", stored)
	}
}
