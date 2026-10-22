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
}
```

`ContextSource` is a typed identity reference (`interaction_session` or the
reserved `cognition_run` kind). It is not authorization. `SourceResolver`
validates existence and caller access, then returns source-tagged ordered
`ContextRecord` values. The builder verifies the resolved identity, every
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

The sliding policy always preserves supplied and persisted system messages,
then includes the largest whole-message chronological suffix that fits the
remaining budget. Once a message does not fit, it and all older non-system
messages are omitted. Individual messages are never truncated. If required
system context does not fit, construction fails explicitly. The policy stores
no cursor: appending history, changing a budget, or reopening the persistent
store deterministically derives a new window from durable history.

The default deterministic estimator charges four framing tokens plus one token
per four Unicode code points, rounded up. `TokenEstimator` is replaceable so a
future inference profile can supply model-specific accounting without changing
history or builder consumers.

This milestone does not connect the builder to the current inference request,
choose an inference profile, retrieve Doll Memory, summarize, compact, or
automatically persist interaction turns. Those are later integration and
policy milestones; this foundation provides the durable history and bounded
construction boundaries they consume.
