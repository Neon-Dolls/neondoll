package inference

import (
	"errors"
	"fmt"
)

var ErrInvalidContextBudget = errors.New("inference: invalid model context budget")

const (
	DefaultContextTokens          = 32768
	DefaultOutputTokens           = 1024
	DefaultRequiredOverheadTokens = 256
)

// ModelContextBudget describes the selected model's total context capacity.
// OutputTokens and RequiredOverheadTokens are reserved before model-visible
// input is assembled by Context Builder.
type ModelContextBudget struct {
	ContextTokens          int `json:"context_tokens"`
	OutputTokens           int `json:"output_tokens"`
	RequiredOverheadTokens int `json:"required_overhead_tokens"`
}

// DefaultModelContextBudget is Core's configured-consumption default. It is a
// limit chosen by Core and must not be expanded from provider-advertised model
// capabilities.
func DefaultModelContextBudget() ModelContextBudget {
	return ModelContextBudget{
		ContextTokens:          DefaultContextTokens,
		OutputTokens:           DefaultOutputTokens,
		RequiredOverheadTokens: DefaultRequiredOverheadTokens,
	}
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
