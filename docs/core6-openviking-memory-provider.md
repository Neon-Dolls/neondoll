# Core 6 OpenViking Memory Provider

The adapter in `Core/Memory/OpenViking` implements the Core-owned
`memory.Provider` contract with OpenViking's documented HTTP API v1. It was
developed against OpenViking revision
`9ac008f9b86c91b79d6e93eb8df8ea7f4d48cecb` (the documentation identifies the
evaluated release line as 0.3.22).

## Mapping

Each Core provider instance is scoped to one Doll and writes below:

```
viking://user/{user_id}/memories/{memory_type}/{namespace}/{doll_id}/
```

One JSON file owns the currently retained representation of a stable memory
ID. The filename is a SHA-256 digest of that ID; the file contains the portable
MemoryRecord v1 envelope, a bounded stream payload when present, and replay-key
digests. A bounded plain-text projection of the content is included in that
same file so OpenViking can index streamed text rather than opaque base64. This
is provider-owned OpenViking content, not a Core-side journal.

The adapter uses only public APIs:

- `POST /api/v1/content/write` for retained records;
- `GET /api/v1/content/read?raw=true` for exact retained representations;
- `POST /api/v1/search/find` for semantic recall;
- paginated `GET /api/v1/fs/ls` for bounded export; and
- `GET /api/v1/system/status` for health.

At the targeted revision, authentication uses `X-API-Key`; content writes are
upserts and create missing parent directories; raw reads return the retained
file body; filesystem listing uses `offset`, `limit`, and `has_more`; semantic
search accepts a result `limit` but exposes no offset/cursor. Memory writes are
placed beneath the configured memory-type directory (`experiences` by
default). File content is committed before semantic/vector refresh completes,
so direct retained reads can succeed while semantic recall is still eventual.

Writes wait for OpenViking's semantic/vector refresh. OpenViking may report an
error after committing content if background indexing fails. Retrying the same
record/replay key is safe; recall can lag until indexing catches up.

## Guarantees and limitations

- `text/plain` and `application/json`, inline or bounded stream transport, are
  supported. Other MIME types are explicitly rejected.
- The read-back representation determines whether `Remember` reports `stored`
  or `transformed`; transformed data is never reported as exact storage.
- Replay keys are durable within one memory record and survive ordinary
  updates and reconnects because historical digests remain in that retained
  OpenViking document. Identical retries are idempotent; conflicting reuse for
  that record fails. Cross-record replay-key uniqueness is not provided.
- The public OpenViking write API does not expose compare-and-swap. A Provider
  instance serializes read/check/write operations, but separate writers aimed
  at the same Doll namespace can race. Deploy exactly one writer per namespace.
  The adapter does not claim cross-instance atomicity or exactly-once writes.
- Capabilities therefore report durable record-scoped replay,
  `single_writer` revision conflicts, eventual read-after-write visibility,
  per-record import atomicity, and provider-native nondeterministic retrieval.
- Export reads incrementally and applies the shared record/byte budget. It
  exports what OpenViking currently retains, not its private vector indexes or
  generated directory summaries.
- Import is record-by-record. A partial report is possible; OpenViking has no
  documented multi-record transaction spanning these API operations.
- Recall is OpenViking-native semantic retrieval. Filtered requests overfetch
  up to `MaxRecallCandidates` before applying portable metadata/source filters.
  OpenViking's documented search endpoint has a limit but no offset cursor, so
  matches outside that bounded candidate set are not guaranteed exhaustive.

## Configuration

Construct `openviking.Config` with an HTTP(S) endpoint, API key (when the
deployment requires one), OpenViking user ID, Doll ID, optional memory type
(the documented built-in `experiences` type by default), optional namespace,
request timeout, and byte limits. Credentials are never placed in Viking URIs
or record content. Replay metadata is capped per record (1,024 keys by
default). Different Dolls must use distinct Doll IDs (and normally
tenant-scoped OpenViking credentials).

This milestone provides the Go provider adapter only. It does not select a
production provider, wire memory into cognition, or implement the future
`memory-provider/v1` WASM transport.

Memory Providers are NeonDoll modules. This Go adapter is an intermediate
implementation: OpenViking, SQLite, and Hindsight must ultimately be installed
and loaded through the shared module system without recompiling Core. No
hardcoded provider registry or alternate plugin system is introduced here.

## Optional live check

Set `OPENVIKING_TEST_ENDPOINT`, `OPENVIKING_TEST_API_KEY`,
`OPENVIKING_TEST_USER_ID`, and `OPENVIKING_TEST_DOLL_ID`, then run:

```
go test -tags=integration ./Core/Memory/OpenViking
```

Normal CI uses deterministic HTTP contract tests and requires no OpenViking
deployment.
