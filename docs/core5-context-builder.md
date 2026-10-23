# Core 5 Context Builder foundation

`Core/ContextBuilder` constructs a disposable, bounded model context from an
authorized context source. It does not store model context, mutate source
records, or treat a context window as continuity-bearing state.

The transport-independent API is:

```go
type Builder interface {
    Build(context.Context, Request) (Result, error)
}

type WindowPolicy interface {
    Select(WindowRequest) (Window, error)
}

type SourceResolver interface {
    Resolve(context.Context, ContextSource) (ResolvedSource, error)
}

type SourceBuilder interface {
    Builder
    BuildSource(context.Context, SourceRequest) (Result, error)
}

type Request struct {
    SessionID          session.ID
    TokenBudget        int
    SystemInstructions []string
    RequiredMessages   []Message
}
```

`ContextSource` is a typed identity reference (`interaction_session` or the
reserved `cognition_run` kind). It does not grant authority. `SourceResolver`
validates source existence and resolves records within trusted Core, then
returns source-tagged ordered `ContextRecord` values. It is not a separate
caller-authorization layer: ingress and action authority remain enforced at
their existing Core boundaries. The builder verifies the resolved identity, every
record's source tag, and strict sequence ordering before constructing context;
cross-source resolver output fails closed.

`SourceBuilder.BuildSource` is the generalized entry point. The original
`Builder` interface and `Build(Request)` API remain unchanged; `Build` maps
`SessionID` to an Interaction Session source.
The compatibility constructor is an internal trusted-call boundary. The
existing Session service plus `HistoryStore` are adapted internally, so
callers and the SQLite schema do not change. Cognition Run persistence and
execution are intentionally not implemented in this milestone; the default
session resolver rejects that source kind as unsupported.

M2.4 adds `CognitionRunResolver`, an ephemeral adapter for one active run. It
accepts an explicit Core-owned run ID, objective, optional wake evidence, and
available working records. On every resolution it reads current authoritative
state through the `State() *dollstate.DollState` subset already provided by
`DollMind.MindAPI`; it does not copy Doll State into a second authority. Only a
compact Doll identity (`doll_id` and canonical name) and the run objective are
required. Soul/Owner/Self/Drives, Goals, Intentions, the five most recent
Memory records, and wake evidence are budget-selectable contextual records.
Authentication state and secrets are not projected.

Required selection is explicit record metadata rather than an inference from
the model-facing role. Each Cognitive Run record carries provenance such as
`doll_mind.goals` or `cognition_run.wake_evidence`; included messages and
omission metadata preserve it. This keeps authoritative Mind data identifiable
as data rather than presenting it as a user message or instruction. The
Interaction Session adapter explicitly marks persisted system messages as
required, preserving its prior behavior and API.

Run-local working records can advance between builds through `Append`. They
remain ephemeral and are selected by the same `WindowPolicy` as session
records. `SourceMux` composes Interaction Session and Cognitive Run resolvers
without fallback between identities or lifecycles. There is no Cognitive Run
database table or synthetic Interaction Session.

M2.5 connects Pulse-originated cognition at the existing production path:
`Pulse.Runner -> Scheduler.EnterPulseWake -> Orient -> Plan`. Each admitted
wake receives an ephemeral UUID carried through `context.Context`; Orient,
Plan, and every tool-loop provider round trip share that identity. The run ID,
working records, and selected model context are not persisted.

Orient and Plan build fresh provider messages from `CognitionRunResolver`.
Before Plan, the structured L1 orientation is appended as run-local working
state. Tool-enabled Plan rebuilds again before every provider invocation.
Tool definitions and each correlated call/result exchange are represented in
the same bounded construction: the current unresolved exchange is required,
while exchanges already consumed by a later provider response become
budget-selectable. Tool call/result metadata is bound to that same selected
`inference.Message`. An adapter may translate the message to a provider-native
tool item instead of its text representation, but must not append another
model-visible copy. Context-Builder requests leave the legacy top-level
`Request.Tools` and `Request.ToolResults` empty, making accidental independent
serialization impossible. If the unresolved exchange cannot fit, construction
fails explicitly before another provider invocation.

The provider audit for M2.5 covers `MockProvider`, `OpenAIProvider`, and
`ToolEnabledProvider`. Mock and OpenAI providers consume `Messages` only;
OpenAI's wire adapter projects each message exactly once. `ToolEnabledProvider`
continues to project top-level `Tools`/`ToolResults` for legacy callers, but
recognizes message-bound tool metadata and passes its already-budgeted content
through without injection. Future native adapters must consume `Message.Tools`,
`Message.ToolCall`, and `Message.ToolResult` as a replacement representation of
that message. Top-level and message-bound tool contracts must not be combined.

M2.6 extends the same ephemeral boundary to every internal DollMind inference
path. `Scheduler.Enter` creates one run after L0 selects the Orient path;
internal Intention wakes therefore reuse one identity across Orient and Plan.
Standalone `Orient` and `Plan` calls defensively create a run at their public
entry boundary, and `DollMind.Run` creates a direct-execution run with its input
as a required working record. Nested calls reuse the run already carried by
`context.Context`, so every inference and tool continuation in one execution
has one identity while separate executions remain isolated. The shared helper
is the only run initializer; none of these runs or transcripts is persisted.

M2.7 connects the session-aware `Interaction.Service.HandleInteraction` path.
It uses the real router-resolved Core Session ID and the existing SQLite
`interaction_session_messages` history. Required instructions are reserved at
the beginning of the model input, the persisted rolling history is selected by
the shared policy, and `RequiredMessages` reserves the current user input at
the end. A completed inference atomically appends its user/assistant pair to
session history; a failed inference appends neither. Each later invocation
reconstructs a fresh window, including after Core/store restart.

The remaining production inference path outside Context Builder is the legacy
unauthenticated Core 4 `Interaction.Service.HandleEvent` compatibility path.
It has no authenticated Core Interaction Session ID, so it cannot safely use a
session source. All internal DollMind and session-aware Interaction inference
paths use bounded construction. Model-profile-specific budgets and routing
remain deferred.

`New` installs the stateless `SlidingWindowPolicy`. `NewWithPolicy` accepts a
replacement policy without changing the Builder, history store, or token
estimator interfaces. A policy receives chronologically ordered `WindowItem`
values with precomputed token estimates and an explicit `Required` marker;
its selected `Window` returns ordered whole messages, token cost, omissions,
and the selected sequence boundaries.

`Result.Messages` is provider-neutral ordered text with `system`, `user`,
`assistant`, or `tool` roles. `Result.Metadata` reports the budget and estimate,
history totals, durable history start/end sequences, selected rolling-window
start/end sequences, included message IDs, and every omitted message's
sequence, estimate, and omission reason. Window boundaries describe the
non-system conversation suffix; required system messages may precede it.

Conversation records are stored in the existing Core SQLite database in
`interaction_session_messages`, keyed and ordered by Core Session ID and an
explicit per-session sequence. They remain separate from temporary model
context and from portable Doll Memory. The store implements
`contextbuilder.HistoryStore`; future interaction wiring can append completed
turns through that interface without coupling the builder to SQLite.

The sliding policy always preserves explicitly required records (including
supplied instructions, persisted Interaction Session system messages, and
required tail messages such as the current input), then includes the largest
whole-record chronological suffix that fits the remaining budget. Required
instructions precede history and required tail messages follow it. Once a
record does not fit, it and all older optional records are omitted. Individual
records are never truncated. If required context does not fit, construction
fails explicitly. The policy stores no cursor: appending history, changing a
budget, or reopening the persistent store deterministically derives a new
window from source records.

The default deterministic estimator charges four framing tokens plus one token
per four Unicode code points, rounded up. `TokenEstimator` is replaceable so a
future inference profile can supply model-specific accounting without changing
history or builder consumers.

This milestone does not choose an inference profile, perform semantic Memory
retrieval, summarize, compact, or persist Cognitive Run turns. Those remain
later integration and policy milestones.
