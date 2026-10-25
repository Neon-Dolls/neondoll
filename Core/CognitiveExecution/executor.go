// Package cognitiveexecution owns Core's bounded multi-step inference loop.
// Context sources remain responsible for rebuilding their model-visible
// messages; the executor guarantees that rebuild occurs before every provider
// invocation and that one selected model configuration remains pinned for the
// complete execution.
package cognitiveexecution

import (
	"context"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
)

const DefaultToolCallLimit = 10

// ToolExchange is one correlated provider call and Core execution result.
// Required is true until the exchange has been presented to a subsequent
// model invocation; context policies may then treat it as selectable history.
type ToolExchange struct {
	Call     inference.ToolCall
	Result   inference.ToolResult
	Required bool
}

// ContextState is the immutable input supplied for one fresh context build.
type ContextState struct {
	InputTokenBudget int
	Tools            []inference.Tool
	Exchanges        []ToolExchange
}

// ContextBuilder reconstructs bounded provider messages for one iteration.
type ContextBuilder func(context.Context, ContextState) ([]inference.Message, error)

// ToolExecutor is the existing authorized Body execution boundary consumed by
// the shared inference loop.
type ToolExecutor interface {
	Tools() []inference.Tool
	ExecuteCall(context.Context, inference.ToolCall, *inference.Tool) inference.ToolResult
}

// Request fixes all execution-wide choices before the first provider call.
type Request struct {
	Provider           inference.Provider
	ModelConfiguration config.ModelConfiguration
	Purpose            inference.Purpose
	Temperature        float64
	BuildContext       ContextBuilder
	ToolExecutor       ToolExecutor
	MaxToolCalls       int
}

// Execute runs one or more inference iterations until the provider returns a
// final response. It never re-resolves the provider, model, or budget.
func Execute(ctx context.Context, request Request) (*inference.Response, error) {
	if request.Provider == nil || request.BuildContext == nil {
		return nil, fmt.Errorf("cognitive execution: provider and context builder are required")
	}
	if err := config.ValidateModelConfiguration(request.ModelConfiguration); err != nil {
		return nil, fmt.Errorf("cognitive execution: model configuration: %w", err)
	}
	inputTokenBudget, err := request.ModelConfiguration.Budget.InputTokens()
	if err != nil {
		return nil, fmt.Errorf("cognitive execution: model budget: %w", err)
	}

	limit := request.MaxToolCalls
	if limit <= 0 {
		limit = DefaultToolCallLimit
	}
	var (
		tools     []inference.Tool
		toolMap   map[string]*inference.Tool
		exchanges []ToolExchange
		callCount int
	)
	if request.ToolExecutor != nil {
		tools = request.ToolExecutor.Tools()
		toolMap = make(map[string]*inference.Tool, len(tools))
		for index := range tools {
			toolMap[tools[index].Name] = &tools[index]
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("cognitive execution cancelled: %w", err)
		}
		state := ContextState{
			InputTokenBudget: inputTokenBudget,
			Tools:            append([]inference.Tool(nil), tools...),
			Exchanges:        append([]ToolExchange(nil), exchanges...),
		}
		messages, err := request.BuildContext(ctx, state)
		if err != nil {
			return nil, fmt.Errorf("cognitive execution context: %w", err)
		}
		response, err := request.Provider.Infer(ctx, inference.Request{
			Model:       request.ModelConfiguration.Model,
			Messages:    messages,
			Temperature: request.Temperature,
			MaxTokens:   request.ModelConfiguration.Budget.OutputTokens,
			Purpose:     request.Purpose,
		})
		if err != nil {
			return nil, fmt.Errorf("cognitive execution inference: %w", err)
		}
		if response == nil {
			return nil, fmt.Errorf("cognitive execution: provider returned nil response")
		}
		if len(response.ToolCalls) == 0 {
			return response, nil
		}
		if request.ToolExecutor == nil {
			return nil, fmt.Errorf("cognitive execution: provider requested tools without an executor")
		}

		for index := range exchanges {
			exchanges[index].Required = false
		}
		for _, call := range response.ToolCalls {
			callCount++
			if callCount > limit {
				return nil, fmt.Errorf("cognitive execution: tool call limit (%d) exceeded", limit)
			}
			result := request.ToolExecutor.ExecuteCall(ctx, call, toolMap[call.Name])
			exchanges = append(exchanges, ToolExchange{Call: call, Result: result, Required: true})
		}
	}
}
