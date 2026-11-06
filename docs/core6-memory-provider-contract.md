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
