# Core 6 SQLite Memory Provider

M2.1 implements the first functional provider behind the Core-owned
`Core/Memory.Provider` contract. It does not alter Core 5 cognition or make
SQLite part of Context Builder, Interaction, Pulse, Orient, or Plan.

## Driver and configuration

The adapter is `Core/Memory/SQLite` and uses the repository's existing
pure-Go `modernc.org/sqlite` driver. `sqlite.Open` accepts a database path, a
required stable Doll ID, and an optional maximum inline-record size. An empty
path defaults to `neondoll-memory.sqlite`; the inline limit defaults to 4 MiB.
Database files and newly created parent directories are restricted to the
local user.

A provider instance is scoped to one Doll. The private primary key includes
both Doll ID and Memory ID, so independently opened Dolls can safely use the
same database without reading or overwriting one another's retained memories.
There is no Core-owned shadow journal.

## Private schema and behavior

The adapter stores the canonical encoded `MemoryRecord v1` envelope alongside
private columns used for deterministic text retrieval. Those columns and
SQLite row details never appear in recall, export, or import records.
Replay-key bookkeeping is private provider state.

`Remember` currently accepts inline `text/plain` and `application/json`.
Unsupported MIME types, stream transport, and over-limit content produce an
explicit `rejected` result; no transformation is claimed. Stable IDs and
revisions use the common M1.1 conflict rules: exact replay is idempotent,
newer revisions replace the retained representation, and stale or differing
same-revision records are rejected.

`Recall` performs bounded case-insensitive substring matching over retained
text/JSON and supports metadata-kind and source-type filters. Results are
ordered by effective experience/creation time and stable Memory ID. This is
basic local retrieval, not semantic ranking or vector search.

Export emits only portable MemoryRecord envelopes. Import reads through the
shared bounded, cancellable transfer wrapper and applies supported records in
one transaction. A source, cancellation, validation, or storage failure rolls
back the transaction; content and revision conflicts remain explicit per-item
rejections. SQLite schema, indexes, and row IDs are never exported.

## Lifecycle and deferred integration

Opening creates or validates the private schema and enables WAL, foreign keys,
and a bounded busy timeout. `Health` reports unavailable storage explicitly;
`Close` is idempotent.

This provider is a Go implementation of the existing contract. The shared
`memory-provider/v1` WASM transport is still deferred: bounded stream framing,
extension lifecycle wiring, and host capabilities must be implemented before
an external WASM Memory Provider can use the same interface. SQLite does not
receive a special cognition path, and this milestone does not implement
production provider selection, DollCard v2 ZIP transport, deletion,
tombstones, consolidation, or forgetting.
