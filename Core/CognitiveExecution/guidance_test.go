package cognitiveexecution

import (
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

func TestSharedInstructionsAdvertiseSubjectiveMemoryOnlyWhenAvailable(t *testing.T) {
	if got := SharedInstructions(nil); len(got) != 0 {
		t.Fatalf("disabled guidance = %q", got)
	}
	if got := SharedInstructions([]inference.Tool{{Name: "runtime.info__read", CapabilityID: "runtime.info"}}); len(got) != 0 {
		t.Fatalf("unrelated guidance = %q", got)
	}
	for _, operation := range []string{"recall", "remember"} {
		got := SharedInstructions([]inference.Tool{{Name: "core.memory__" + operation, CapabilityID: "core.memory", OperationID: operation}})
		if len(got) != 1 || got[0] != MemoryGuidance {
			t.Fatalf("%s guidance = %q", operation, got)
		}
	}
}
