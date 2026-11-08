# Core 6 Hindsight Memory Provider

The Go adapter targets Hindsight 1.2.0 at revision
`0bd55015393d224deec1073d63b24bcc0faad164` and its documented HTTP API v1.
It is an intermediate adapter: Memory Providers remain independently
installable NeonDoll modules, and no compiled-in provider registry is added.

## Mapping

- One provider instance derives one stable Hindsight bank ID from its Core Doll
  ID and configured namespace. Credentials and endpoint configuration are not
  stored in memories.
- A portable record is retained as a Hindsight document with a stable,
  record-derived `document_id`. Its portable identity, revision, provenance,
  digest, and record-scoped replay history live in Hindsight document metadata.
- Hindsight's extracted memory units are authoritative. If they differ from the
  submitted content, `Remember` returns `transformed` with a portable JSON view
  of those units. It does not claim the original was retained unchanged.
- `Recall` uses Hindsight's native semantic/keyword/graph/temporal retrieval.
  Portable kind and source filters are applied to the bounded returned set, so
  filtered recall is approximate rather than exhaustive.
- `Export` pages through tagged Hindsight documents and exports the current
  native retained view. It does not export a Core-side copy. Import uses normal
  retain semantics and may transform records again.

Supported submitted content is UTF-8 `text/plain` and valid
`application/json`, using inline or bounded stream transport.

## Consistency and limitations

- Replay identity is durable and record-scoped while its Hindsight document
  metadata remains available. There is no cross-record replay uniqueness.
- Revision checks are serialized only within one adapter instance. Deploy one
  active writer per Doll bank; Hindsight's public document upsert API does not
  expose compare-and-swap.
- Recall visibility is eventual and ordering is provider-native.
- Import is per-record and non-atomic. Completed records are not rolled back
  after a later operational failure.
- Hindsight may extract no memory units, consolidate facts into observations,
  or otherwise change the retained view. Export reports that current view.

Hindsight's `reflect` operation can later support NeonDoll's distinct
reflection cognition signal. This milestone deliberately exposes neither a
portable reflection method nor cognition wiring.

## Configuration and live test

Configure an endpoint, optional bearer token, Doll ID, namespace, request
timeout, payload bounds, recall token bound, listing page size, and a
per-document memory-unit bound. The bank
must already exist and be configured by the operator; the adapter never changes
its mission or extraction mode.

Run the optional live test with:

```sh
HINDSIGHT_TEST_ENDPOINT=http://127.0.0.1:8888 \
HINDSIGHT_TEST_TOKEN=... \
HINDSIGHT_TEST_DOLL_ID=preconfigured-test-doll \
go test -tags=integration ./Core/Memory/Hindsight
```
