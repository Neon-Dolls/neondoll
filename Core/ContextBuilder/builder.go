package contextbuilder

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Neon-Dolls/neondoll/Core/Session"
)

type Service struct {
	sessions  session.Service
	history   HistoryStore
	estimator TokenEstimator
}

var _ Builder = (*Service)(nil)

func New(sessions session.Service, history HistoryStore, estimator TokenEstimator) (*Service, error) {
	if sessions == nil || history == nil {
		return nil, ErrInvalidRequest
	}
	if estimator == nil {
		estimator = ApproximateEstimator{}
	}
	return &Service{sessions: sessions, history: history, estimator: estimator}, nil
}

// ApproximateEstimator is deterministic and provider-independent. It charges
// four framing tokens plus one token per four Unicode code points (rounded up).
// Future inference profiles can replace it with model-specific estimation.
type ApproximateEstimator struct{}

func (ApproximateEstimator) EstimateTokens(message Message) int {
	return 4 + (utf8.RuneCountInString(message.Content)+3)/4
}

func (builder *Service) Build(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(string(request.SessionID)) == "" || request.TokenBudget <= 0 {
		return Result{}, ErrInvalidRequest
	}
	if _, err := builder.sessions.Lookup(ctx, request.SessionID); err != nil {
		return Result{}, err
	}
	history, err := builder.history.ListSessionMessages(ctx, request.SessionID)
	if err != nil {
		return Result{}, err
	}

	result := Result{SessionID: request.SessionID, Metadata: Metadata{TokenBudget: request.TokenBudget, HistoryMessages: len(history)}}
	for _, instruction := range request.SystemInstructions {
		message := Message{Role: RoleSystem, Content: instruction}
		cost := estimate(builder.estimator, message)
		if result.Metadata.EstimatedTokens+cost > request.TokenBudget {
			return Result{}, ErrRequiredContextExceedsBudget
		}
		result.Messages = append(result.Messages, message)
		result.Metadata.EstimatedTokens += cost
	}

	costs := make([]int, len(history))
	required := result.Metadata.EstimatedTokens
	for index, stored := range history {
		if !validRole(stored.Role) {
			return Result{}, ErrInvalidRequest
		}
		costs[index] = estimate(builder.estimator, Message{Role: stored.Role, Content: stored.Content})
		if stored.Role == RoleSystem {
			required += costs[index]
		}
	}
	if required > request.TokenBudget {
		return Result{}, ErrRequiredContextExceedsBudget
	}

	remaining := request.TokenBudget - required
	cutoff := 0
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == RoleSystem {
			continue
		}
		if costs[index] > remaining {
			cutoff = index + 1
			break
		}
		remaining -= costs[index]
	}

	for index, stored := range history {
		included := stored.Role == RoleSystem || index >= cutoff
		if included {
			result.Messages = append(result.Messages, Message{Role: stored.Role, Content: stored.Content})
			result.Metadata.EstimatedTokens += costs[index]
			result.Metadata.IncludedHistoryMessages++
			result.Metadata.IncludedMessageIDs = append(result.Metadata.IncludedMessageIDs, stored.ID)
			continue
		}
		result.Metadata.OmittedHistoryMessages++
		result.Metadata.OmittedMessages = append(result.Metadata.OmittedMessages, OmittedMessage{
			ID: stored.ID, Sequence: stored.Sequence, EstimatedTokens: costs[index], Reason: OmittedBudget,
		})
	}
	return result, nil
}

func estimate(estimator TokenEstimator, message Message) int {
	if value := estimator.EstimateTokens(message); value > 0 {
		return value
	}
	return 1
}

func validRole(role Role) bool {
	return role == RoleSystem || role == RoleUser || role == RoleAssistant || role == RoleTool
}
