package contextbuilder

// SlidingWindowPolicy keeps required items plus the newest whole-message
// chronological suffix that fits the remaining budget. It is stateless and
// safe to share across concurrent session builds.
type SlidingWindowPolicy struct{}

var _ WindowPolicy = SlidingWindowPolicy{}

func (SlidingWindowPolicy) Select(request WindowRequest) (Window, error) {
	if request.TokenBudget < 0 {
		return Window{}, ErrRequiredContextExceedsBudget
	}

	requiredTokens := 0
	for _, item := range request.Items {
		if item.EstimatedTokens <= 0 {
			return Window{}, ErrInvalidRequest
		}
		if item.Required {
			requiredTokens += item.EstimatedTokens
		}
	}
	if requiredTokens > request.TokenBudget {
		return Window{}, ErrRequiredContextExceedsBudget
	}

	remaining := request.TokenBudget - requiredTokens
	cutoff := 0
	for index := len(request.Items) - 1; index >= 0; index-- {
		item := request.Items[index]
		if item.Required {
			continue
		}
		if item.EstimatedTokens > remaining {
			cutoff = index + 1
			break
		}
		remaining -= item.EstimatedTokens
	}

	window := Window{EstimatedTokens: requiredTokens}
	for index, item := range request.Items {
		if item.Required || index >= cutoff {
			window.Items = append(window.Items, item)
			if !item.Required {
				if window.StartSequence == nil {
					window.StartSequence = sequencePointer(item.Record.Sequence)
				}
				window.EndSequence = sequencePointer(item.Record.Sequence)
				window.EstimatedTokens += item.EstimatedTokens
			}
			continue
		}
		window.OmittedMessages = append(window.OmittedMessages, OmittedMessage{
			ID:              item.Record.ID,
			Sequence:        item.Record.Sequence,
			EstimatedTokens: item.EstimatedTokens,
			Reason:          OmittedBudget,
		})
	}
	return window, nil
}
