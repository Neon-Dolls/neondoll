package dollmind

import (
	"context"
	"fmt"
	"strings"

	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

// CognitiveContextTokenBudget bounds the provider input assembled for an
// internal cognition invocation. Model-profile-specific budgets remain a
// later Core 5 concern.
const CognitiveContextTokenBudget = 8192

type cognitionRunContextKey struct{}

type cognitionRunExecution struct {
	id       string
	resolver *contextbuilder.CognitionRunResolver
	builder  contextbuilder.SourceBuilder
}

func newCognitionRunExecution(id, objective, wakeEvidence string, mind MindAPI) (*cognitionRunExecution, error) {
	resolver, err := contextbuilder.NewCognitionRunResolver(contextbuilder.CognitionRun{
		ID:           id,
		Objective:    objective,
		WakeEvidence: wakeEvidence,
	}, mind, nil)
	if err != nil {
		return nil, fmt.Errorf("create cognition run resolver: %w", err)
	}
	builder, err := contextbuilder.NewWithResolver(resolver, nil, contextbuilder.SlidingWindowPolicy{})
	if err != nil {
		return nil, fmt.Errorf("create cognition context builder: %w", err)
	}
	return &cognitionRunExecution{id: id, resolver: resolver, builder: builder}, nil
}

func withCognitionRun(ctx context.Context, run *cognitionRunExecution) context.Context {
	return context.WithValue(ctx, cognitionRunContextKey{}, run)
}

func cognitionRunFromContext(ctx context.Context) *cognitionRunExecution {
	run, _ := ctx.Value(cognitionRunContextKey{}).(*cognitionRunExecution)
	return run
}

// CognitionRunID returns the ephemeral run identity associated with an active
// internal cognition execution. It is intended for tracing and provider-side
// correlation; an empty string means the call is not part of such a run.
func CognitionRunID(ctx context.Context) string {
	if run := cognitionRunFromContext(ctx); run != nil {
		return run.id
	}
	return ""
}

func (run *cognitionRunExecution) appendWorking(role contextbuilder.Role, content string) error {
	return run.resolver.Append(contextbuilder.CognitionWorkingRecord{Role: role, Content: content})
}

func (run *cognitionRunExecution) messages(ctx context.Context, instruction string) ([]inference.Message, error) {
	result, err := run.builder.BuildSource(ctx, contextbuilder.SourceRequest{
		Source:             run.resolver.Source(),
		TokenBudget:        CognitiveContextTokenBudget,
		SystemInstructions: []string{instruction},
	})
	if err != nil {
		return nil, fmt.Errorf("build cognition context: %w", err)
	}
	messages := make([]inference.Message, 0, len(result.Messages))
	for _, message := range result.Messages {
		// Existing provider adapters receive cognition instructions as one
		// system message. Coalescing preserves that contract while selection
		// and budgeting remain record-aware inside Context Builder.
		if message.Role == contextbuilder.RoleSystem && len(messages) > 0 && messages[len(messages)-1].Role == "system" {
			messages[len(messages)-1].Content += "\n\n" + message.Content
			continue
		}
		messages = append(messages, inference.Message{Role: string(message.Role), Content: message.Content})
	}
	if len(messages) > 0 {
		messages[0].Content = strings.TrimSpace(messages[0].Content)
	}
	return messages, nil
}
