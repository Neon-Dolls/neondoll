package bodyresolver_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/BodyResolver"
)

type candidateBody struct {
	id           body.BodyID
	kind         body.BodyKind
	capabilities []body.Capability
}

func (b *candidateBody) ID() body.BodyID     { return b.id }
func (b *candidateBody) Kind() body.BodyKind { return b.kind }
func (b *candidateBody) Name() string        { return string(b.id) }
func (b *candidateBody) Describe() []body.Capability {
	return append([]body.Capability(nil), b.capabilities...)
}
func (b *candidateBody) RegisterCapability(capability body.Capability) error {
	b.capabilities = append(b.capabilities, capability)
	return nil
}
func (b *candidateBody) Execute(body.ExecutionRequest) (*body.ExecutionResult, error) {
	panic("resolver must not execute candidates")
}
func (b *candidateBody) ResolveCapability(capabilityID, operation string) error {
	for _, capability := range b.capabilities {
		if capability.ID != capabilityID {
			continue
		}
		for _, candidateOperation := range capability.Operations {
			if candidateOperation == operation {
				if !capability.Available {
					return body.ErrUnavailable
				}
				return nil
			}
		}
		return body.ErrUnsupportedOperation
	}
	return body.ErrUnsupportedCapability
}

func TestResolveFiltersCapabilityResourcesDataAvailabilityAndConstraints(t *testing.T) {
	requirement := bodyresolver.ActionRequirement{
		Capability: "camera.capture", Operation: "read",
		RequiredResources:             map[string]string{"camera": "front"},
		RequiredData:                  map[string]string{"album": "owner"},
		RequiredCapabilityConstraints: map[string]string{"format": "jpeg"},
	}
	constraints := bodyresolver.TaskConstraints{RequiredAttributes: map[string]string{"location": "home"}}
	valid := newBody("body-valid", true, "jpeg")
	wrongCapability := newBody("body-unsupported", true, "jpeg")
	wrongCapability.capabilities[0].ID = "audio.capture"
	unavailable := newBody("body-offline", false, "jpeg")
	missingMaterial := newBody("body-missing", true, "png")

	result, err := bodyresolver.Resolve(requirement, constraints, bodyresolver.OwnerPolicy{}, []bodyresolver.Candidate{
		{Body: wrongCapability, Resources: values("camera", "front"), Data: values("album", "owner"), Attributes: values("location", "home")},
		{Body: unavailable, Resources: values("camera", "front"), Data: values("album", "owner"), Attributes: values("location", "home")},
		{Body: missingMaterial, Attributes: values("location", "away")},
		{Body: valid, Resources: values("camera", "front"), Data: values("album", "owner"), Attributes: values("location", "home")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := eligibleIDs(result); !reflect.DeepEqual(got, []body.BodyID{"body-valid"}) {
		t.Fatalf("eligible = %v", got)
	}
	assertRejectedWith(t, result, "body-unsupported", bodyresolver.RejectedUnsupportedCapability)
	assertRejectedWith(t, result, "body-offline", bodyresolver.RejectedUnavailable)
	assertRejectedWith(t, result, "body-missing", bodyresolver.RejectedCapabilityConstraint)
	assertRejectedWith(t, result, "body-missing", bodyresolver.RejectedMissingResource)
	assertRejectedWith(t, result, "body-missing", bodyresolver.RejectedMissingData)
	assertRejectedWith(t, result, "body-missing", bodyresolver.RejectedTaskConstraint)
}

func TestResolveOrdersDeterministicallyAfterEligibility(t *testing.T) {
	requirement := bodyresolver.ActionRequirement{Capability: "runtime.info", Operation: "read"}
	candidates := []bodyresolver.Candidate{
		{Body: newRuntimeBody("body-c"), Preference: 5},
		{Body: newRuntimeBody("body-b"), Preference: 10},
		{Body: newRuntimeBody("body-a"), Preference: 10},
		{Body: newBody("body-invalid-preferred", false, "jpeg"), Preference: 1000},
	}
	result, err := bodyresolver.Resolve(requirement, bodyresolver.TaskConstraints{}, bodyresolver.OwnerPolicy{}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := eligibleIDs(result), []body.BodyID{"body-a", "body-b", "body-c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("eligible order = %v, want %v", got, want)
	}
	assertRejectedWith(t, result, "body-invalid-preferred", bodyresolver.RejectedUnsupportedCapability)

	result, err = bodyresolver.Resolve(requirement, bodyresolver.TaskConstraints{}, bodyresolver.OwnerPolicy{}, []bodyresolver.Candidate{candidates[2], candidates[0], candidates[1]})
	if err != nil || !reflect.DeepEqual(eligibleIDs(result), []body.BodyID{"body-a", "body-b", "body-c"}) {
		t.Fatalf("permuted result = %v err=%v", eligibleIDs(result), err)
	}
}

func TestResolveAppliesExplicitConstraintsAndOwnerPolicy(t *testing.T) {
	requirement := bodyresolver.ActionRequirement{Capability: "runtime.info", Operation: "read"}
	result, err := bodyresolver.Resolve(requirement,
		bodyresolver.TaskConstraints{AllowedKinds: []body.BodyKind{body.BodyKindRemote}, ExcludedBodyIDs: []body.BodyID{"body-task-denied"}},
		bodyresolver.OwnerPolicy{DeniedBodyIDs: map[body.BodyID]string{"body-owner-denied": "owner disabled bedroom devices"}},
		[]bodyresolver.Candidate{
			{Body: newRuntimeBody("body-allowed")},
			{Body: newRuntimeBody("body-task-denied")},
			{Body: newRuntimeBody("body-owner-denied")},
			{Body: &candidateBody{id: "body-local-kind", kind: body.BodyKindLocal, capabilities: newRuntimeBody("ignored").capabilities}},
		})
	if err != nil {
		t.Fatal(err)
	}
	if got := eligibleIDs(result); !reflect.DeepEqual(got, []body.BodyID{"body-allowed"}) {
		t.Fatalf("eligible = %v", got)
	}
	assertRejectedWith(t, result, "body-task-denied", bodyresolver.RejectedTaskConstraint)
	assertRejectedWith(t, result, "body-owner-denied", bodyresolver.RejectedOwnerPolicy)
	assertRejectedWith(t, result, "body-local-kind", bodyresolver.RejectedTaskConstraint)
}

func TestResolveFailsClosedWithRejectionEvidence(t *testing.T) {
	result, err := bodyresolver.Resolve(
		bodyresolver.ActionRequirement{Capability: "runtime.info", Operation: "read"},
		bodyresolver.TaskConstraints{},
		bodyresolver.OwnerPolicy{DeniedBodyIDs: map[body.BodyID]string{"body-only": "owner policy"}},
		[]bodyresolver.Candidate{{Body: newRuntimeBody("body-only"), Preference: 99}},
	)
	if !errors.Is(err, bodyresolver.ErrNoEligibleCandidates) {
		t.Fatalf("error = %v", err)
	}
	if len(result.Eligible) != 0 || len(result.Rejected) != 1 {
		t.Fatalf("result = %+v", result)
	}
	assertRejectedWith(t, result, "body-only", bodyresolver.RejectedOwnerPolicy)
}

func newBody(id body.BodyID, available bool, format string) *candidateBody {
	return &candidateBody{id: id, kind: body.BodyKindRemote, capabilities: []body.Capability{{
		ID: "camera.capture", Operations: []string{"read"}, Available: available,
		Constraints: map[string]string{"format": format},
	}}}
}

func newRuntimeBody(id body.BodyID) *candidateBody {
	return &candidateBody{id: id, kind: body.BodyKindRemote, capabilities: []body.Capability{{
		ID: "runtime.info", Operations: []string{"read"}, Available: true,
	}}}
}

func values(key, value string) map[string]string { return map[string]string{key: value} }

func eligibleIDs(result bodyresolver.Result) []body.BodyID {
	ids := make([]body.BodyID, len(result.Eligible))
	for index, candidate := range result.Eligible {
		ids[index] = candidate.Body.ID()
	}
	return ids
}

func assertRejectedWith(t *testing.T, result bodyresolver.Result, bodyID body.BodyID, code bodyresolver.RejectionCode) {
	t.Helper()
	for _, rejected := range result.Rejected {
		if rejected.BodyID != bodyID {
			continue
		}
		for _, reason := range rejected.Reasons {
			if reason.Code == code {
				return
			}
		}
		t.Fatalf("Body %q reasons = %+v, missing %q", bodyID, rejected.Reasons, code)
	}
	t.Fatalf("Body %q was not rejected", bodyID)
}
