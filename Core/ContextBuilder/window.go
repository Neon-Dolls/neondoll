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
			if item.Record.OmitReason != "" {
				return Window{}, ErrInvalidRequest
			}
			requiredTokens += item.EstimatedTokens
		}
	}
	if requiredTokens > request.TokenBudget {
		return Window{}, ErrRequiredContextExceedsBudget
	}

	remaining := request.TokenBudget - requiredTokens
	selected := make([]bool, len(request.Items))
	groupKeys := make([]string, len(request.Items))
	type selectionGroup struct {
		tokens  int
		indexes []int
	}
	groups := make(map[string]*selectionGroup)
	for index, item := range request.Items {
		if item.Required || item.Record.OmitReason != "" {
			continue
		}
		group := item.Record.WindowGroup
		if group == "" {
			group = "record:" + item.Record.ID
		} else {
			group = "group:" + group
		}
		groupKeys[index] = group
		selection := groups[group]
		if selection == nil {
			selection = &selectionGroup{}
			groups[group] = selection
		}
		selection.tokens += item.EstimatedTokens
		selection.indexes = append(selection.indexes, index)
	}
	seenGroups := make(map[string]struct{})
	for index := len(request.Items) - 1; index >= 0; index-- {
		item := request.Items[index]
		if item.Required || item.Record.OmitReason != "" {
			continue
		}
		group := groupKeys[index]
		if _, ok := seenGroups[group]; ok {
			continue
		}
		seenGroups[group] = struct{}{}
		selection := groups[group]
		if selection.tokens > remaining {
			break
		}
		remaining -= selection.tokens
		for _, selectedIndex := range selection.indexes {
			selected[selectedIndex] = true
		}
	}

	window := Window{EstimatedTokens: requiredTokens}
	for index, item := range request.Items {
		if item.Required || selected[index] {
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
		reason := item.Record.OmitReason
		if reason == "" {
			reason = OmittedBudget
		}
		window.OmittedMessages = append(window.OmittedMessages, OmittedMessage{
			ID:              item.Record.ID,
			Sequence:        item.Record.Sequence,
			EstimatedTokens: item.EstimatedTokens,
			Reason:          reason,
			Provenance:      item.Record.Provenance,
		})
	}
	return window, nil
}
