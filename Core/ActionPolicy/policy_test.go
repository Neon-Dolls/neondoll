package actionpolicy_test

import (
	"errors"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
)

func TestUnrestrictedActionDefaultsToAllow(t *testing.T) {
	policy := actionpolicy.Default()
	result := policy.Evaluate(actionpolicy.Request{Doll: "doll", Capability: "runtime.info", Operation: "read"})
	if result.Decision != actionpolicy.DecisionAllow || result.Reason != "unrestricted" {
		t.Fatalf("result = %+v", result)
	}
}

func TestConfiguredDecisions(t *testing.T) {
	policy, err := actionpolicy.New([]actionpolicy.Rule{
		{Capability: "private.data", Operation: "delete", Decision: actionpolicy.DecisionDeny, Reason: "owner_prohibited"},
		{Capability: "camera", Operation: "capture", Decision: actionpolicy.DecisionRequireApproval, Reason: "owner_approval"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := policy.Evaluate(actionpolicy.Request{Capability: "private.data", Operation: "delete"}); result.Decision != actionpolicy.DecisionDeny {
		t.Fatalf("deny result = %+v", result)
	}
	if result := policy.Evaluate(actionpolicy.Request{Capability: "camera", Operation: "capture"}); result.Decision != actionpolicy.DecisionRequireApproval {
		t.Fatalf("approval result = %+v", result)
	}
}

func TestExplicitProhibitionOutranksOverlappingAllow(t *testing.T) {
	policy, err := actionpolicy.New([]actionpolicy.Rule{
		{Doll: "doll", Capability: "camera", Operation: "capture", Decision: actionpolicy.DecisionAllow},
		{Capability: "camera", Operation: "capture", Decision: actionpolicy.DecisionDeny, Reason: "owner_prohibited"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := policy.Evaluate(actionpolicy.Request{Doll: "doll", Capability: "camera", Operation: "capture"})
	if result.Decision != actionpolicy.DecisionDeny || result.Reason != "owner_prohibited" {
		t.Fatalf("result = %+v", result)
	}
}

func TestInvalidPolicyFailsClosedAtConstruction(t *testing.T) {
	for _, rules := range [][]actionpolicy.Rule{
		{{Capability: "camera", Operation: "capture", Decision: "maybe"}},
		{{Operation: "capture", Decision: actionpolicy.DecisionDeny}},
		{
			{Capability: "camera", Operation: "capture", Decision: actionpolicy.DecisionDeny},
			{Capability: "camera", Operation: "capture", Decision: actionpolicy.DecisionAllow},
		},
	} {
		if _, err := actionpolicy.New(rules); !errors.Is(err, actionpolicy.ErrInvalidPolicy) {
			t.Fatalf("rules=%+v error=%v", rules, err)
		}
	}
}
