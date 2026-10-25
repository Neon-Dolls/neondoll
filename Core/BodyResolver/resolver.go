// Package bodyresolver deterministically selects eligible Body execution
// candidates. Resolution is discovery and policy filtering, not authority or
// execution: every selected candidate must still pass through Body.Guard.
package bodyresolver

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Body"
)

var (
	ErrInvalidRequest       = errors.New("invalid body resolution request")
	ErrNoEligibleCandidates = errors.New("no eligible body candidate")
)

// ActionRequirement identifies the operation and execution-path material that
// must be present on a candidate Body.
type ActionRequirement struct {
	Capability                    string
	Operation                     string
	RequiredResources             map[string]string
	RequiredData                  map[string]string
	RequiredCapabilityConstraints map[string]string
}

// TaskConstraints are explicit, caller-owned restrictions for one task.
// Empty fields impose no restriction.
type TaskConstraints struct {
	AllowedBodyIDs     []body.BodyID
	ExcludedBodyIDs    []body.BodyID
	AllowedKinds       []body.BodyKind
	RequiredAttributes map[string]string
}

// OwnerPolicy contains selection-level owner restrictions. It does not grant
// execution authority; Body.Guard remains the authorization boundary.
type OwnerPolicy struct {
	AllowedBodyIDs []body.BodyID
	DeniedBodyIDs  map[body.BodyID]string
}

// Candidate describes one concrete Body execution path. Resources, Data, and
// Attributes are trusted Core discovery facts, not Body authority claims.
type Candidate struct {
	Body        body.Body
	Resources   map[string]string
	Data        map[string]string
	Attributes  map[string]string
	Unavailable bool
	Preference  int
}

// RejectionCode is a stable machine-readable filtering outcome.
type RejectionCode string

const (
	RejectedUnsupportedCapability RejectionCode = "unsupported_capability"
	RejectedUnsupportedOperation  RejectionCode = "unsupported_operation"
	RejectedUnavailable           RejectionCode = "unavailable"
	RejectedCapabilityConstraint  RejectionCode = "capability_constraint"
	RejectedMissingResource       RejectionCode = "missing_resource"
	RejectedMissingData           RejectionCode = "missing_data"
	RejectedTaskConstraint        RejectionCode = "task_constraint"
	RejectedOwnerPolicy           RejectionCode = "owner_policy"
)

type Rejection struct {
	Code   RejectionCode
	Detail string
}

// ExecutionCandidate is an eligible path. Preference is exposed for
// observability but has no effect on eligibility.
type ExecutionCandidate struct {
	Body       body.Body
	Capability body.Capability
	Preference int
}

type RejectedCandidate struct {
	BodyID  body.BodyID
	Reasons []Rejection
}

type Result struct {
	Requirement ActionRequirement
	Eligible    []ExecutionCandidate
	Rejected    []RejectedCandidate
}

// Resolve filters every candidate, then orders eligible candidates by
// preference (higher first) and stable Body ID. It returns the complete result
// with ErrNoEligibleCandidates when filtering leaves no execution path.
func Resolve(requirement ActionRequirement, constraints TaskConstraints, policy OwnerPolicy, candidates []Candidate) (Result, error) {
	if strings.TrimSpace(requirement.Capability) == "" || strings.TrimSpace(requirement.Operation) == "" {
		return Result{}, ErrInvalidRequest
	}

	allowedTaskBodies := idSet(constraints.AllowedBodyIDs)
	excludedTaskBodies := idSet(constraints.ExcludedBodyIDs)
	allowedKinds := kindSet(constraints.AllowedKinds)
	allowedPolicyBodies := idSet(policy.AllowedBodyIDs)
	seen := make(map[body.BodyID]struct{}, len(candidates))
	result := Result{Requirement: requirement}

	for _, candidate := range candidates {
		if candidate.Body == nil || candidate.Body.ID() == "" {
			return Result{}, ErrInvalidRequest
		}
		bodyID := candidate.Body.ID()
		if _, exists := seen[bodyID]; exists {
			return Result{}, fmt.Errorf("%w: duplicate candidate %q", ErrInvalidRequest, bodyID)
		}
		seen[bodyID] = struct{}{}

		capability, reasons := evaluateCapability(candidate, requirement)
		if candidate.Unavailable {
			reasons = append(reasons, Rejection{Code: RejectedUnavailable, Detail: "candidate execution path is unavailable"})
		}
		reasons = append(reasons, matchValues(RejectedMissingResource, "resource", requirement.RequiredResources, candidate.Resources)...)
		reasons = append(reasons, matchValues(RejectedMissingData, "data", requirement.RequiredData, candidate.Data)...)

		if len(allowedTaskBodies) > 0 && !containsID(allowedTaskBodies, bodyID) {
			reasons = append(reasons, Rejection{Code: RejectedTaskConstraint, Detail: "Body is not in the task allowlist"})
		}
		if containsID(excludedTaskBodies, bodyID) {
			reasons = append(reasons, Rejection{Code: RejectedTaskConstraint, Detail: "Body is excluded by task constraints"})
		}
		if len(allowedKinds) > 0 {
			if _, ok := allowedKinds[candidate.Body.Kind()]; !ok {
				reasons = append(reasons, Rejection{Code: RejectedTaskConstraint, Detail: "Body kind is not allowed"})
			}
		}
		reasons = append(reasons, matchValues(RejectedTaskConstraint, "attribute", constraints.RequiredAttributes, candidate.Attributes)...)

		if len(allowedPolicyBodies) > 0 && !containsID(allowedPolicyBodies, bodyID) {
			reasons = append(reasons, Rejection{Code: RejectedOwnerPolicy, Detail: "Body is not allowed by owner policy"})
		}
		if detail, denied := policy.DeniedBodyIDs[bodyID]; denied {
			if strings.TrimSpace(detail) == "" {
				detail = "Body is denied by owner policy"
			}
			reasons = append(reasons, Rejection{Code: RejectedOwnerPolicy, Detail: detail})
		}

		if len(reasons) == 0 {
			result.Eligible = append(result.Eligible, ExecutionCandidate{Body: candidate.Body, Capability: capability, Preference: candidate.Preference})
		} else {
			result.Rejected = append(result.Rejected, RejectedCandidate{BodyID: bodyID, Reasons: reasons})
		}
	}

	sort.Slice(result.Eligible, func(i, j int) bool {
		if result.Eligible[i].Preference != result.Eligible[j].Preference {
			return result.Eligible[i].Preference > result.Eligible[j].Preference
		}
		return result.Eligible[i].Body.ID() < result.Eligible[j].Body.ID()
	})
	sort.Slice(result.Rejected, func(i, j int) bool { return result.Rejected[i].BodyID < result.Rejected[j].BodyID })
	if len(result.Eligible) == 0 {
		return result, ErrNoEligibleCandidates
	}
	return result, nil
}

func evaluateCapability(candidate Candidate, requirement ActionRequirement) (body.Capability, []Rejection) {
	err := candidate.Body.ResolveCapability(requirement.Capability, requirement.Operation)
	if err != nil {
		switch {
		case errors.Is(err, body.ErrUnsupportedCapability):
			return body.Capability{}, []Rejection{{Code: RejectedUnsupportedCapability, Detail: err.Error()}}
		case errors.Is(err, body.ErrUnsupportedOperation):
			return body.Capability{}, []Rejection{{Code: RejectedUnsupportedOperation, Detail: err.Error()}}
		case errors.Is(err, body.ErrUnavailable):
			return body.Capability{}, []Rejection{{Code: RejectedUnavailable, Detail: err.Error()}}
		default:
			return body.Capability{}, []Rejection{{Code: RejectedUnavailable, Detail: "capability resolution failed"}}
		}
	}
	for _, capability := range candidate.Body.Describe() {
		if capability.ID != requirement.Capability {
			continue
		}
		return capability, matchValues(RejectedCapabilityConstraint, "capability constraint", requirement.RequiredCapabilityConstraints, capability.Constraints)
	}
	return body.Capability{}, []Rejection{{Code: RejectedUnsupportedCapability, Detail: "resolved capability is not described"}}
}

func matchValues(code RejectionCode, label string, required, available map[string]string) []Rejection {
	keys := make([]string, 0, len(required))
	for key := range required {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var reasons []Rejection
	for _, key := range keys {
		if value, ok := available[key]; !ok || value != required[key] {
			reasons = append(reasons, Rejection{Code: code, Detail: fmt.Sprintf("required %s %q=%q is unavailable", label, key, required[key])})
		}
	}
	return reasons
}

func idSet(values []body.BodyID) map[body.BodyID]struct{} {
	result := make(map[body.BodyID]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func kindSet(values []body.BodyKind) map[body.BodyKind]struct{} {
	result := make(map[body.BodyKind]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func containsID(values map[body.BodyID]struct{}, value body.BodyID) bool {
	_, ok := values[value]
	return ok
}
