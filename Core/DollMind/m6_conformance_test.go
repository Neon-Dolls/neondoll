package dollmind

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Body"
	"github.com/Neon-Dolls/neondoll/Core/Inference"
	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	"github.com/Neon-Dolls/neondoll/DollState"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
)

// ── Scripted Provider Fixture ───────────────────────────────────────────

// scriptedAct is one response the scriptedProvider returns on a call.
type scriptedAct struct {
	toolCalls []inference.ToolCall
	content   string
}

// scriptedProvider returns a deterministic sequence of responses.
type scriptedProvider struct {
	name     string
	acts     []scriptedAct
	idx      atomic.Int64
	lastReqs []inference.Request // all requests received, in order
}

func (p *scriptedProvider) ID() inference.ProviderID {
	return inference.ProviderID(p.name)
}

func (p *scriptedProvider) Infer(_ context.Context, req inference.Request) (*inference.Response, error) {
	p.lastReqs = append(p.lastReqs, req)
	i := int(p.idx.Add(1) - 1)
	if i >= len(p.acts) {
		// Fallback: return a no-op Plan so the test doesn't hang.
		return &inference.Response{
			Content:    `{"summary":"fallback","proposed_action":"none","observations":[],"should_reorient":false,"request_future_cognition":false}`,
			ProviderID: p.ID(),
			TokensUsed: 10,
		}, nil
	}
	act := p.acts[i]
	return &inference.Response{
		Content:    act.content,
		ToolCalls:  act.toolCalls,
		ProviderID: p.ID(),
		TokensUsed: 10,
	}, nil
}

// ── Test MindAPI ────────────────────────────────────────────────────────

// conformanceMockAPI provides a fixed DollState for M6 conformance tests.
type conformanceMockAPI struct {
	state  *dollstate.DollState
	saveFn func() error // optional; nil means Save returns nil
}

func (m *conformanceMockAPI) Inference() inference.Provider { return nil }
func (m *conformanceMockAPI) State() *dollstate.DollState   { return m.state }
func (m *conformanceMockAPI) Save() error {
	if m.saveFn != nil {
		return m.saveFn()
	}
	return nil
}

// newConformanceState creates a minimal valid DollState for plan building.
func newConformanceState() *dollstate.DollState {
	return &dollstate.DollState{
		Identity: dollstate.Identity{CanonicalName: "Spark"},
		Soul:     dollstate.Soul{Content: "A curious little AI girl."},
		Owner:    dollstate.Owner{Name: "Zero"},
	}
}

// makePlanJSON returns a minimal valid Plan JSON string.
func makePlanJSON(summary string) string {
	return `{"summary":"` + summary + `","proposed_action":"done","observations":["ok"],"should_reorient":false,"request_future_cognition":false,"future_subject":"","future_reason":"","future_wake_time":""}`
}

// allowAllEvaluator allows every request.
type allowAllEvaluator struct{}

func (allowAllEvaluator) Evaluate(_ body.AuthorityRequest) body.AuthorityResult {
	return body.AuthorityResult{Decision: body.AuthorityAllow}
}

// denyAllEvaluator denies every request with a global reason.
type denyAllEvaluator struct{}

func (denyAllEvaluator) Evaluate(_ body.AuthorityRequest) body.AuthorityResult {
	return body.AuthorityResult{
		Decision: body.AuthorityDeny,
		Reason:   "global_deny",
		Message:  "all operations denied for test",
	}
}

// setupConformanceScheduler creates a Scheduler configured for M6 tool tests.
// guardEvaluator can be nil (defaults to allowAllEvaluator).
// acts are the scripted provider responses.
func setupConformanceScheduler(acts []scriptedAct, guardEval body.AuthorityEvaluator, opts ...Option) (*Scheduler, *scriptedProvider) {
	prov := &scriptedProvider{
		name: "m6-conformance",
		acts: acts,
	}
	if guardEval == nil {
		guardEval = allowAllEvaluator{}
	}
	reg := body.NewRegistry()
	guard := body.NewGuard(reg, guardEval)
	loc, _ := reg.Local()
	exec := NewToolExecutor(guard, loc)

	log := logger.New(logger.ErrorLevel, nil)
	state := newConformanceState()
	mockAPI := &conformanceMockAPI{state: state}

	s := New(prov, log, mockAPI, append([]Option{
		WithTimeProvider(func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }),
		WithToolExecutor(exec),
	}, opts...)...)
	return s, prov
}

// ── Positive Proof ──────────────────────────────────────────────────────

func TestM6_PositiveProof_TwoSequentialToolCalls(t *testing.T) {
	// Canonical M6 proof:
	//   provider → tool A → result A
	//            → tool B → result B
	//            → final valid Plan
	toolA := inference.ToolCall{ID: "call_A", Name: "runtime.info__read"}
	toolB := inference.ToolCall{ID: "call_B", Name: "runtime.info__list"}
	finalPlan := makePlanJSON("M6 positive proof: two tools executed")

	s, prov := setupConformanceScheduler([]scriptedAct{
		{toolCalls: []inference.ToolCall{toolA}},
		{toolCalls: []inference.ToolCall{toolB}},
		{content: finalPlan},
	}, nil)

	ctx := context.Background()
	plan, dirty, err := s.Plan(ctx, events.TypeMessage, "test input", &Orientation{
		Summary: "M6 positive proof",
		Matters: true,
		Reason:  "testing tool loop",
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}
	if plan.Summary != "M6 positive proof: two tools executed" {
		t.Errorf("plan.Summary = %q, want %q", plan.Summary, "M6 positive proof: two tools executed")
	}
	if len(plan.Observations) == 0 {
		t.Error("expected non-empty observations")
	}
	// Dirty=true because observations materialise into memory items
	if !dirty {
		t.Errorf("Plan() returned dirty=%v, want true (observations materialised)", dirty)
	}

	// Verify the provider was called 3 times (2 tool rounds + 1 final)
	if len(prov.lastReqs) != 3 {
		t.Errorf("provider called %d times, want 3", len(prov.lastReqs))
	}

	// First call: Tools should be populated, ToolResults should be empty
	if len(prov.lastReqs[0].Tools) == 0 {
		t.Error("first request has no tools, expected at least 1 tool definition")
	}
	if len(prov.lastReqs[0].ToolResults) != 0 {
		t.Errorf("first request has %d ToolResults, want 0", len(prov.lastReqs[0].ToolResults))
	}

	// Second call: after first tool execution, should have 1 ToolResult
	if len(prov.lastReqs[1].ToolResults) != 1 {
		t.Errorf("second request has %d ToolResults, want 1", len(prov.lastReqs[1].ToolResults))
	}
	if prov.lastReqs[1].ToolResults[0].ToolCallID != "call_A" {
		t.Errorf("first ToolResult call ID = %q, want %q",
			prov.lastReqs[1].ToolResults[0].ToolCallID, "call_A")
	}
	if prov.lastReqs[1].ToolResults[0].Status != inference.ToolResultSuccess {
		t.Errorf("first ToolResult status = %q, want %q",
			prov.lastReqs[1].ToolResults[0].Status, inference.ToolResultSuccess)
	}

	// Third call: after both tool executions, should have 2 ToolResults
	if len(prov.lastReqs[2].ToolResults) != 2 {
		t.Errorf("third request has %d ToolResults, want 2", len(prov.lastReqs[2].ToolResults))
	}
	if prov.lastReqs[2].ToolResults[1].ToolCallID != "call_B" {
		t.Errorf("second ToolResult call ID = %q, want %q",
			prov.lastReqs[2].ToolResults[1].ToolCallID, "call_B")
	}
	if prov.lastReqs[2].ToolResults[1].Status != inference.ToolResultSuccess {
		t.Errorf("second ToolResult status = %q, want %q",
			prov.lastReqs[2].ToolResults[1].Status, inference.ToolResultSuccess)
	}

	// Verify tool executions went through the real Body
	if len(prov.lastReqs[1].ToolResults[0].Result) == 0 {
		t.Error("first ToolResult has no result data, expected Body output")
	}
	if len(prov.lastReqs[2].ToolResults[1].Result) == 0 {
		t.Error("second ToolResult has no result data, expected Body output")
	}
}

// ── Negative Proofs ─────────────────────────────────────────────────────

func TestM6_NoToolInference_BehavesAsBefore(t *testing.T) {
	// Scheduler without toolExecutor: Plan() uses the original Infer() → parsePlan path.
	prov := &scriptedProvider{
		name: "m6-no-tool",
		acts: []scriptedAct{
			{content: makePlanJSON("no-tool backward compat")},
		},
	}
	log := logger.New(logger.ErrorLevel, nil)
	state := newConformanceState()
	mockAPI := &conformanceMockAPI{state: state}
	s := New(prov, log, mockAPI, WithTimeProvider(func() time.Time {
		return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	}))

	// No WithToolExecutor → no tool loop
	ctx := context.Background()
	plan, dirty, err := s.Plan(ctx, events.TypeMessage, "no-tool test", &Orientation{
		Summary: "no-tool",
		Matters: true,
		Reason:  "backward compat",
	})
	if err != nil {
		t.Fatalf("Plan() without toolExecutor = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}
	if plan.Summary != "no-tool backward compat" {
		t.Errorf("plan.Summary = %q, want %q", plan.Summary, "no-tool backward compat")
	}
	// Dirty=true because plan has observations that materialise into memory
	if !dirty {
		t.Errorf("Plan() returned dirty=%v, want true (observations materialised)", dirty)
	}
	// Provider called exactly once (no tool loop)
	if len(prov.lastReqs) != 1 {
		t.Errorf("provider called %d times, want 1", len(prov.lastReqs))
	}
}

func TestM6_UnknownTool_ReturnsFailureResult(t *testing.T) {
	// Provider returns a ToolCall with a name that doesn't match any exposed tool.
	// The executor must produce a failure ToolResult without invoking a Body.
	s, prov := setupConformanceScheduler([]scriptedAct{
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_unknown", Name: "nonexistent__tool"},
			},
		},
		{content: makePlanJSON("unknown tool handled")},
	}, nil)

	ctx := context.Background()
	plan, _, err := s.Plan(ctx, events.TypeMessage, "unknown tool test", &Orientation{
		Summary: "unknown tool",
		Matters: true,
		Reason:  "testing unknown tool handling",
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}
	if plan.Summary != "unknown tool handled" {
		t.Errorf("plan.Summary = %q, want %q", plan.Summary, "unknown tool handled")
	}

	// The second request should contain a failure ToolResult
	if len(prov.lastReqs) < 2 {
		t.Fatalf("provider called %d times, expected >= 2", len(prov.lastReqs))
	}
	results := prov.lastReqs[1].ToolResults
	if len(results) != 1 {
		t.Fatalf("second request has %d ToolResults, want 1", len(results))
	}
	if results[0].ToolCallID != "call_unknown" {
		t.Errorf("ToolResult call ID = %q, want %q", results[0].ToolCallID, "call_unknown")
	}
	if results[0].Status != inference.ToolResultFailure {
		t.Errorf("ToolResult status = %q, want %q", results[0].Status, inference.ToolResultFailure)
	}
	if results[0].Error == nil {
		t.Fatal("ToolResult.Error is nil, expected error details")
	}
	if results[0].Error.Code != "unknown_tool" {
		t.Errorf("ToolResult.Error.Code = %q, want %q", results[0].Error.Code, "unknown_tool")
	}
}

func TestM6_DeniedAuthority_ReturnsFailureResult(t *testing.T) {
	// Guard with deny-all evaluator: every tool call is denied but the loop continues.
	s, prov := setupConformanceScheduler([]scriptedAct{
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_denied", Name: "runtime.info__read"},
			},
		},
		{content: makePlanJSON("denied handled")},
	}, denyAllEvaluator{})

	ctx := context.Background()
	plan, _, err := s.Plan(ctx, events.TypeMessage, "denied test", &Orientation{
		Summary: "denied test",
		Matters: true,
		Reason:  "testing denied authority",
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}

	if len(prov.lastReqs) < 2 {
		t.Fatalf("provider called %d times, expected >= 2", len(prov.lastReqs))
	}
	results := prov.lastReqs[1].ToolResults
	if len(results) != 1 {
		t.Fatalf("second request has %d ToolResults, want 1", len(results))
	}
	if results[0].Status != inference.ToolResultFailure {
		t.Errorf("ToolResult status = %q, want %q", results[0].Status, inference.ToolResultFailure)
	}
	if results[0].Error == nil {
		t.Fatal("ToolResult.Error is nil")
	}
	if results[0].Error.Code != "denied" {
		t.Errorf("ToolResult.Error.Code = %q, want %q", results[0].Error.Code, "denied")
	}
}

func TestM6_DeniedExecution_PreservesExecutionID(t *testing.T) {
	t.Parallel()

	// Set up a deny-all evaluator so every tool execution attempt is denied
	// but does reach Guard.Execute, producing an ExecutionResult with an ID.
	s, prov := setupConformanceScheduler([]scriptedAct{
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_deny_x1", Name: "runtime.info__read"},
			},
		},
		{content: makePlanJSON("denied execution has correlation")},
	}, denyAllEvaluator{})

	ctx := context.Background()
	plan, _, err := s.Plan(ctx, events.TypeMessage, "denied execution id", &Orientation{
		Summary: "denied execution id",
		Matters: true,
		Reason:  "testing ExecutionID preservation on denied Body execution",
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}

	// Grab the ToolResults passed back after the denied tool call.
	if len(prov.lastReqs) < 2 {
		t.Fatalf("provider called %d times, expected >= 2", len(prov.lastReqs))
	}
	results := prov.lastReqs[1].ToolResults
	if len(results) != 1 {
		t.Fatalf("second request has %d ToolResults, want 1", len(results))
	}

	r := results[0]

	// Denied execution must still be a failure.
	if r.Status != inference.ToolResultFailure {
		t.Errorf("ToolResult.Status = %q, want %q", r.Status, inference.ToolResultFailure)
	}
	if r.Error == nil {
		t.Fatal("ToolResult.Error is nil")
	}
	if r.Error.Code != "denied" {
		t.Errorf("ToolResult.Error.Code = %q, want %q", r.Error.Code, "denied")
	}

	// Core of this test: a denied Guard.Execute attempt must preserve
	// correlation IDs so multi-attempt scenarios are fully observable.
	if r.ToolCallID != "call_deny_x1" {
		t.Errorf("ToolResult.ToolCallID = %q, want %q", r.ToolCallID, "call_deny_x1")
	}
	if r.ExecutionID == "" {
		t.Error("ToolResult.ExecutionID is empty — denied execution attempt is unobservable")
	}
}

func TestM6_FailedToolContinuation_ProviderCanStillFinish(t *testing.T) {
	// Tool A fails (denied), provider gets failure ToolResult, calls Tool B (allowed),
	// then returns a valid Plan. Verifies the loop doesn't abort on failure.
	s, prov := setupConformanceScheduler([]scriptedAct{
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_A", Name: "runtime.info__read"},
			},
		},
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_B", Name: "runtime.info__list"},
			},
		},
		{content: makePlanJSON("failed-tool continuation works")},
	}, denyAllEvaluator{})

	ctx := context.Background()
	plan, _, err := s.Plan(ctx, events.TypeMessage, "failed-tool continuation", &Orientation{
		Summary: "failed tool",
		Matters: true,
		Reason:  "testing continuation after tool failure",
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}
	if plan.Summary != "failed-tool continuation works" {
		t.Errorf("plan.Summary = %q, want %q", plan.Summary, "failed-tool continuation works")
	}

	// Both tool calls should have failure results (denyAll denies everything)
	if len(prov.lastReqs) < 3 {
		t.Fatalf("provider called %d times, expected >= 3", len(prov.lastReqs))
	}
	results := prov.lastReqs[len(prov.lastReqs)-1].ToolResults
	if len(results) != 2 {
		t.Fatalf("final request has %d ToolResults, want 2", len(results))
	}
	for i, r := range results {
		if r.Status != inference.ToolResultFailure {
			t.Errorf("ToolResult[%d] status = %q, want failure", i, r.Status)
		}
	}
	// Correlation preserved
	if results[0].ToolCallID != "call_A" || results[1].ToolCallID != "call_B" {
		t.Errorf("ToolResult call IDs = %q / %q, want call_A / call_B",
			results[0].ToolCallID, results[1].ToolCallID)
	}
}

func TestM6_LoopLimit_StopsRunawayProvider(t *testing.T) {
	// Provider keeps returning tool calls. After ToolCallLimit (10) tool calls,
	// the loop must error out.
	const toolLimit = 10
	overflow := toolLimit + 3 // 13 tool calls, should hit limit at 10+1=11th

	toolCall := inference.ToolCall{ID: "call_loop", Name: "runtime.info__read"}
	acts := make([]scriptedAct, overflow)
	for i := range acts {
		acts[i] = scriptedAct{toolCalls: []inference.ToolCall{toolCall}}
	}

	s, _ := setupConformanceScheduler(acts, nil)

	ctx := context.Background()
	_, _, err := s.Plan(ctx, events.TypeMessage, "loop limit test", &Orientation{
		Summary: "loop limit",
		Matters: true,
		Reason:  "testing tool call limit",
	})
	if err == nil {
		t.Fatal("Plan() = nil error, expected loop limit error")
	}
	if !strings.Contains(err.Error(), "tool call limit") {
		t.Errorf("error = %q, want 'tool call limit'", err.Error())
	}
}

func TestM6_Cancellation_StopsToolLoop(t *testing.T) {
	s, _ := setupConformanceScheduler([]scriptedAct{
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_A", Name: "runtime.info__read"},
			},
		},
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_B", Name: "runtime.info__list"},
			},
		},
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	_, _, err := s.Plan(ctx, events.TypeMessage, "cancellation test", &Orientation{
		Summary: "cancellation",
		Matters: true,
		Reason:  "testing cancellation",
	})
	if err == nil {
		t.Fatal("Plan() = nil error, expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestM6_SequentialExecution_NoConcurrency(t *testing.T) {
	// Provider returns two tool calls in a single response. The executor must
	// execute them sequentially (not concurrently). We verify this by checking
	// that results are in the same order as the calls.
	s, prov := setupConformanceScheduler([]scriptedAct{
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_1", Name: "runtime.info__read"},
				{ID: "call_2", Name: "runtime.info__list"},
			},
		},
		{content: makePlanJSON("sequential execution")},
	}, nil)

	ctx := context.Background()
	plan, _, err := s.Plan(ctx, events.TypeMessage, "sequential test", &Orientation{
		Summary: "sequential",
		Matters: true,
		Reason:  "testing sequential execution",
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}

	// Second request should have exactly 2 ToolResults in order
	if len(prov.lastReqs) < 2 {
		t.Fatalf("provider called %d times, expected >= 2", len(prov.lastReqs))
	}
	results := prov.lastReqs[len(prov.lastReqs)-1].ToolResults
	if len(results) != 2 {
		t.Fatalf("final request has %d ToolResults, want 2", len(results))
	}
	if results[0].ToolCallID != "call_1" || results[1].ToolCallID != "call_2" {
		t.Errorf("ToolResult order = %q / %q, want call_1 / call_2",
			results[0].ToolCallID, results[1].ToolCallID)
	}
	if results[0].Status != inference.ToolResultSuccess {
		t.Errorf("ToolResult[0] status = %q, want success", results[0].Status)
	}
	if results[1].Status != inference.ToolResultSuccess {
		t.Errorf("ToolResult[1] status = %q, want success", results[1].Status)
	}
}

func TestM6_CorrelationSurvivesContinuation(t *testing.T) {
	// Three tool calls in sequence, each correlated to its original call ID.
	s, prov := setupConformanceScheduler([]scriptedAct{
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_alpha", Name: "runtime.info__read"},
			},
		},
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_beta", Name: "runtime.info__list"},
			},
		},
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_gamma", Name: "runtime.info__read"},
			},
		},
		{content: makePlanJSON("correlation preserved")},
	}, nil)

	ctx := context.Background()
	plan, _, err := s.Plan(ctx, events.TypeMessage, "correlation test", &Orientation{
		Summary: "correlation",
		Matters: true,
		Reason:  "testing correlation across continuations",
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}

	// Final request should have 3 ToolResults with correct IDs
	if len(prov.lastReqs) < 4 {
		t.Fatalf("provider called %d times, expected >= 4", len(prov.lastReqs))
	}
	finalReq := prov.lastReqs[len(prov.lastReqs)-1]
	if len(finalReq.ToolResults) != 3 {
		t.Fatalf("final request has %d ToolResults, want 3", len(finalReq.ToolResults))
	}
	expected := []string{"call_alpha", "call_beta", "call_gamma"}
	for i, want := range expected {
		if finalReq.ToolResults[i].ToolCallID != want {
			t.Errorf("ToolResult[%d].ToolCallID = %q, want %q", i, finalReq.ToolResults[i].ToolCallID, want)
		}
		if finalReq.ToolResults[i].Status != inference.ToolResultSuccess {
			t.Errorf("ToolResult[%d].Status = %q, want success", i, finalReq.ToolResults[i].Status)
		}
	}
}

func TestM6_NoProviderTypesLeakIntoBody(t *testing.T) {
	// Verify that the ToolExecutor uses Guard.Execute (M4 path), which takes
	// body.ExecutionRequest and returns body.ExecutionResult.
	// The tool executor should never construct or return inference types
	// inside the Body boundary. We verify by ensuring that Body types used
	// inside the executor are distinct from the inference types passed to the provider.
	// This is a structural/import test — it compiles as proof.
	s, prov := setupConformanceScheduler([]scriptedAct{
		{
			toolCalls: []inference.ToolCall{
				{ID: "call_A", Name: "runtime.info__read"},
			},
		},
		{content: makePlanJSON("no type leak")},
	}, nil)

	ctx := context.Background()
	plan, _, err := s.Plan(ctx, events.TypeMessage, "type leak test", &Orientation{
		Summary: "type leak test",
		Matters: true,
		Reason:  "testing no provider type leakage",
	})
	if err != nil {
		t.Fatalf("Plan() = %v, want nil", err)
	}
	if plan == nil {
		t.Fatal("Plan() returned nil plan")
	}
	if plan.Summary != "no type leak" {
		t.Errorf("plan.Summary = %q, want %q", plan.Summary, "no type leak")
	}

	// Verify the ToolResult contains Body execution output (body.ExecutionResult.Output
	// was projected into inference.ToolResult.Result)
	if len(prov.lastReqs[1].ToolResults) == 1 {
		tr := prov.lastReqs[1].ToolResults[0]
		if tr.Status != inference.ToolResultSuccess {
			t.Errorf("ToolResult status = %q, want success", tr.Status)
		}
		// The result should contain the Body's output (RuntimeInfo JSON)
		output, hasOutput := tr.Result["output"]
		if !hasOutput {
			t.Error("ToolResult.Result missing 'output' key from Body execution")
		} else {
			outStr, ok := output.(string)
			if !ok {
				t.Errorf("output is %T, want string", output)
			}
			if !strings.Contains(outStr, `"os":`) || !strings.Contains(outStr, `"go_version":`) {
				t.Errorf("output = %q, want runtime info JSON with os and go_version", outStr)
			}
		}
	}
}

func TestM6_HandlesEmptyToolsGracefully(t *testing.T) {
	// Provider returns no ToolCalls and no Content on the first call (empty response).
	// The loop should fall through to parsePlan and return an error (empty Plan).
	s, prov := setupConformanceScheduler([]scriptedAct{
		{content: ""}, // empty content
	}, nil)

	ctx := context.Background()
	_, _, err := s.Plan(ctx, events.TypeMessage, "empty test", &Orientation{
		Summary: "empty",
		Matters: true,
		Reason:  "testing empty response",
	})
	if err == nil {
		t.Fatal("Plan() = nil error, expected parse error for empty content")
	}
	if len(prov.lastReqs) != 1 {
		t.Errorf("provider called %d times, want 1", len(prov.lastReqs))
	}
}
