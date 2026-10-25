# Core 5 unified cognitive execution closure audit

This audit covers the production inference call graph after M2.21. It treats
provider implementations and provider-to-provider delegation as infrastructure;
all Core-owned cognitive inference must enter through `CognitiveExecution.Execute`.

## Result

No remaining independent production cognitive inference path was found. The
shared executor is the sole caller that creates a model request and invokes a
selected provider. `ToolEnabledProvider` contains the only other production
calls to `Infer`; both delegate the already-built request to its wrapped
provider and do not own an inference lifecycle.

The audit therefore required no production behavior change. A source-level
architecture test now rejects future production `Infer` call sites outside the
shared executor and the explicitly audited adapter.

## Production call-path inventory

| Entry | Path to shared execution | Context identity and persistence |
| --- | --- | --- |
| Authenticated Body interaction | Doll Link `BodyIngress` -> `Interaction.Router` -> `Service.HandleInteraction` -> `CognitiveExecution.Execute` | Router-resolved Interaction Session and one Core-owned Turn ID; only the terminal user/assistant pair is persisted |
| Orient | `Scheduler.Enter`, `EnterWake`, or `EnterPulseWake` -> `Orient` -> `executeCognition` -> `CognitiveExecution.Execute` | One ephemeral Cognitive Run reused by nested cognition; no run persistence |
| Plan | `Scheduler.Enter` -> `Plan` -> `executeCognition` -> `CognitiveExecution.Execute` | Reuses the active Cognitive Run and its selectable working evidence |
| Direct cognition | `Scheduler.Run` -> `executeCognition` -> `CognitiveExecution.Execute` | A new ephemeral Cognitive Run per direct execution |
| Tool-loop continuation | `CognitiveExecution.Execute` loop -> caller `BuildContext` -> selected provider | Same execution identity, provider, model configuration, budget, and ToolExecutor on every iteration |

The production daemon resolves the complete task model configuration and
provider at composition time. Interaction and the DollMind scheduler receive
tool-aware provider adapters plus one trusted `DollMind.ToolExecutor`. That
executor projects allowed Local Body capabilities and invokes them through the
existing `Body.Guard`; the shared execution engine does not grant authority.

## Verified invariants

- Every provider invocation is immediately preceded by the execution's
  `BuildContext` callback. Interaction rebuilds from durable Session history;
  cognition rebuilds from the ephemeral Cognitive Run resolver.
- One `CognitiveExecution.Request` pins provider, model identifier, input
  budget, output reserve, tool executor, and tool-call limit for the complete
  execution. The loop does not resolve any of them again.
- Unresolved tool exchanges are required. Consumed exchanges are selectable
  under the existing sliding-window policy. Calls and results remain one
  correlated context record, and required overflow fails before inference.
- Interaction holds its per-session serialization boundary around context
  construction, all tool iterations, and the atomic terminal history append.
  Intermediate exchanges are not persisted. Session ID, Turn ID, event
  correlation ID, provider tool-call IDs, and Cognitive Run ID remain distinct.
- Cancellation is checked before every iteration and is passed to context
  construction, provider inference, Guard execution, and session-lock waits.
- Pulse and intention wakes enter the same Orient/Plan methods; they do not
  own alternate provider calls or tool loops.

## Regression evidence

`TestProductionInferenceCallsStayBehindCognitiveExecution` parses all
non-test Go files and fails on a call to `Infer` outside:

1. `Core/CognitiveExecution/executor.go`, the sole cognitive invocation; or
2. `Core/Inference/tool_provider.go`, the audited provider adapter delegation.

Executor tests additionally prove fresh builds, multi-step correlated tools,
required-exchange lifecycle, and fixed model/output/input budgets across loop
iterations. Interaction and DollMind tests cover their identity, persistence,
authorization, cancellation, context-window, and production-wiring boundaries.

## Remaining gaps

No closure gap remains in the current production call graph. Provider-native
adapters may legitimately delegate or serialize a request, but a new adapter
call site must be added to the architecture test only after confirming it does
not construct or accumulate an independent cognitive context. New cognitive
entry points must use `CognitiveExecution.Execute`.
