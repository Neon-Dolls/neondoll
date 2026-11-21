# Core 6 developing Self

Core treats Soul, Self, and Memory as separate concerns:

- Soul is the Doll's given foundation and is not changed by Self revision.
- Self is the Doll's current, developing understanding of who it is.
- Memory is provider-owned, subjective retained experience and information.

## Contract and persistence

The active Self is one Markdown snapshot with a stable revision ID, a monotonic
revision number, and creation/update timestamps. `self.max_tokens` bounds the
snapshot (default: 2,000 estimated tokens). A revision is a complete
replacement: Core validates it without truncation and atomically compares the
caller's expected revision ID before activating it. A stale or invalid proposal
leaves the previous revision active.

SQLite initializes `self_json` when a Doll row is first created. Subsequent
whole-Doll saves preserve that column at the SQL upsert boundary, even when the
caller holds an older snapshot or uses another database connection. Only the
revision-controlled Self replacement operation may update an existing
`self_json` value.

Only the latest accepted revision is persisted in the existing Doll state
record and loaded after restart. Core does not maintain an unbounded Self
history or inject previous revisions into cognition. The legacy structured
Self fields used by DollCard v1 remain readable and survive replacement for
compatibility; the Markdown snapshot aligns with DollCard v2's `self/self.md`
section. A production v2 archive pipeline remains a later milestone.

## Cognition

The current Self is an optional, provenance-labelled Context Builder record for
Interaction, Orient, Plan, Reflection, Pulse, and direct cognition. Required
instructions and current objectives take precedence when the model context is
too small. Every context rebuild reads the latest revision, so a successful
revision becomes visible on the next inference iteration; revision history is
never appended.

`core.self__revise` is the model-directed revision tool. It accepts
`expected_revision_id` and the complete replacement Markdown. It runs through
the shared ToolExecutor and existing ActionPolicy, but it cannot edit Soul,
create authority, or implicitly alter relationships. Reflection may choose the
tool, but Reflection output alone never changes Self.

A Self change can be meaningful enough to remember. That remains a separate,
explicit `core.memory__remember` decision: Self revision neither requires a
Memory Provider nor claims that a memory was created. Memory failure cannot
roll back an accepted Self revision.
