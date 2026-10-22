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
	policy    WindowPolicy
}

var _ Builder = (*Service)(nil)

func New(sessions session.Service, history HistoryStore, estimator TokenEstimator) (*Service, error) {
	return NewWithPolicy(sessions, history, estimator, SlidingWindowPolicy{})
}

// NewWithPolicy constructs a Builder with an explicit history-window policy.
func NewWithPolicy(sessions session.Service, history HistoryStore, estimator TokenEstimator, policy WindowPolicy) (*Service, error) {
	if sessions == nil || history == nil {
		return nil, ErrInvalidRequest
	}
	if estimator == nil {
		estimator = ApproximateEstimator{}
	}
	if policy == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{sessions: sessions, history: history, estimator: estimator, policy: policy}, nil
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
	if len(history) > 0 {
		result.Metadata.HistoryStartSequence = sequencePointer(history[0].Sequence)
		result.Metadata.HistoryEndSequence = sequencePointer(history[len(history)-1].Sequence)
	}
	for _, instruction := range request.SystemInstructions {
		message := Message{Role: RoleSystem, Content: instruction}
		cost := estimate(builder.estimator, message)
		if result.Metadata.EstimatedTokens+cost > request.TokenBudget {
			return Result{}, ErrRequiredContextExceedsBudget
		}
		result.Messages = append(result.Messages, message)
		result.Metadata.EstimatedTokens += cost
	}

	items := make([]WindowItem, len(history))
	for index, stored := range history {
		if !validRole(stored.Role) {
			return Result{}, ErrInvalidRequest
		}
		items[index] = WindowItem{
			Message:         stored,
			EstimatedTokens: estimate(builder.estimator, Message{Role: stored.Role, Content: stored.Content}),
			Required:        stored.Role == RoleSystem,
		}
	}
	window, err := builder.policy.Select(WindowRequest{
		Items:       items,
		TokenBudget: request.TokenBudget - result.Metadata.EstimatedTokens,
	})
	if err != nil {
		return Result{}, err
	}
	result.Metadata.EstimatedTokens += window.EstimatedTokens
	result.Metadata.WindowStartSequence = window.StartSequence
	result.Metadata.WindowEndSequence = window.EndSequence
	result.Metadata.IncludedHistoryMessages = len(window.Items)
	result.Metadata.OmittedHistoryMessages = len(window.OmittedMessages)
	result.Metadata.OmittedMessages = window.OmittedMessages
	for _, item := range window.Items {
		result.Messages = append(result.Messages, Message{Role: item.Message.Role, Content: item.Message.Content})
		result.Metadata.IncludedMessageIDs = append(result.Metadata.IncludedMessageIDs, item.Message.ID)
	}
	return result, nil
}

func sequencePointer(sequence int) *int {
	value := sequence
	return &value
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
