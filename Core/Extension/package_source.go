package extension

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"

	packagesource "github.com/Neon-Dolls/neondoll/Core/PackageSource"
)

type packageSourceEnvelope struct {
	EventType string          `json:"event_type"`
	RequestID string          `json:"request_id,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Data      string          `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// WASMPackageSource adapts an independently built Package Source guest to the
// provider-neutral Core contract. The manifest's Core-assigned instance ID is
// authoritative; guest payloads cannot select another source identity.
type WASMPackageSource struct {
	manifest        Manifest
	runtime         *WASMRuntime
	sequence        atomic.Uint64
	callMu          sync.Mutex
	proposalMu      sync.Mutex
	proposals       map[string]packagesource.Proposal
	proposalsLoaded bool
}

const packageSourceProposalStateKey = "package-source/pending-proposals-v1"

func NewWASMPackageSource(manifest Manifest, runtime *WASMRuntime) *WASMPackageSource {
	return &WASMPackageSource{manifest: manifest, runtime: runtime, proposals: map[string]packagesource.Proposal{}}
}

func (s *WASMPackageSource) ID() string { return s.manifest.InstanceID }

func (s *WASMPackageSource) invoke(ctx context.Context, operation string, request any) ([]packageSourceEnvelope, error) {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	id := fmt.Sprintf("%s:%d", s.manifest.InstanceID, s.sequence.Add(1))
	payload, err := json.Marshal(map[string]any{"request_id": id, "request": request})
	if err != nil {
		return nil, err
	}
	if err = s.runtime.HandleEvent(s.manifest.InstanceID, "package_source."+operation, payload); err != nil {
		return nil, err
	}
	return s.responses(ctx, id, s.runtime.TakeOutbound(s.manifest.InstanceID))
}

func (s *WASMPackageSource) responses(ctx context.Context, requestID string, messages []GuestMessage) ([]packageSourceEnvelope, error) {
	responses := []packageSourceEnvelope{}
	for _, message := range messages {
		if message.Type != "outgoing" {
			continue
		}
		var response packageSourceEnvelope
		if json.Unmarshal(message.Payload, &response) != nil {
			continue
		}
		if response.EventType == "package_source.proposal" {
			if err := s.captureProposal(ctx, response.Result); err != nil {
				return nil, err
			}
			continue
		}
		if response.RequestID != requestID {
			continue
		}
		if response.Error != "" {
			return nil, fmt.Errorf("package source %s: %s", s.ID(), response.Error)
		}
		responses = append(responses, response)
	}
	if len(responses) == 0 {
		return nil, fmt.Errorf("package source %s: missing correlated response", s.ID())
	}
	return responses, nil
}

func (s *WASMPackageSource) ensureProposals(ctx context.Context) error {
	s.proposalMu.Lock()
	defer s.proposalMu.Unlock()
	if s.proposalsLoaded {
		return nil
	}
	data, err := s.runtime.state.Get(ctx, s.manifest.InstanceID, packageSourceProposalStateKey)
	if err == nil {
		if json.Unmarshal(data, &s.proposals) != nil {
			return ErrInvalidManifest
		}
	} else if !errors.Is(err, ErrStateNotFound) {
		return err
	}
	if s.proposals == nil {
		s.proposals = map[string]packagesource.Proposal{}
	}
	s.proposalsLoaded = true
	return nil
}

func (s *WASMPackageSource) captureProposal(ctx context.Context, raw json.RawMessage) error {
	if err := s.ensureProposals(ctx); err != nil {
		return err
	}
	var proposal packagesource.Proposal
	if json.Unmarshal(raw, &proposal) != nil || proposal.ID == "" {
		return ErrInvalidManifest
	}
	proposal.Plan.SourceID = s.ID()
	proposal.Plan.ProposalID = proposal.ID
	for index := range proposal.Plan.Items {
		proposal.Plan.Items[index].Package.SourceID = s.ID()
	}
	if err := proposal.Plan.Validate(); err != nil {
		return err
	}
	s.proposalMu.Lock()
	defer s.proposalMu.Unlock()
	if existing, ok := s.proposals[proposal.ID]; ok {
		old, _ := json.Marshal(existing)
		next, _ := json.Marshal(proposal)
		if string(old) != string(next) {
			return packagesource.ErrConflict
		}
		return nil
	}
	s.proposals[proposal.ID] = proposal
	return s.persistProposals(ctx)
}

func (s *WASMPackageSource) persistProposals(ctx context.Context) error {
	data, err := json.Marshal(s.proposals)
	if err != nil {
		return err
	}
	return s.runtime.state.Put(ctx, s.manifest.InstanceID, packageSourceProposalStateKey, data)
}

func (s *WASMPackageSource) Search(ctx context.Context, request packagesource.SearchRequest) (packagesource.SearchResult, error) {
	responses, err := s.invoke(ctx, "search", request)
	if err != nil {
		return packagesource.SearchResult{}, err
	}
	var result packagesource.SearchResult
	if responses[0].EventType != "package_source.search.result" || json.Unmarshal(responses[0].Result, &result) != nil {
		return result, ErrInvalidManifest
	}
	for index := range result.Packages {
		result.Packages[index].Reference.SourceID = s.ID()
	}
	return result, nil
}

func (s *WASMPackageSource) Plan(ctx context.Context, request packagesource.PlanRequest) (packagesource.InstallationPlan, error) {
	request.Package.SourceID = s.ID()
	responses, err := s.invoke(ctx, "plan", request)
	if err != nil {
		return packagesource.InstallationPlan{}, err
	}
	var plan packagesource.InstallationPlan
	if responses[0].EventType != "package_source.plan.result" || json.Unmarshal(responses[0].Result, &plan) != nil {
		return plan, ErrInvalidManifest
	}
	plan.SourceID = s.ID()
	for index := range plan.Items {
		plan.Items[index].Package.SourceID = s.ID()
	}
	return plan, plan.Validate()
}

func (s *WASMPackageSource) Acquire(ctx context.Context, artifact packagesource.Artifact, destination io.Writer) error {
	responses, err := s.invoke(ctx, "acquire", artifact)
	if err != nil {
		return err
	}
	var written int64
	for _, response := range responses {
		switch response.EventType {
		case "package_source.artifact.chunk":
			data, err := base64.StdEncoding.DecodeString(response.Data)
			if err != nil {
				return ErrInvalidManifest
			}
			if written+int64(len(data)) > artifact.Size {
				return packagesource.ErrBounds
			}
			if _, err = destination.Write(data); err != nil {
				return err
			}
			written += int64(len(data))
		case "package_source.artifact.complete":
		default:
			return ErrInvalidManifest
		}
	}
	if written != artifact.Size || responses[len(responses)-1].EventType != "package_source.artifact.complete" {
		return packagesource.ErrBounds
	}
	return nil
}

func (s *WASMPackageSource) Proposals(ctx context.Context, request packagesource.ProposalRequest) (packagesource.ProposalResult, error) {
	if request.Limit == 0 {
		return packagesource.ProposalResult{}, packagesource.ErrInvalid
	}
	if err := s.ensureProposals(ctx); err != nil {
		return packagesource.ProposalResult{}, err
	}
	s.callMu.Lock()
	if err := s.runtime.Poll(s.manifest.InstanceID); err != nil {
		s.callMu.Unlock()
		return packagesource.ProposalResult{}, err
	}
	messages := s.runtime.TakeOutbound(s.manifest.InstanceID)
	s.callMu.Unlock()
	for _, message := range messages {
		var envelope packageSourceEnvelope
		if message.Type != "outgoing" || json.Unmarshal(message.Payload, &envelope) != nil || envelope.EventType != "package_source.proposal" {
			continue
		}
		if err := s.captureProposal(ctx, envelope.Result); err != nil {
			return packagesource.ProposalResult{}, err
		}
	}
	s.proposalMu.Lock()
	ids := make([]string, 0, len(s.proposals))
	for id := range s.proposals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := packagesource.ProposalResult{Cursor: request.Cursor}
	for _, id := range ids {
		result.Proposals = append(result.Proposals, s.proposals[id])
		if uint32(len(result.Proposals)) == request.Limit {
			break
		}
	}
	s.proposalMu.Unlock()
	return result, nil
}

func (s *WASMPackageSource) AcknowledgeProposals(ctx context.Context, ids []string) error {
	if err := s.ensureProposals(ctx); err != nil {
		return err
	}
	s.proposalMu.Lock()
	for _, id := range ids {
		delete(s.proposals, id)
	}
	if err := s.persistProposals(ctx); err != nil {
		s.proposalMu.Unlock()
		return err
	}
	s.proposalMu.Unlock()
	_, err := s.invoke(ctx, "proposals.ack", map[string]any{"proposal_ids": ids})
	if err != nil {
		return err
	}
	return nil
}

func (s *WASMPackageSource) RequestWorkflow(ctx context.Context, request packagesource.WorkflowRequest) (packagesource.Workflow, error) {
	responses, err := s.invoke(ctx, "workflow.request", request)
	if err != nil {
		return packagesource.Workflow{}, err
	}
	return s.decodeWorkflow(responses, "package_source.workflow.result")
}
func (s *WASMPackageSource) WorkflowStatus(ctx context.Context, id string) (packagesource.Workflow, error) {
	responses, err := s.invoke(ctx, "workflow.status", map[string]string{"workflow_id": id})
	if err != nil {
		return packagesource.Workflow{}, err
	}
	return s.decodeWorkflow(responses, "package_source.workflow.result")
}
func (s *WASMPackageSource) CancelWorkflow(ctx context.Context, id string) (packagesource.Workflow, error) {
	responses, err := s.invoke(ctx, "workflow.cancel", map[string]string{"workflow_id": id})
	if err != nil {
		return packagesource.Workflow{}, err
	}
	return s.decodeWorkflow(responses, "package_source.workflow.result")
}

func (s *WASMPackageSource) decodeWorkflow(responses []packageSourceEnvelope, eventType string) (packagesource.Workflow, error) {
	var workflow packagesource.Workflow
	if len(responses) == 0 || responses[0].EventType != eventType || json.Unmarshal(responses[0].Result, &workflow) != nil || workflow.ID == "" || workflow.UpdatedAt.IsZero() {
		return workflow, ErrInvalidManifest
	}
	if workflow.Plan != nil {
		workflow.Plan.SourceID = s.ID()
		for index := range workflow.Plan.Items {
			workflow.Plan.Items[index].Package.SourceID = s.ID()
		}
		if err := workflow.Plan.Validate(); err != nil {
			return workflow, err
		}
	}
	return workflow, nil
}

func (*WASMPackageSource) Close(context.Context) error { return nil }
