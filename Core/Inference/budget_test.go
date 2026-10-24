package inference

import (
	"errors"
	"testing"
)

func TestModelContextBudgetInputTokens(t *testing.T) {
	tests := []struct {
		name   string
		budget ModelContextBudget
		want   int
		valid  bool
	}{
		{name: "small model", budget: ModelContextBudget{ContextTokens: 4096, OutputTokens: 512, RequiredOverheadTokens: 128}, want: 3456, valid: true},
		{name: "large model", budget: ModelContextBudget{ContextTokens: 32768, OutputTokens: 2048, RequiredOverheadTokens: 256}, want: 30464, valid: true},
		{name: "missing configuration", budget: ModelContextBudget{}, valid: false},
		{name: "insufficient capacity", budget: ModelContextBudget{ContextTokens: 1024, OutputTokens: 1024}, valid: false},
		{name: "negative overhead", budget: ModelContextBudget{ContextTokens: 4096, OutputTokens: 512, RequiredOverheadTokens: -1}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.budget.InputTokens()
			if test.valid && (err != nil || got != test.want) {
				t.Fatalf("InputTokens() = %d, %v; want %d", got, err, test.want)
			}
			if !test.valid && !errors.Is(err, ErrInvalidContextBudget) {
				t.Fatalf("InputTokens() error = %v, want ErrInvalidContextBudget", err)
			}
		})
	}
}

func TestDefaultModelContextBudget(t *testing.T) {
	budget := DefaultModelContextBudget()
	if budget.ContextTokens != 32768 {
		t.Fatalf("default context tokens = %d, want 32768", budget.ContextTokens)
	}
	input, err := budget.InputTokens()
	if err != nil || input != 31488 {
		t.Fatalf("default input tokens = %d, %v; want 31488", input, err)
	}
}
