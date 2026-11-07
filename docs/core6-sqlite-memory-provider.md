# Core 6 SQLite Memory Provider

M2.1 implements the first functional provider behind the Core-owned
`Core/Memory.Provider` contract. It does not alter Core 5 cognition or make
SQLite part of Context Builder, Interaction, Pulse, Orient, or Plan.

## Driver and configuration

The adapter is `Core/Memory/SQLite` and uses the repository's existing
pure-Go `modernc.org/sqlite` driver. `sqlite.Open` accepts a database path, a
required stable Doll ID, and an optional maximum inline-record size. An empty
path defaults to `neondoll-memory.sqlite`; the inline limit defaults to 4 MiB.
The independently configurable stream limit defaults to 16 MiB.
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

Provider startup transactionally migrates the initial M2.1 replay schema to a
versioned digest representation. Existing `envelope_sha256` values are
preserved verbatim as `legacy-envelope-v1`; they are the only durable identity
of historical requests whose retained Memory revision may since have changed.
New replay keys use `retained-v1`, which covers the canonical envelope and
exact stream payload. The original replay key, Memory ID, revision, and Doll
ID are retained. A matching historical key for a superseded revision is
recognized without conflict but returns an explicit rejected/superseded
outcome rather than claiming the old revision is still retained or restoring
it. The old table is replaced only after every row succeeds. Failed migration
rolls back both replay and payload-column changes, and reopening an
already-current database performs no schema mutation.

`Remember` accepts inline and streamed `text/plain` and `application/json`.
Stream bytes are copied through the cancellable M1.1 interface under a hard
provider limit, checked against any declared size, validated as UTF-8 text or
JSON, and retained in a private SQLite BLOB. Unsupported MIME types, malformed
content, size mismatches, and over-limit content produce an explicit
`rejected` result; no transformation is claimed. Stable IDs and revisions use
the common M1.1 conflict rules. Exact replay includes the stream payload in
its identity, newer revisions replace the retained representation, and stale
or differing same-revision records are rejected.

`Recall` performs bounded case-insensitive substring matching over retained
text/JSON and supports metadata-kind and source-type filters. Results are
ordered by effective experience/creation time and stable Memory ID. This is
basic local retrieval, not semantic ranking or vector search.

Export emits portable MemoryRecord envelopes and exposes retained stream BLOBs
through bounded `StreamReader` payloads. It uses deterministic keyset
iteration, preflights each next record against the remaining transfer budget,
and loads at most one record/payload at a time. No SQLite cursor is held while
calling the external sink. Import reads and validates stream payloads through
the shared bounded, cancellable transfer wrapper and applies supported records
in one transaction. A source, cancellation, validation, or storage failure
rolls back the transaction; content and revision conflicts remain explicit
per-item rejections. SQLite schema, indexes, and row IDs are never exported.

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
