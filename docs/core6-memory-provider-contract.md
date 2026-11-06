# Core 6 Memory Provider contract

Core 6 M1.1 defines the portable retained-memory boundary in `Core/Memory`.
It does not implement a provider or change Core 5 cognition.

## Contract

`MemoryRecord v1` follows `memory-record-v1.md`: required envelope fields,
open source and metadata vocabularies, MIME-typed content, and the closed
`inline`/`stream` transport choice. Inline content is limited to UTF-8 text
and JSON. Other media is transferred as a bounded, cancellable stream.

Providers implement `Remember`, `Recall`, `Export`, `Import`, `Capabilities`,
and `Health`. Recall is bounded and returns stable ID/revision references;
ranking remains provider-native. Remember and import explicitly report
storage, transformation, idempotence, rejection, and partial failure. Exact
same-revision replay is idempotent only when the envelope is identical. A
different same revision or an older revision is a conflict. Newer revisions
may be stored or transformed by the provider.

A transformed remember result contains both the complete retained
representation and explicit transformation metadata (`kind`, description,
and optional input/output media types). A rejection contains no retained
representation. This makes transformations such as image-to-text observable
without treating the retained memory as an exact copy of the experience.

Export and import limits are enforced with shared bounded sink/source
wrappers. The byte budget covers the encoded MemoryRecord envelope plus any
stream payload, while the record budget covers admitted envelopes. Exact
boundaries are accepted and the next byte or record fails with
`ErrTransferLimit`; partial reports remain available to providers.

Streaming payloads use minimal context-aware `ReadContext`/`WriteContext`
interfaces. Implementations must unblock with `ctx.Err()` when cancellation
occurs, including while I/O is waiting. The `Reader` and `Writer` adapters are
only for immediately completing standard-library I/O; blocking filesystem,
network, and future WASM streams must implement the context-aware interface
directly. This requirement is intentionally part of the portable contract so
the future WASM framing layer cannot weaken cancellation.

Core keeps no canonical shadow memory journal. Doll identity, Soul/Self,
authority, Interaction Session history, and operational state remain outside
the provider. Provider unavailability therefore cannot invalidate those
Core-owned records.

## Configuration and extensions

`memory.active` selects one named provider installation while
`memory.providers` permits multiple independently configured references. The
shape leaves future routing possible without changing cognitive interfaces.
SQLite, OpenViking, and Hindsight are equal implementation targets; no
provider-specific option appears in the contract.

`memory-provider/v1` is reserved as a distinct, stateful Extension contract.
It is intentionally rejected as unsupported by the current Gateway-only WASM
runtime. A later adapter must define bounded stream framing, lifecycle, and
least-privilege host grants. It must not disguise a Memory Provider as a
Gateway, Body, Capability, or inference provider.

## Deliberately unresolved

The canonical specification requires a separate design for deletion and
tombstones. M1.1 therefore defines no `Delete` operation and never interprets
absence from an export as deletion. Cross-provider deletion propagation,
sensitive-data policy, provider-native ordering, and the WASM streaming ABI
remain later architectural decisions. DollCard JSONL memory segments use the
same record envelope, but provider-native storage is not a DollCard format.
