# Core 5 production lifecycle

Core uses one `DollMind.Lifecycle` to serialize process-local cognition entry
for spontaneous Pulse wakes, durable intention wakes, and permission outcome
reconsideration. Durable authority remains in Doll State and PermissionRequest
persistence; the lifecycle object is reconstructed on every daemon start.

The existing Pulse runner is also the intention clock. Every tick checks
`Scheduler.DueIntentions` before evaluating an optional spontaneous wake.
This check remains active when spontaneous Pulse cognition is disabled, so an
overdue intention is dispatched after restart and an intention becoming due
during normal operation is dispatched without a separate scheduler. Failed
cognition leaves the intention pending for a later tick. The runner's existing
occupancy claim and the lifecycle serialization mutex prevent concurrent
duplicate dispatch.

Permission decisions remain durable before execution. The production Gateway
runtime supplies the existing `ToolExecutor` as its resolution handler, so an
authenticated decision triggers the continuation immediately. Its replay loop
also claims approved or denied continuations recovered at startup. Current
ActionPolicy and Body Guard checks are reapplied by `ToolExecutor`; denial,
expiry, and cancellation never become executable continuations.

Continuation guarantees are deliberately narrower than exactly-once external
side effects:

- `waiting` is pending Owner approval.
- `ready`/`denied` is resolved and awaiting the durable execution claim.
- `running` means Core persisted its claim before dispatch.
- `succeeded` or `failed` contains the recorded outcome and is never executed
  again.
- If Core restarts with an approved continuation still `running`, it records a
  terminal failure with `execution_outcome_ambiguous` and notifies fresh
  cognition. It does not repeat a Body action that may already have happened.
  A denied `running` continuation is safe to return to denial handling because
  it has no external side effect.

If no eligible Body is available, the approved continuation returns to
`ready`; the production replay loop claims it again after the Registry changes.
Once a Body has actually returned an execution failure, that result is
terminal and is handed to cognition. Core neither loses the continuation nor
fabricates success.

Because Cognitive Runs intentionally remain ephemeral, Core cannot provide
exactly-once semantics for an unapproved autonomous tool side effect if the
process dies after the Body performs it but before the intention's final Doll
State save. The intention remains recoverable and is reconsidered, but tools
that need stronger guarantees must use their existing execution correlation
or provide idempotent operations.
