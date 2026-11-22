package extension

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	manifest Manifest
	runtime  *WASMRuntime
	sequence atomic.Uint64
}

func NewWASMPackageSource(manifest Manifest, runtime *WASMRuntime) *WASMPackageSource {
	return &WASMPackageSource{manifest: manifest, runtime: runtime}
}

func (s *WASMPackageSource) ID() string { return s.manifest.InstanceID }

func (s *WASMPackageSource) invoke(ctx context.Context, operation string, request any) ([]packageSourceEnvelope, error) {
	id := fmt.Sprintf("%s:%d", s.manifest.InstanceID, s.sequence.Add(1))
	payload, err := json.Marshal(map[string]any{"request_id": id, "request": request})
	if err != nil {
		return nil, err
	}
	if err = s.runtime.HandleEvent(s.manifest.InstanceID, "package_source."+operation, payload); err != nil {
		return nil, err
	}
	return s.responses(id, s.runtime.TakeOutbound(s.manifest.InstanceID))
}

func (s *WASMPackageSource) responses(requestID string, messages []GuestMessage) ([]packageSourceEnvelope, error) {
	responses := []packageSourceEnvelope{}
	for _, message := range messages {
		if message.Type != "outgoing" {
			continue
		}
		var response packageSourceEnvelope
		if json.Unmarshal(message.Payload, &response) != nil || response.RequestID != requestID {
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
	if err := s.runtime.Poll(s.manifest.InstanceID); err != nil {
		return packagesource.ProposalResult{}, err
	}
	result := packagesource.ProposalResult{}
	for _, message := range s.runtime.TakeOutbound(s.manifest.InstanceID) {
		var envelope packageSourceEnvelope
		if message.Type != "outgoing" || json.Unmarshal(message.Payload, &envelope) != nil || envelope.EventType != "package_source.proposal" {
			continue
		}
		var proposal packagesource.Proposal
		if json.Unmarshal(envelope.Result, &proposal) != nil {
			return result, ErrInvalidManifest
		}
		proposal.Plan.SourceID = s.ID()
		proposal.Plan.ProposalID = proposal.ID
		for index := range proposal.Plan.Items {
			proposal.Plan.Items[index].Package.SourceID = s.ID()
		}
		if err := proposal.Plan.Validate(); err != nil {
			return result, err
		}
		if proposal.ID > request.Cursor {
			result.Proposals = append(result.Proposals, proposal)
			result.Cursor = proposal.ID
		}
		if uint32(len(result.Proposals)) == request.Limit {
			break
		}
	}
	return result, nil
}

func (*WASMPackageSource) Close(context.Context) error { return nil }
