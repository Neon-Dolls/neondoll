package contextbuilder

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Neon-Dolls/neondoll/Core/Session"
)

type Service struct {
	resolver  SourceResolver
	estimator TokenEstimator
	policy    WindowPolicy
}

var _ Builder = (*Service)(nil)
var _ SourceBuilder = (*Service)(nil)

func New(sessions session.Service, history HistoryStore, estimator TokenEstimator) (*Service, error) {
	return NewWithPolicy(sessions, history, estimator, SlidingWindowPolicy{})
}

// NewWithPolicy constructs a Builder with an explicit history-window policy.
func NewWithPolicy(sessions session.Service, history HistoryStore, estimator TokenEstimator, policy WindowPolicy) (*Service, error) {
	if sessions == nil || history == nil {
		return nil, ErrInvalidRequest
	}
	resolver, err := NewInteractionSessionSourceResolver(sessions, history)
	if err != nil {
		return nil, err
	}
	return NewWithResolver(resolver, estimator, policy)
}

// NewWithResolver constructs the shared Builder over an explicit authorized
// source-resolution boundary.
func NewWithResolver(resolver SourceResolver, estimator TokenEstimator, policy WindowPolicy) (*Service, error) {
	if resolver == nil {
		return nil, ErrInvalidRequest
	}
	if estimator == nil {
		estimator = ApproximateEstimator{}
	}
	if policy == nil {
		return nil, ErrInvalidRequest
	}
	return &Service{resolver: resolver, estimator: estimator, policy: policy}, nil
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
	result, err := builder.BuildSource(ctx, SourceRequest{
		Source:             InteractionSessionSource(request.SessionID),
		TokenBudget:        request.TokenBudget,
		SystemInstructions: request.SystemInstructions,
		RequiredMessages:   request.RequiredMessages,
		TransientMessages:  request.TransientMessages,
	})
	if err != nil {
		return Result{}, err
	}
	result.SessionID = request.SessionID
	return result, nil
}

func (builder *Service) BuildSource(ctx context.Context, request SourceRequest) (Result, error) {
	if !validSource(request.Source) {
		return Result{}, ErrInvalidSource
	}
	if request.TokenBudget <= 0 {
		return Result{}, ErrInvalidRequest
	}
	resolved, err := builder.resolver.Resolve(ctx, request.Source)
	if err != nil {
		return Result{}, err
	}
	if resolved.Source != request.Source {
		return Result{}, ErrSourceIsolation
	}
	for index, record := range resolved.Records {
		if record.Source != request.Source {
			return Result{}, ErrSourceIsolation
		}
		if strings.TrimSpace(record.ID) == "" || record.Sequence < 0 || (index > 0 && record.Sequence <= resolved.Records[index-1].Sequence) {
			return Result{}, ErrInvalidSource
		}
	}

	result := Result{Metadata: Metadata{TokenBudget: request.TokenBudget, HistoryMessages: len(resolved.Records)}}
	if request.Source.Kind == SourceInteractionSession {
		result.SessionID = session.ID(request.Source.ID)
	}
	if len(resolved.Records) > 0 {
		result.Metadata.HistoryStartSequence = sequencePointer(resolved.Records[0].Sequence)
		result.Metadata.HistoryEndSequence = sequencePointer(resolved.Records[len(resolved.Records)-1].Sequence)
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
	requiredTail := make([]Message, len(request.RequiredMessages))
	for index, message := range request.RequiredMessages {
		if !validRole(message.Role) || strings.TrimSpace(message.Content) == "" {
			return Result{}, ErrInvalidRequest
		}
		cost := estimate(builder.estimator, message)
		if result.Metadata.EstimatedTokens+cost > request.TokenBudget {
			return Result{}, ErrRequiredContextExceedsBudget
		}
		requiredTail[index] = message
		result.Metadata.EstimatedTokens += cost
	}

	items := make([]WindowItem, 0, len(resolved.Records)+len(request.TransientMessages))
	historyIDs := make(map[string]struct{}, len(resolved.Records))
	for _, record := range resolved.Records {
		if !validRole(record.Role) {
			return Result{}, ErrInvalidRequest
		}
		historyIDs[record.ID] = struct{}{}
		items = append(items, WindowItem{
			Record:          record,
			EstimatedTokens: estimate(builder.estimator, Message{Role: record.Role, Content: record.Content, Provenance: record.Provenance}),
			Required:        record.Required,
		})
	}
	nextSequence := 0
	if len(resolved.Records) > 0 {
		nextSequence = resolved.Records[len(resolved.Records)-1].Sequence + 1
	}
	seenTransient := make(map[string]struct{}, len(request.TransientMessages))
	for index, transient := range request.TransientMessages {
		if strings.TrimSpace(transient.ID) == "" || !validRole(transient.Message.Role) || strings.TrimSpace(transient.Message.Content) == "" {
			return Result{}, ErrInvalidRequest
		}
		if _, exists := historyIDs[transient.ID]; exists {
			return Result{}, ErrInvalidRequest
		}
		if _, exists := seenTransient[transient.ID]; exists {
			return Result{}, ErrInvalidRequest
		}
		seenTransient[transient.ID] = struct{}{}
		record := ContextRecord{
			ID: transient.ID, Source: request.Source, Sequence: nextSequence + index,
			Role: transient.Message.Role, Content: transient.Message.Content, Provenance: transient.Message.Provenance,
			Required: transient.Required, WindowGroup: transient.WindowGroup,
		}
		items = append(items, WindowItem{
			Record: record, EstimatedTokens: estimate(builder.estimator, transient.Message), Required: transient.Required,
		})
	}
	window, err := builder.policy.Select(WindowRequest{
		Items:       items,
		TokenBudget: request.TokenBudget - result.Metadata.EstimatedTokens,
	})
	if err != nil {
		return Result{}, err
	}
	result.Metadata.EstimatedTokens += window.EstimatedTokens
	result.Metadata.OmittedMessages = window.OmittedMessages
	for _, omitted := range window.OmittedMessages {
		if _, ok := historyIDs[omitted.ID]; ok {
			result.Metadata.OmittedHistoryMessages++
		}
	}
	var transientTail []Message
	for _, item := range window.Items {
		message := Message{Role: item.Record.Role, Content: item.Record.Content, Provenance: item.Record.Provenance}
		if _, ok := historyIDs[item.Record.ID]; !ok {
			transientTail = append(transientTail, message)
			continue
		}
		result.Messages = append(result.Messages, message)
		result.Metadata.IncludedHistoryMessages++
		result.Metadata.IncludedMessageIDs = append(result.Metadata.IncludedMessageIDs, item.Record.ID)
		if !item.Required {
			if result.Metadata.WindowStartSequence == nil {
				result.Metadata.WindowStartSequence = sequencePointer(item.Record.Sequence)
			}
			result.Metadata.WindowEndSequence = sequencePointer(item.Record.Sequence)
		}
	}
	result.Messages = append(result.Messages, requiredTail...)
	result.Messages = append(result.Messages, transientTail...)
	return result, nil
}

func validSource(source ContextSource) bool {
	if strings.TrimSpace(source.ID) == "" {
		return false
	}
	return source.Kind == SourceInteractionSession || source.Kind == SourceCognitionRun
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
