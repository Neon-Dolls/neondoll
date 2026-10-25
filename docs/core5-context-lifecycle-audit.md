# Core 5 context lifecycle closure audit

This audit covers the Context Builder work delivered in M2.1–M2.17. It was
performed against the Core 5 session, shared-context construction, extension,
and roadmap concepts, plus the production inference call graph on `main` after
M2.17.

## Acceptance result

The context lifecycle workstream is complete for its defined scope. No release
blocking context defect or obsolete unbounded production inference path was
found. The audit required no production code change.

## Production inference inventory

| Path | Context source | Fresh build boundary | Model task |
| --- | --- | --- | --- |
| External interaction | Persisted Interaction Session history | `Interaction.Service.HandleInteraction` before each inference | `interaction` |
| Orient | Ephemeral Cognitive Run and authoritative Doll Mind state | `Scheduler.Orient` before inference | `orient` |
| Plan without tools | The active ephemeral Cognitive Run | `Scheduler.Plan` before inference | `plan` |
| Plan with tools | The active run plus selectable correlated tool exchanges | `toolCognize` before every loop inference | `plan` |
| Direct cognition | A new ephemeral Cognitive Run | `Scheduler.Run` before inference | `direct_cognition` |

The only provider calls in production Core code are these boundaries and the
`ToolEnabledProvider` delegation layer. The delegation layer adapts an already
constructed request; it does not own or accumulate context.

## Verified lifecycle properties

- Interaction inference uses a router-resolved Core Session ID. The complete
  build, inference, and atomic user/assistant history append is serialized per
  session while separate sessions remain concurrent.
- New history records carry a Core-owned Turn ID. Conversation selection uses
  complete explicit turns, rejects orphan assistant and tool records, and has
  an isolated adjacency fallback for pre-Turn-ID history.
- SQLite history is durable and reconstructed after store/Core restart. Context
  windows remain derived state and are rebuilt rather than persisted.
- Cognitive Run IDs, working records, and tool exchanges are process-local and
  execution-local. No Cognitive Run persistence or synthetic Interaction
  Session exists.
- Interaction, Orient, Plan, direct cognition, Pulse/intention paths, and every
  tool-loop continuation use the complete task-selected model configuration.
  Input capacity is `context_tokens - output_tokens - overhead_tokens`, and the
  configured output reserve is passed as inference `MaxTokens`.
- Required instructions, current interaction input, essential cognition
  identity/objective, and unresolved tool exchanges fail explicitly if they do
  not fit. Optional history and Mind records are selected as whole records or
  complete turns and are never individually truncated.
- Tool definitions and call/result correlation metadata are attached to the
  same budgeted messages. Context-Builder callers do not populate the legacy
  top-level tool-result fields, preventing a provider adapter from adding an
  unbudgeted second copy.
- Invalid task references and unsupported providers fail at runtime composition
  before an inference request is sent. Unassigned known tasks resolve to the
  explicit configured default.

## Test coverage reviewed

Existing focused coverage includes:

- sliding-window advancement, deterministic selection, required overflow,
  oversized records, complete turns, legacy history, and source isolation;
- atomic concurrent history appends, restart reconstruction, and session
  isolation;
- per-session inference serialization, cross-session concurrency, failed
  inference persistence behavior, and restart continuity;
- ephemeral run isolation, Pulse and intention entry, direct cognition, and
  repeated tool-loop reconstruction;
- oversized required tool results, tool correlation, configured budgets, task
  routing, default fallback, distinct endpoints, and real local HTTP provider
  integration.

## Deferred enhancements and non-blocking gaps

These are outside the completed bounded-context lifecycle and are not release
blockers for this workstream:

- The deterministic token estimator is an approximation. A model-specific
  estimator can replace it through the existing interface.
- Semantic retrieval, summarization, compression, and model-capability
  discovery remain deliberately deferred.
- Cognitive Mind sections are selected as coarse whole records, and Memory
  context is limited to the currently defined recent references. Finer-grained
  relevance selection is a future policy improvement.
- External Interaction inference does not yet have multi-step tool
  orchestration. Turn IDs and history records are shaped to support it, but it
  must reuse the same fresh-build rule when introduced.
- Interaction has not yet migrated onto the shared multi-step execution
  component introduced after this audit. Its existing single-invocation path
  remains freshly bounded; a later migration can add tool capability without
  changing its persisted context source.

Any new inference entry point or provider adapter must be treated as a new
context-lifecycle boundary: it must select a complete model configuration,
build immediately before every provider invocation, and serialize structured
tool content exactly once within that budget.
