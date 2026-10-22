# Core 5 Context Builder foundation

`Core/ContextBuilder` constructs a disposable, bounded model context for one
persisted Interaction Session. It does not store model context, mutate history,
or treat a context window as continuity-bearing state.

The transport-independent API is:

```go
type Builder interface {
    Build(context.Context, Request) (Result, error)
}

type Request struct {
    SessionID          session.ID
    TokenBudget        int
    SystemInstructions []string
}
```

`Result.Messages` is provider-neutral ordered text with `system`, `user`,
`assistant`, or `tool` roles. `Result.Metadata` reports the budget and estimate,
history totals, included message IDs, and every omitted message's sequence,
estimate, and omission reason.

Conversation records are stored in the existing Core SQLite database in
`interaction_session_messages`, keyed and ordered by Core Session ID and an
explicit per-session sequence. They remain separate from temporary model
context and from portable Doll Memory. The store implements
`contextbuilder.HistoryStore`; future interaction wiring can append completed
turns through that interface without coupling the builder to SQLite.

The initial policy always preserves supplied and persisted system messages,
then includes the largest whole-message chronological suffix that fits the
remaining budget. Once a message does not fit, it and all older non-system
messages are omitted. Individual messages are never truncated. If required
system context does not fit, construction fails explicitly.

The default deterministic estimator charges four framing tokens plus one token
per four Unicode code points, rounded up. `TokenEstimator` is replaceable so a
future inference profile can supply model-specific accounting without changing
history or builder consumers.

This milestone does not connect the builder to the current inference request,
choose an inference profile, retrieve Doll Memory, summarize, compact, or
automatically persist interaction turns. Those are later integration and
policy milestones; this foundation provides the durable history and bounded
construction boundaries they consume.
