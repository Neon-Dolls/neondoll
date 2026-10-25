package dollmind

import (
	"context"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	contextbuilder "github.com/Neon-Dolls/neondoll/Core/ContextBuilder"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

// executeCognition adapts one ephemeral Cognitive Run to Core's shared
// multi-step execution engine. The selected task model remains fixed, while
// messages are reconstructed from the run before every provider invocation.
func (s *Scheduler) executeCognition(ctx context.Context, run *cognitionRunExecution, selected taskModel, purpose inference.Purpose, temperature float64, instructions ...string) (*inference.Response, error) {
	baseWorking := run.snapshotWorking()
	baseExchanges := run.snapshotToolExchanges()
	var toolExecutor cognitiveexecution.ToolExecutor
	if s.toolExecutor != nil {
		toolExecutor = s.toolExecutor
	}
	result, err := cognitiveexecution.Execute(ctx, cognitiveexecution.Request{
		Provider:           selected.provider,
		ModelConfiguration: selected.configuration,
		Purpose:            purpose,
		Temperature:        temperature,
		ToolExecutor:       toolExecutor,
		MaxToolCalls:       ToolCallLimit,
		BuildContext: func(ctx context.Context, state cognitiveexecution.ContextState) ([]inference.Message, error) {
			working := append([]contextbuilder.CognitionWorkingRecord(nil), baseWorking...)
			for _, exchange := range state.Exchanges {
				record, err := cognitionToolRecord(exchange)
				if err != nil {
					return nil, fmt.Errorf("tool context record: %w", err)
				}
				working = append(working, record)
			}
			if err := run.replaceWorking(working); err != nil {
				return nil, fmt.Errorf("context working state: %w", err)
			}
			iterationInstructions := append([]string(nil), instructions...)
			if len(state.Tools) > 0 {
				iterationInstructions = append(iterationInstructions, inference.FormatToolsForPrompt(state.Tools))
			}
			messages, err := run.messages(ctx, state.InputTokenBudget, iterationInstructions...)
			if err != nil {
				return nil, fmt.Errorf("bounded context: %w", err)
			}
			allExchanges := append(append([]cognitiveexecution.ToolExchange(nil), baseExchanges...), state.Exchanges...)
			if err := cognitiveexecution.BindToolContext(messages, state.Tools, allExchanges, "cognition_run.tool_exchange"); err != nil {
				return nil, fmt.Errorf("tool protocol context: %w", err)
			}
			return messages, nil
		},
	})
	if err != nil {
		return nil, err
	}
	if len(result.Exchanges) > 0 {
		working := append([]contextbuilder.CognitionWorkingRecord(nil), baseWorking...)
		for _, exchange := range result.Exchanges {
			record, recordErr := cognitionToolRecord(exchange)
			if recordErr != nil {
				return nil, fmt.Errorf("final tool context record: %w", recordErr)
			}
			working = append(working, record)
		}
		if err := run.replaceWorking(working); err != nil {
			return nil, fmt.Errorf("final context working state: %w", err)
		}
		run.appendToolExchanges(result.Exchanges)
	}
	return result.Response, nil
}

func cognitionToolRecord(exchange cognitiveexecution.ToolExchange) (contextbuilder.CognitionWorkingRecord, error) {
	content, err := cognitiveexecution.FormatToolExchange(exchange)
	if err != nil {
		return contextbuilder.CognitionWorkingRecord{}, err
	}
	return contextbuilder.CognitionWorkingRecord{
		Role:       contextbuilder.RoleTool,
		Content:    content,
		Provenance: "cognition_run.tool_exchange",
		Required:   exchange.Required,
	}, nil
}
