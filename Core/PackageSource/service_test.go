package packagesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	artifact     []byte
	proposal     Proposal
	mu           sync.Mutex
	acknowledged map[string]bool
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acknowledged[s.proposal.ID] {
		return ProposalResult{Cursor: request.Cursor}, nil
	}
	return ProposalResult{Proposals: []Proposal{s.proposal}, Cursor: s.proposal.ID}, nil
}
func (s *proposalSource) AcknowledgeProposals(_ context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acknowledged == nil {
		s.acknowledged = map[string]bool{}
	}
	for _, id := range ids {
		s.acknowledged[id] = true
	}
	return nil
}
func (*proposalSource) RequestWorkflow(context.Context, WorkflowRequest) (Workflow, error) {
	return Workflow{}, ErrNotFound
}
func (*proposalSource) WorkflowStatus(context.Context, string) (Workflow, error) {
	return Workflow{}, ErrNotFound
}
func (*proposalSource) CancelWorkflow(context.Context, string) (Workflow, error) {
	return Workflow{}, ErrNotFound
}
func (*proposalSource) Close(context.Context) error { return nil }

type countingSkillInstaller struct {
	mu       sync.Mutex
	calls    int
	artifact []byte
}

type unorderedProposalSource struct {
	*proposalSource
	proposals []Proposal
}

func (s *unorderedProposalSource) Proposals(_ context.Context, _ ProposalRequest) (ProposalResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := ProposalResult{}
	for _, proposal := range s.proposals {
		if !s.acknowledged[proposal.ID] {
			result.Proposals = append(result.Proposals, proposal)
		}
	}
	return result, nil
}

func TestProposalIDsAreOpaqueAndRepeatedDeliverySurvivesRestart(t *testing.T) {
	ctx := context.Background()
	makeProposal := func(id string) Proposal {
		plan := InstallationPlan{Version: 1, ID: "plan-" + id, SourceID: "proposal-source", Operation: "install", CreatedAt: time.Now().UTC(), Approval: Approval{State: ApprovalReady}, ProposalID: id, Items: []PlanItem{{Package: Reference{SourceID: "proposal-source", ID: "skill-" + id, Version: "1"}, Kind: KindSkill, Artifact: Artifact{ID: "artifact-" + id, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 0, MediaType: "application/zip"}}}}
		return Proposal{ID: id, Plan: plan, Reason: "test", CreatedAt: time.Now().UTC()}
	}
	source := &unorderedProposalSource{proposalSource: &proposalSource{acknowledged: map[string]bool{}}, proposals: []Proposal{makeProposal("z-last-lexically"), makeProposal("a-first-lexically")}}
	registry := NewRegistry()
	if err := registry.Register(source); err != nil {
		t.Fatal(err)
	}
	executor, _ := NewExecutor(registry, &countingSkillInstaller{}, nil, ArtifactBounds{MaxBytes: 1024, MaxItems: 4})
	state := &memoryState{values: map[string][]byte{}}
	service, _ := NewService(ctx, registry, executor, state, "opaque")
	if err := service.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := service.Proposals(); len(got) != 2 {
		t.Fatalf("proposals=%+v", got)
	}
	// Simulate a source restart forgetting acknowledgements: stable IDs are
	// delivered again, but Core's durable store remains deduplicated.
	source.mu.Lock()
	source.acknowledged = map[string]bool{}
	source.mu.Unlock()
	if err := service.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := service.Proposals(); len(got) != 2 {
		t.Fatalf("repeated proposals=%+v", got)
	}
	restarted, err := NewService(ctx, registry, executor, state, "opaque")
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Proposals(); len(got) != 2 {
		t.Fatalf("restart proposals=%+v", got)
	}
}

type failSecondInstaller struct{ calls int }

func (s *failSecondInstaller) Install(context.Context, skill.ID, string, []byte) error {
	s.calls++
	if s.calls == 2 {
		return errors.New("second install failed")
	}
	return nil
}

func TestPartialPlanProgressIsExplicitAndDurable(t *testing.T) {
	ctx := context.Background()
	artifact := []byte("x")
	makeItem := func(id string) PlanItem {
		return PlanItem{Package: Reference{SourceID: "proposal-source", ID: id, Version: "1"}, Kind: KindSkill, Artifact: Artifact{ID: id, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 1, MediaType: "application/zip"}}
	}
	plan := InstallationPlan{Version: 1, ID: "partial", SourceID: "proposal-source", Operation: "install", CreatedAt: time.Now().UTC(), Approval: Approval{State: ApprovalReady}, ProposalID: "partial-proposal", Items: []PlanItem{makeItem("one"), makeItem("two")}}
	source := &proposalSource{artifact: artifact, proposal: Proposal{ID: "partial-proposal", Plan: plan, CreatedAt: time.Now().UTC()}, acknowledged: map[string]bool{}}
	registry := NewRegistry()
	_ = registry.Register(source)
	installer := &failSecondInstaller{}
	executor, _ := NewExecutor(registry, installer, nil, ArtifactBounds{MaxBytes: 8, MaxItems: 4})
	state := &memoryState{values: map[string][]byte{}}
	service, _ := NewService(ctx, registry, executor, state, "partial")
	if err := service.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := service.Accept(ctx, "proposal-source", "partial-proposal")
	if err == nil || report.Completed || !report.Partial || len(report.Items) != 2 || !report.Items[0].Installed || report.Items[1].Error == "" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	restarted, _ := NewService(ctx, registry, executor, state, "partial")
	stored := restarted.Proposals()
	if len(stored) != 1 || stored[0].State != ProposalFailed || !stored[0].Report.Partial {
		t.Fatalf("stored=%+v", stored)
	}
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
