package contextbuilder_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
)

func TestSlidingWindowPolicyPreservesRequiredItemsAndOrdering(t *testing.T) {
	items := []contextbuilder.WindowItem{
		windowItem("old", 0, 3, false),
		windowItem("system", 1, 2, true),
		windowItem("middle", 2, 3, false),
		windowItem("new", 3, 3, false),
	}
	window, err := (contextbuilder.SlidingWindowPolicy{}).Select(contextbuilder.WindowRequest{Items: items, TokenBudget: 8})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(window.Items))
	for index, item := range window.Items {
		got[index] = item.Record.ID
	}
	if !reflect.DeepEqual(got, []string{"system", "middle", "new"}) {
		t.Fatalf("selected IDs = %v", got)
	}
	if window.EstimatedTokens != 8 || *window.StartSequence != 2 || *window.EndSequence != 3 {
		t.Fatalf("window = %+v", window)
	}
	if len(window.OmittedMessages) != 1 || window.OmittedMessages[0].ID != "old" || window.OmittedMessages[0].Reason != contextbuilder.OmittedBudget {
		t.Fatalf("omissions = %+v", window.OmittedMessages)
	}
}

func TestSlidingWindowPolicyRejectsRequiredContextOverBudget(t *testing.T) {
	_, err := (contextbuilder.SlidingWindowPolicy{}).Select(contextbuilder.WindowRequest{
		Items:       []contextbuilder.WindowItem{windowItem("system", 0, 4, true)},
		TokenBudget: 3,
	})
	if !errors.Is(err, contextbuilder.ErrRequiredContextExceedsBudget) {
		t.Fatalf("Select error = %v", err)
	}
}

func windowItem(id string, sequence, tokens int, required bool) contextbuilder.WindowItem {
	return contextbuilder.WindowItem{
		Record: contextbuilder.ContextRecord{
			ID:       id,
			Sequence: sequence,
			Role:     contextbuilder.RoleUser,
			Content:  id,
		},
		EstimatedTokens: tokens,
		Required:        required,
	}
}
