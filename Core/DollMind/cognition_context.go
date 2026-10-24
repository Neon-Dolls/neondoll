package dollmind

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

// CognitiveContextTokenBudget bounds the provider input assembled for an
// internal cognition invocation. Cognition does not yet have production model
// selection/configuration wiring; M2.11 therefore does not pretend this value
// was resolved from a selected model.
const CognitiveContextTokenBudget = 8192

type cognitionRunContextKey struct{}

type cognitionRunExecution struct {
	id       string
	resolver *contextbuilder.CognitionRunResolver
	builder  contextbuilder.SourceBuilder

	mu      sync.Mutex
	working []contextbuilder.CognitionWorkingRecord
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

// ensureCognitionRun creates the one ephemeral run owned by an execution
// boundary, or reuses the run already carried by a nested cognition call.
func (s *Scheduler) ensureCognitionRun(ctx context.Context, objective, evidence string) (context.Context, *cognitionRunExecution, error) {
	if run := cognitionRunFromContext(ctx); run != nil {
		return ctx, run, nil
	}
	run, err := newCognitionRunExecution(uuid.NewString(), objective, evidence, s.mindAPI)
	if err != nil {
		return ctx, nil, err
	}
	return withCognitionRun(ctx, run), run, nil
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
	return run.appendRecord(contextbuilder.CognitionWorkingRecord{Role: role, Content: content})
}

func (run *cognitionRunExecution) appendRecord(record contextbuilder.CognitionWorkingRecord) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.resolver.Append(record); err != nil {
		return err
	}
	run.working = append(run.working, record)
	return nil
}

func (run *cognitionRunExecution) snapshotWorking() []contextbuilder.CognitionWorkingRecord {
	run.mu.Lock()
	defer run.mu.Unlock()
	return append([]contextbuilder.CognitionWorkingRecord(nil), run.working...)
}

func (run *cognitionRunExecution) replaceWorking(records []contextbuilder.CognitionWorkingRecord) error {
	run.mu.Lock()
	defer run.mu.Unlock()
	if err := run.resolver.ReplaceWorking(records); err != nil {
		return err
	}
	run.working = append(run.working[:0], records...)
	return nil
}

func (run *cognitionRunExecution) messages(ctx context.Context, instructions ...string) ([]inference.Message, error) {
	result, err := run.builder.BuildSource(ctx, contextbuilder.SourceRequest{
		Source:             run.resolver.Source(),
		TokenBudget:        CognitiveContextTokenBudget,
		SystemInstructions: instructions,
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
		messages = append(messages, inference.Message{Role: string(message.Role), Content: message.Content, Provenance: message.Provenance})
	}
	if len(messages) > 0 {
		messages[0].Content = strings.TrimSpace(messages[0].Content)
	}
	return messages, nil
}
