package inference

import (
	"errors"
	"fmt"
)

var ErrInvalidContextBudget = errors.New("inference: invalid model context budget")

// ModelContextBudget describes the selected model's total context capacity.
// OutputTokens and RequiredOverheadTokens are reserved before model-visible
// input is assembled by Context Builder.
type ModelContextBudget struct {
	ContextTokens          int
	OutputTokens           int
	RequiredOverheadTokens int
}

// InputTokens returns the capacity available to Context Builder.
func (budget ModelContextBudget) InputTokens() (int, error) {
	if budget.ContextTokens <= 0 || budget.OutputTokens <= 0 || budget.RequiredOverheadTokens < 0 {
		return 0, fmt.Errorf("%w: context and output must be positive and overhead non-negative", ErrInvalidContextBudget)
	}
	input := budget.ContextTokens - budget.OutputTokens - budget.RequiredOverheadTokens
	if input <= 0 {
		return 0, fmt.Errorf("%w: reserved output and overhead leave no input capacity", ErrInvalidContextBudget)
	}
	return input, nil
}
