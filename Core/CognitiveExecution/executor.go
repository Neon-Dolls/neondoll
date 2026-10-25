// Package cognitiveexecution owns Core's bounded multi-step inference loop.
// Context sources remain responsible for rebuilding their model-visible
// messages; the executor guarantees that rebuild occurs before every provider
// invocation and that one selected model configuration remains pinned for the
// complete execution.
package cognitiveexecution

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/Core/Session"
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
	TurnID             session.TurnID
	BuildContext       ContextBuilder
	ToolExecutor       ToolExecutor
	MaxToolCalls       int
}

// Result returns the terminal provider response and the correlated exchanges
// consumed during this execution. Terminal exchanges are selectable context;
// none remains required after the provider has produced its final response.
type Result struct {
	Response  *inference.Response
	Exchanges []ToolExchange
}

// Execute runs one or more inference iterations until the provider returns a
// final response. It never re-resolves the provider, model, or budget.
func Execute(ctx context.Context, request Request) (*Result, error) {
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
			TurnID:      request.TurnID,
		})
		if err != nil {
			return nil, fmt.Errorf("cognitive execution inference: %w", err)
		}
		if response == nil {
			return nil, fmt.Errorf("cognitive execution: provider returned nil response")
		}
		if len(response.ToolCalls) == 0 {
			for index := range exchanges {
				exchanges[index].Required = false
			}
			return &Result{Response: response, Exchanges: exchanges}, nil
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

// FormatToolExchange is the canonical model-visible representation of one
// correlated call/result pair.
func FormatToolExchange(exchange ToolExchange) (string, error) {
	encodedCall, err := json.Marshal(exchange.Call)
	if err != nil {
		return "", err
	}
	return "Tool call:\n" + string(encodedCall) + "\n" + inference.FormatToolResultForPrompt(exchange.Result), nil
}

// BindToolContext attaches tools and correlated exchanges to their existing
// budgeted messages. It never adds another model-visible copy.
func BindToolContext(messages []inference.Message, tools []inference.Tool, exchanges []ToolExchange, provenance string) error {
	for index := range messages {
		if messages[index].Role == "system" {
			messages[index].Tools = append([]inference.Tool(nil), tools...)
			break
		}
	}
	used := make([]bool, len(exchanges))
	for index := range messages {
		if messages[index].Provenance != provenance {
			continue
		}
		matched := false
		for exchangeIndex := range exchanges {
			if used[exchangeIndex] {
				continue
			}
			content, err := FormatToolExchange(exchanges[exchangeIndex])
			if err != nil {
				return err
			}
			if content != messages[index].Content {
				continue
			}
			messages[index].ToolCall = &exchanges[exchangeIndex].Call
			messages[index].ToolResult = &exchanges[exchangeIndex].Result
			used[exchangeIndex] = true
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("cognitive execution: tool exchange metadata missing")
		}
	}
	return nil
}
