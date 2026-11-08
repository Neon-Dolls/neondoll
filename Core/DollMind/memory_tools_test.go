package dollmind

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/ActionPolicy"
	"github.com/Neon-Dolls/neondoll/Core/Body"
	cognitiveexecution "github.com/Neon-Dolls/neondoll/Core/CognitiveExecution"
	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	memory "github.com/Neon-Dolls/neondoll/Core/Memory"
	"github.com/Neon-Dolls/neondoll/Core/Pulse"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	dollstate "github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

type fakeMemoryProvider struct {
	recall     func(context.Context, memory.RecallRequest) (memory.RecallResult, error)
	remember   func(context.Context, memory.RememberRequest) (memory.RememberResult, error)
	remembered []memory.RememberRequest
	recalls    int
}

func (f *fakeMemoryProvider) Remember(ctx context.Context, request memory.RememberRequest) (memory.RememberResult, error) {
	f.remembered = append(f.remembered, request)
	if f.remember != nil {
		return f.remember(ctx, request)
	}
	retained := request.Record
	return memory.RememberResult{Outcome: memory.RememberStored, Retained: &retained}, nil
}
func (f *fakeMemoryProvider) Recall(ctx context.Context, request memory.RecallRequest) (memory.RecallResult, error) {
	f.recalls++
	if f.recall != nil {
		return f.recall(ctx, request)
	}
	return memory.RecallResult{}, nil
}
func (*fakeMemoryProvider) Export(context.Context, memory.ExportRequest, memory.ExportSink) (memory.ExportReport, error) {
	return memory.ExportReport{}, errors.New("unused")
}
func (*fakeMemoryProvider) Import(context.Context, memory.ImportRequest, memory.ImportSource) (memory.ImportReport, error) {
	return memory.ImportReport{}, errors.New("unused")
}
func (*fakeMemoryProvider) Capabilities(context.Context) (memory.Capabilities, error) {
	return memory.Capabilities{ContractVersion: 1}, nil
}
func (*fakeMemoryProvider) Health(context.Context) (memory.Health, error) {
	return memory.Health{Status: memory.HealthHealthy}, nil
}

func memoryExecutor(provider memory.Provider) *ToolExecutor {
	registry := body.NewRegistry()
	return NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{}), actionpolicy.Default(), "doll", WithMemoryProvider(provider))
}

func memoryTool(t *testing.T, executor *ToolExecutor, operation string) *inference.Tool {
	t.Helper()
	for _, tool := range executor.Tools() {
		if tool.CapabilityID == memoryCapability && tool.OperationID == operation {
			copy := tool
			return &copy
		}
	}
	t.Fatalf("memory tool %q not exposed", operation)
	return nil
}

func recalledRecord(content string) memory.Record {
	data, _ := json.Marshal(content)
	return memory.Record{SchemaVersion: 1, ID: "memory-a", Revision: 3, CreatedAt: time.Unix(100, 0).UTC(),
		Source: memory.Source{Type: "interaction", Ref: "turn-a"}, Content: memory.Content{MediaType: "text/plain", Transport: memory.TransportInline, Data: data},
		Metadata: memory.Metadata{Kind: "preference", Subject: "tea"}}
}

func TestMemoryRecallReturnsProviderSelectedRecordWithProvenance(t *testing.T) {
	record := recalledRecord("prefers jasmine tea")
	provider := &fakeMemoryProvider{recall: func(_ context.Context, request memory.RecallRequest) (memory.RecallResult, error) {
		if request.Query != "tea preference" || request.Limit != 2 {
			t.Fatalf("request = %+v", request)
		}
		return memory.RecallResult{Matches: []memory.RecallMatch{{Reference: record.Reference(), Record: &record}}}, nil
	}}
	executor := memoryExecutor(provider)
	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "recall-1", Name: "core.memory__recall", Arguments: map[string]any{"query": "tea preference", "limit": float64(2)}}, memoryTool(t, executor, memoryOperationRecall))
	if result.Status != inference.ToolResultSuccess || result.Result["match_count"] != 1 {
		t.Fatalf("result = %+v", result)
	}
	encoded, _ := json.Marshal(result.Result)
	if !strings.Contains(string(encoded), "turn-a") || !strings.Contains(string(encoded), "jasmine") {
		t.Fatalf("provenance/content missing: %s", encoded)
	}
}

func TestMemoryRememberReportsStoredTransformedAndRejected(t *testing.T) {
	tests := []struct {
		name   string
		result memory.RememberResult
		want   memory.RememberOutcome
	}{
		{name: "stored", want: memory.RememberStored},
		{name: "transformed", want: memory.RememberTransformed},
		{name: "rejected", result: memory.RememberResult{Outcome: memory.RememberRejected, Reason: "not meaningful"}, want: memory.RememberRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeMemoryProvider{remember: func(_ context.Context, request memory.RememberRequest) (memory.RememberResult, error) {
				if test.result.Outcome == memory.RememberRejected {
					return test.result, nil
				}
				retained := request.Record
				if test.want == memory.RememberTransformed {
					retained.Metadata.Kind = "provider_fact"
					return memory.RememberResult{Outcome: memory.RememberTransformed, Retained: &retained, Transformation: &memory.Transformation{Kind: "extraction", Description: "extracted a preference", FromMediaType: "text/plain", ToMediaType: "text/plain"}}, nil
				}
				return memory.RememberResult{Outcome: memory.RememberStored, Retained: &retained}, nil
			}}
			executor := memoryExecutor(provider)
			result := executor.ExecuteCorrelatedCall(context.Background(), cognitiveexecution.ToolCorrelation{CognitiveRunID: "run-1", IntentionID: "intention-1"}, inference.ToolCall{ID: "remember-1", Name: "core.memory__remember", Arguments: map[string]any{"content": "I prefer jasmine tea", "media_type": "text/plain", "kind": "preference", "subject": "tea"}}, memoryTool(t, executor, memoryOperationRemember))
			if result.Status != inference.ToolResultSuccess || result.Result["outcome"] != test.want {
				t.Fatalf("result = %+v", result)
			}
			if len(provider.remembered) != 1 || provider.remembered[0].Record.Source.Type != "cognition" || !strings.Contains(provider.remembered[0].Record.Source.Ref, "intention-1") {
				t.Fatalf("remember request = %+v", provider.remembered)
			}
		})
	}
}

func TestMemoryFailuresRemainExplicitToolResults(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{"unavailable", memory.ErrUnavailable, "memory_unavailable"},
		{"cancelled", context.Canceled, "memory_cancelled"},
		{"operational", errors.New("backend broke"), "memory_recall_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := memoryExecutor(&fakeMemoryProvider{recall: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
				return memory.RecallResult{}, test.err
			}})
			result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "recall", Arguments: map[string]any{"query": "anything"}}, memoryTool(t, executor, memoryOperationRecall))
			if result.Status != inference.ToolResultFailure || result.Error == nil || result.Error.Code != test.code {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestMemoryRecallCancellationPropagatesToProvider(t *testing.T) {
	entered := make(chan struct{})
	executor := memoryExecutor(&fakeMemoryProvider{recall: func(ctx context.Context, _ memory.RecallRequest) (memory.RecallResult, error) {
		close(entered)
		<-ctx.Done()
		return memory.RecallResult{}, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan inference.ToolResult, 1)
	tool := memoryTool(t, executor, memoryOperationRecall)
	go func() {
		done <- executor.ExecuteCall(ctx, inference.ToolCall{ID: "recall", Arguments: map[string]any{"query": "anything"}}, tool)
	}()
	<-entered
	cancel()
	result := <-done
	if result.Error == nil || result.Error.Code != "memory_cancelled" {
		t.Fatalf("result = %+v", result)
	}
}

func TestMemoryRecallRejectsOversizedResultWithoutTruncation(t *testing.T) {
	record := recalledRecord(strings.Repeat("meaningful-memory-", maxMemoryToolBytes))
	executor := memoryExecutor(&fakeMemoryProvider{recall: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
		return memory.RecallResult{Matches: []memory.RecallMatch{{Reference: record.Reference(), Record: &record}}}, nil
	}})
	result := executor.ExecuteCall(context.Background(), inference.ToolCall{ID: "recall", Arguments: map[string]any{"query": "large"}}, memoryTool(t, executor, memoryOperationRecall))
	if result.Error == nil || result.Error.Code != "memory_result_too_large" || result.Result != nil {
		t.Fatalf("result = %+v", result)
	}
}

type memoryLoopProvider struct {
	calls     int
	sawResult bool
}

func (*memoryLoopProvider) ID() inference.ProviderID { return "memory-loop" }
func (p *memoryLoopProvider) Infer(_ context.Context, request inference.Request) (*inference.Response, error) {
	p.calls++
	if p.calls == 1 {
		return &inference.Response{ToolCalls: []inference.ToolCall{{ID: "recall", Name: "core.memory__recall", Arguments: map[string]any{"query": "tea"}}}}, nil
	}
	for _, message := range request.Messages {
		if strings.Contains(message.Content, "jasmine tea") {
			p.sawResult = true
		}
	}
	return &inference.Response{Content: "done"}, nil
}

func TestMemoryRecallParticipatesInBoundedSharedToolLoop(t *testing.T) {
	record := recalledRecord("prefers jasmine tea")
	executor := memoryExecutor(&fakeMemoryProvider{recall: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
		return memory.RecallResult{Matches: []memory.RecallMatch{{Reference: record.Reference(), Record: &record}}}, nil
	}})
	provider := &memoryLoopProvider{}
	model := config.ModelConfiguration{Name: "memory", Provider: "test", Model: "model", Endpoint: "local", Budget: inference.ModelContextBudget{ContextTokens: 2000, OutputTokens: 200, RequiredOverheadTokens: 100}}
	result, err := cognitiveexecution.Execute(context.Background(), cognitiveexecution.Request{Provider: provider, ModelConfiguration: model, ToolExecutor: executor,
		BuildContext: func(_ context.Context, state cognitiveexecution.ContextState) ([]inference.Message, error) {
			messages := []inference.Message{{Role: "system", Content: "bounded"}}
			for _, exchange := range state.Exchanges {
				content, _ := cognitiveexecution.FormatToolExchange(exchange)
				messages = append(messages, inference.Message{Role: "tool", Content: content})
			}
			return messages, nil
		}})
	if err != nil || result.Response.Content != "done" || provider.calls != 2 || !provider.sawResult {
		t.Fatalf("result=%+v calls=%d saw=%v err=%v", result, provider.calls, provider.sawResult, err)
	}
}

func TestMemoryRecallIsAvailableAcrossOrientPlanAndPulse(t *testing.T) {
	record := recalledRecord("prefers jasmine tea")
	newMemory := func() *fakeMemoryProvider {
		return &fakeMemoryProvider{recall: func(context.Context, memory.RecallRequest) (memory.RecallResult, error) {
			return memory.RecallResult{Matches: []memory.RecallMatch{{Reference: record.Reference(), Record: &record}}}, nil
		}}
	}
	t.Run("orient", func(t *testing.T) {
		mem := newMemory()
		provider := &toolContextProvider{responses: []*inference.Response{
			{ToolCalls: []inference.ToolCall{{ID: "orient-memory", Name: "core.memory__recall", Arguments: map[string]any{"query": "tea"}}}},
			{Content: `{"summary":"memory considered","matters":false,"reason":"complete"}`},
		}}
		if _, err := memoryToolScheduler(provider, mem).Orient(context.Background(), events.TypeMessage, "What tea do I like?"); err != nil {
			t.Fatal(err)
		}
		if mem.recalls != 1 || len(provider.requests) != 2 {
			t.Fatalf("recalls=%d requests=%d", mem.recalls, len(provider.requests))
		}
	})
	t.Run("plan", func(t *testing.T) {
		mem := newMemory()
		provider := &toolContextProvider{responses: []*inference.Response{
			{ToolCalls: []inference.ToolCall{{ID: "plan-memory", Name: "core.memory__recall", Arguments: map[string]any{"query": "tea"}}}},
			{Content: makePlanJSON("memory considered")},
		}}
		if _, _, err := memoryToolScheduler(provider, mem).Plan(context.Background(), events.TypeMessage, "plan tea", &Orientation{Summary: "tea", Matters: true}); err != nil {
			t.Fatal(err)
		}
		if mem.recalls != 1 || len(provider.requests) != 2 {
			t.Fatalf("recalls=%d requests=%d", mem.recalls, len(provider.requests))
		}
	})
	t.Run("pulse", func(t *testing.T) {
		mem := newMemory()
		provider := &toolContextProvider{responses: []*inference.Response{
			{Content: `{"summary":"check memory","matters":true,"reason":"relevant"}`},
			{ToolCalls: []inference.ToolCall{{ID: "pulse-memory", Name: "core.memory__recall", Arguments: map[string]any{"query": "tea"}}}},
			{Content: makePlanJSON("memory considered")},
		}}
		if err := memoryToolScheduler(provider, mem).EnterPulseWake(context.Background(), pulse.PulseWake{Pressure: 0.8, EffectivePressure: 0.8}); err != nil {
			t.Fatal(err)
		}
		if mem.recalls != 1 || len(provider.requests) != 3 {
			t.Fatalf("recalls=%d requests=%d", mem.recalls, len(provider.requests))
		}
	})
}

func memoryToolScheduler(provider inference.Provider, mem memory.Provider) *Scheduler {
	registry := body.NewRegistry()
	executor := NewToolExecutor(registry, body.NewGuard(registry, allowAllEvaluator{}), actionpolicy.Default(), "doll", WithMemoryProvider(mem))
	state := &dollstate.DollState{Identity: dollstate.Identity{DollID: "doll", CanonicalName: "Spark"}}
	return New(provider, logger.New(logger.ErrorLevel, nil), &orientMockAPI{state: state}, WithToolExecutor(executor))
}

func TestMemoryDisabledDoesNotExposeTools(t *testing.T) {
	registry := body.NewRegistry()
	executor := NewToolExecutor(registry, body.NewGuard(registry, &recordingAuthority{}), actionpolicy.Default(), "doll")
	for _, tool := range executor.Tools() {
		if tool.CapabilityID == memoryCapability {
			t.Fatalf("memory tool exposed while disabled: %+v", tool)
		}
	}
}
