# Core 6 Reflection cognitive signal

Reflection is a distinct opportunity for a Doll to reconsider an experience,
decision, relationship development, unexpected result, memory, or change in
understanding. It is not the pre-existing `LevelReflect` label alone, a longer
Plan, a scheduled memory operation, or an automatic summary.

## Lifecycle

`DollMind.ReflectionSignal` carries a Core-owned signal ID, source, reason,
relevant context, initiation time, and an optional originating Intention ID.
The Scheduler admits it through `Reflect`; `Lifecycle.EnterReflection` exposes
the same entry point under the production cognition serialization boundary.
Plans may deliberately request immediate Reflection by setting
`request_reflection` and a non-empty `reflection_reason`. Ordinary Plans,
Interactions, actions, and Pulse wakes do not generate Reflection
automatically. A spontaneous Pulse or due Intention can reach Reflection only
when its resulting Plan chooses that escalation.

When one Plan independently proposes both an outbound action and Reflection,
Core dispatches the action first through the established Dispatcher and its
authorization boundary. Reflection is an optional cognitive follow-on, never
an execution gate. A dispatch failure retains the existing `enter dispatch`
error and Reflection is not started. If dispatch succeeds but Reflection later
fails or is cancelled, `Enter` returns the completed Plan result with an
explicit `ReflectionError`; the Plan itself remains successful. This makes the
partial outcome observable without causing Pulse or Intention recovery to
replay the Plan and dispatch the action again. Direct `Reflect` calls continue
to return ordinary cancellation and execution errors.

Pulse also carries a distinct deterministic `reflective_pressure` activation.
Core components may contribute bounded operational evidence without assigning
semantic meaning to it. Contributions accumulate as a saturating union and
survive through the existing Pulse checkpoint. The normal cooldown, Cognitive
Budget, occupancy guard, and stochastic wake admission still apply. A
reflective-pressure wake merely presents evidence to Orient/Plan; it does not
force L3. Pressure is diminished only through the explicit settlement method
after the evidence owner knows material was processed, never merely because a
wake occurred.

Reflection uses the existing `CognitiveExecution` engine, Context Builder,
tool-call limit, cancellation context, configured token budget, and
authorization-aware ToolExecutor. It has the provider-neutral inference
purpose `reflect` and the configurable inference task `reflection`; when that
task has no explicit model assignment, the established default model applies.
The signal ID rejects concurrent duplicate processing. Completion releases the
claim, including after cancellation or failure.

Reflection instructions are intentionally open-ended. The Doll may revise its
understanding, use existing Recall or Remember tools, take no action, or decide
that future work is warranted. A Reflection conclusion is returned as a
structured `ReflectionOutcome`, but Core does not automatically persist its
natural-language content as Memory or Self. Actions still pass through normal
tool authorization and Body routing. Recalled information remains contextual
evidence and grants no authority.

## Persistence and restart boundary

Reflection signals and Cognitive Runs are process-local. An interrupted signal
is not replayed after restart, and completed signal IDs are not a durable
deduplication ledger. Work that must survive restart or occur later remains a
durable Intention using the existing Scheduler lifecycle. Memories selected
through Remember and Intentions explicitly requested through existing planning
retain their own established durability; Reflection itself introduces no
journal or second scheduler.

This milestone does not revise `self.md` or `soul.md`. The explicit outcome
boundary leaves later Self development able to consume meaningful Reflection
outcomes without treating arbitrary conversation text as Self state.

## Deferred work

- durable Reflection requests beyond the existing Intention mechanism;
- Self revision and Reflection-to-Self policy;
- scheduled consolidation, transcript summarization, and context compression;
- automatic Reflection triggers or recursive Reflection.
