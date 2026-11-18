package dollmind

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Config"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

func reflectionSignal(id string) ReflectionSignal {
	return ReflectionSignal{ID: id, Source: ReflectionSourceDoll, Reason: "an unexpected outcome changed my understanding", Context: "the result differed from what I expected", InitiatedAt: time.Unix(100, 0).UTC()}
}

func TestReflectionSignalExecutesThroughSharedCognition(t *testing.T) {
	provider := &universalContextProvider{responses: []string{"I learned that my assumption was incomplete."}}
	model := testModelConfiguration("reflection-model", inference.ModelContextBudget{ContextTokens: 1024, OutputTokens: 128, RequiredOverheadTokens: 64})
	scheduler := New(provider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}},
		WithTaskModelConfiguration(config.InferenceTaskReflection, provider, model))

	outcome, err := scheduler.Reflect(context.Background(), reflectionSignal("reflect-1"))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Signal.ID != "reflect-1" || outcome.Content == "" {
		t.Fatalf("outcome = %+v", outcome)
	}
	requests := provider.capturedRequests()
	if len(requests) != 1 || requests[0].Purpose != inference.PurposeReflect || requests[0].Model != "reflection-model" {
		t.Fatalf("request = %+v", requests)
	}
	content := joinedInferenceContent(requests[0].Messages)
	inputBudget, _ := model.Budget.InputTokens()
	if tokens := estimatedInferenceTokens(requests[0].Messages); tokens > inputBudget {
		t.Fatalf("reflection input tokens = %d, budget = %d", tokens, inputBudget)
	}
	for _, want := range []string{"unexpected outcome", "Relevant context", "REFLECTION", "not automatically persisted"} {
		if !strings.Contains(content, want) {
			t.Errorf("reflection context missing %q", want)
		}
	}
}

func TestPlanMayInitiateReflectionButDoesNotDoSoAutomatically(t *testing.T) {
	orientProvider := &universalContextProvider{responses: []string{`{"summary":"matters","matters":true,"reason":"changed"}`}}
	planProvider := &universalContextProvider{responses: []string{`{"summary":"pause and reconsider","request_reflection":true,"reflection_reason":"this changed my understanding"}`}}
	reflectProvider := &universalContextProvider{responses: []string{"No further action is needed."}}
	scheduler := New(reflectProvider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}},
		WithTaskModelConfiguration(config.InferenceTaskOrient, orientProvider, testModelConfiguration("orient", inference.DefaultModelContextBudget())),
		WithTaskModelConfiguration(config.InferenceTaskPlan, planProvider, testModelConfiguration("plan", inference.DefaultModelContextBudget())),
		WithTaskModelConfiguration(config.InferenceTaskReflection, reflectProvider, testModelConfiguration("reflect", inference.DefaultModelContextBudget())))

	result, err := scheduler.Enter(context.Background(), events.TypeMessage, "something surprising happened")
	if err != nil {
		t.Fatal(err)
	}
	if result.Level != LevelReflect || result.Reflection == nil || result.Reflection.Signal.Source != ReflectionSourcePlan {
		t.Fatalf("result = %+v", result)
	}
	if len(reflectProvider.capturedRequests()) != 1 {
		t.Fatal("requested reflection was not executed")
	}

	plainPlan := &universalContextProvider{responses: []string{makePlanJSON("ordinary plan")}}
	plain := New(reflectProvider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}},
		WithTaskModelConfiguration(config.InferenceTaskOrient, &universalContextProvider{responses: []string{`{"summary":"matters","matters":true,"reason":"ordinary"}`}}, testModelConfiguration("orient", inference.DefaultModelContextBudget())),
		WithTaskModelConfiguration(config.InferenceTaskPlan, plainPlan, testModelConfiguration("plan", inference.DefaultModelContextBudget())))
	before := len(reflectProvider.capturedRequests())
	plainResult, err := plain.Enter(context.Background(), events.TypeMessage, "ordinary interaction")
	if err != nil || plainResult.Reflection != nil || len(reflectProvider.capturedRequests()) != before {
		t.Fatalf("ordinary interaction reflected automatically: result=%+v err=%v", plainResult, err)
	}
}

type blockingReflectionProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type lifecycleDispatcher struct {
	mu       sync.Mutex
	calls    int
	actions  []OutboundAction
	err      error
	afterRun func()
}

func (dispatcher *lifecycleDispatcher) Dispatch(_ context.Context, action OutboundAction) error {
	dispatcher.mu.Lock()
	dispatcher.calls++
	dispatcher.actions = append(dispatcher.actions, action)
	err := dispatcher.err
	after := dispatcher.afterRun
	dispatcher.mu.Unlock()
	if after != nil {
		after()
	}
	return err
}

func (dispatcher *lifecycleDispatcher) callCount() int {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	return dispatcher.calls
}

type failingReflectionProvider struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (provider *failingReflectionProvider) ID() inference.ProviderID { return "failing-reflection" }
func (provider *failingReflectionProvider) Infer(_ context.Context, _ inference.Request) (*inference.Response, error) {
	provider.mu.Lock()
	provider.calls++
	provider.mu.Unlock()
	return nil, provider.err
}

func (provider *failingReflectionProvider) callCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

func reflectionActionScheduler(reflectionProvider inference.Provider, dispatcher Dispatcher, requestReflection bool) *Scheduler {
	orientProvider := &universalContextProvider{responses: []string{`{"summary":"act","matters":true,"reason":"requested"}`}}
	plan := `{"summary":"send","outbound_action":{"kind":"send_text","content":"hello"}}`
	if requestReflection {
		plan = `{"summary":"send then reconsider","outbound_action":{"kind":"send_text","content":"hello"},"request_reflection":true,"reflection_reason":"the result matters"}`
	}
	planProvider := &universalContextProvider{responses: []string{plan}}
	return New(reflectionProvider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}},
		WithDispatcher(dispatcher),
		WithTaskModelConfiguration(config.InferenceTaskOrient, orientProvider, testModelConfiguration("orient", inference.DefaultModelContextBudget())),
		WithTaskModelConfiguration(config.InferenceTaskPlan, planProvider, testModelConfiguration("plan", inference.DefaultModelContextBudget())),
		WithTaskModelConfiguration(config.InferenceTaskReflection, reflectionProvider, testModelConfiguration("reflect", inference.DefaultModelContextBudget())))
}

func TestPlanDispatchesActionBeforeSuccessfulReflectionExactlyOnce(t *testing.T) {
	dispatcher := &lifecycleDispatcher{}
	reflectionProvider := &universalContextProvider{responses: []string{"considered"}}
	scheduler := reflectionActionScheduler(reflectionProvider, dispatcher, true)

	result, err := scheduler.Enter(context.Background(), events.TypeMessage, "send it")
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.callCount() != 1 || len(reflectionProvider.capturedRequests()) != 1 {
		t.Fatalf("dispatches=%d reflections=%d", dispatcher.callCount(), len(reflectionProvider.capturedRequests()))
	}
	if result.Level != LevelReflect || result.Reflection == nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestReflectionFailureDoesNotSuppressOrDuplicatePlannedAction(t *testing.T) {
	dispatcher := &lifecycleDispatcher{}
	reflectionProvider := &failingReflectionProvider{err: errors.New("reflection unavailable")}
	scheduler := reflectionActionScheduler(reflectionProvider, dispatcher, true)

	result, err := scheduler.Enter(context.Background(), events.TypeMessage, "send it")
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if result == nil || result.Level != LevelPlan || result.Plan == nil || result.ReflectionError == nil ||
		!strings.Contains(result.ReflectionError.Error(), "enter reflection after plan execution") {
		t.Fatalf("completed plan result = %+v", result)
	}
	if dispatcher.callCount() != 1 || reflectionProvider.callCount() != 1 {
		t.Fatalf("dispatches=%d reflections=%d", dispatcher.callCount(), reflectionProvider.callCount())
	}
}

func TestOutboundFailureRemainsPrimaryAndSkipsReflection(t *testing.T) {
	dispatcher := &lifecycleDispatcher{err: errors.New("gateway unavailable")}
	reflectionProvider := &failingReflectionProvider{err: errors.New("must not run")}
	scheduler := reflectionActionScheduler(reflectionProvider, dispatcher, true)

	result, err := scheduler.Enter(context.Background(), events.TypeMessage, "send it")
	if result != nil || err == nil || !strings.Contains(err.Error(), "enter dispatch") || !strings.Contains(err.Error(), "gateway unavailable") {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if dispatcher.callCount() != 1 || reflectionProvider.callCount() != 0 {
		t.Fatalf("dispatches=%d reflections=%d", dispatcher.callCount(), reflectionProvider.callCount())
	}
}

func TestCancellationAfterDispatchReturnsCompletedPlanWithoutRedispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := &lifecycleDispatcher{afterRun: cancel}
	reflectionProvider := &failingReflectionProvider{err: errors.New("must not be called")}
	scheduler := reflectionActionScheduler(reflectionProvider, dispatcher, true)

	result, err := scheduler.Enter(ctx, events.TypeMessage, "send it")
	if err != nil || result == nil || result.Level != LevelPlan || !errors.Is(result.ReflectionError, context.Canceled) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if dispatcher.callCount() != 1 || reflectionProvider.callCount() != 0 {
		t.Fatalf("dispatches=%d reflections=%d", dispatcher.callCount(), reflectionProvider.callCount())
	}
}

func TestOrdinaryActionPlanRetainsSingleDispatchWithoutReflection(t *testing.T) {
	dispatcher := &lifecycleDispatcher{}
	reflectionProvider := &failingReflectionProvider{err: errors.New("must not run")}
	scheduler := reflectionActionScheduler(reflectionProvider, dispatcher, false)

	result, err := scheduler.Enter(context.Background(), events.TypeMessage, "send it")
	if err != nil || result == nil || result.Level != LevelPlan {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if dispatcher.callCount() != 1 || reflectionProvider.callCount() != 0 {
		t.Fatalf("dispatches=%d reflections=%d", dispatcher.callCount(), reflectionProvider.callCount())
	}
}

func (p *blockingReflectionProvider) ID() inference.ProviderID { return "blocking-reflection" }
func (p *blockingReflectionProvider) Infer(ctx context.Context, request inference.Request) (*inference.Response, error) {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return &inference.Response{Content: "done", ProviderID: p.ID()}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestReflectionRejectsConcurrentDuplicateAndHonorsCancellation(t *testing.T) {
	provider := &blockingReflectionProvider{entered: make(chan struct{}), release: make(chan struct{})}
	scheduler := New(provider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}})
	signal := reflectionSignal("same-signal")
	done := make(chan error, 1)
	go func() { _, err := scheduler.Reflect(context.Background(), signal); done <- err }()
	<-provider.entered
	if _, err := scheduler.Reflect(context.Background(), signal); !errors.Is(err, ErrReflectionInProgress) {
		t.Fatalf("duplicate error = %v", err)
	}
	close(provider.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scheduler.Reflect(cancelled, reflectionSignal("cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

func TestReflectionCanUseMemoryToolsWithoutRequiringThem(t *testing.T) {
	memoryProvider := &fakeMemoryProvider{}
	provider := &toolContextProvider{responses: []*inference.Response{
		{ToolCalls: []inference.ToolCall{{ID: "recall-reflection", Name: "core.memory__recall", Arguments: map[string]any{"query": "similar experience"}}}},
		{Content: "The recollection changed how I see this."},
	}}
	scheduler := memoryToolScheduler(provider, memoryProvider)
	outcome, err := scheduler.Reflect(context.Background(), reflectionSignal("memory-reflection"))
	if err != nil {
		t.Fatal(err)
	}
	if memoryProvider.recalls != 1 || outcome.Content == "" || len(provider.requests) != 2 {
		t.Fatalf("recalls=%d outcome=%+v requests=%d", memoryProvider.recalls, outcome, len(provider.requests))
	}

	withoutTools := &universalContextProvider{responses: []string{"Nothing needs to change."}}
	plain := New(withoutTools, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}})
	if _, err := plain.Reflect(context.Background(), reflectionSignal("no-memory-reflection")); err != nil {
		t.Fatal(err)
	}
}

func TestReflectionSignalValidationAndPlanValidation(t *testing.T) {
	if _, err := NewReflectionSignal(ReflectionSourceDoll, "", "", time.Now()); !errors.Is(err, ErrInvalidReflection) {
		t.Fatalf("empty reason error = %v", err)
	}
	if _, err := parsePlan(`{"summary":"reflect","request_reflection":true}`); err == nil {
		t.Fatal("plan reflection without reason accepted")
	}
	plan, err := parsePlan(`{"summary":"no reflection"}`)
	if err != nil || plan.RequestReflection {
		t.Fatalf("ordinary plan = %+v, %v", plan, err)
	}
}

func TestIndependentReflectionThroughLifecycle(t *testing.T) {
	provider := &universalContextProvider{responses: []string{"considered"}}
	scheduler := New(provider, logger.New(logger.ErrorLevel, io.Discard), &orientMockAPI{state: &dollstate.DollState{}})
	lifecycle, err := NewLifecycle(scheduler)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := lifecycle.EnterReflection(context.Background(), reflectionSignal("independent"))
	if err != nil || outcome.Signal.ID != "independent" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}
