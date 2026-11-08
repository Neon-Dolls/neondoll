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
- Hindsight's extracted memory units are authoritative. `Remember`
  conservatively returns `transformed` with a portable JSON view of those
  units, even when one unit has identical text: the public API does not prove
  that context, tags, classification, temporal fields, and extraction state
  are all equivalent to the submitted portable representation.
- `Recall` uses Hindsight's native semantic/keyword/graph/temporal retrieval.
  It resolves each unique native document at most once, preserves first-match
  relevance order, and returns one portable record with the original NeonDoll
  identity and provenance for all matching units from that document. Portable
  kind and source filters are applied to this bounded set, so filtered recall
  is approximate rather than exhaustive. Missing identity metadata is an
  explicit error and is never replaced with fabricated identity.
- `Export` pages through tagged Hindsight documents and exports the current
  native retained view. It does not export a Core-side copy. Import uses normal
  retain semantics and may transform records again.

Supported submitted content is UTF-8 `text/plain` and valid
`application/json`, using inline or bounded stream transport.

## Memory portability, not database replication

Export describes the information the source provider retained at export time.
Import submits that portable representation to the destination provider's
normal native retention process. Hindsight may extract different facts,
consolidate them, reinterpret the representation, retain nothing, or reject it.
Repeated migrations may therefore produce different retained representations.

Portable record identity, revision, and provenance remain distinct from the
provider's internal memory representation and are preserved where Hindsight's
document metadata supports them. A successful destination transformation is
reported as `ImportTransformed`; a subjective refusal is `ImportRejected`;
authentication, timeout, cancellation, protocol, and storage failures stop the
import as operational errors with an incomplete partial report.

This is memory portability: remembered information is transferred for another
provider to assimilate. It is not database replication and promises neither
byte-for-byte restoration nor semantic equivalence with the source bank.

## Consistency and limitations

- Replay identity is durable and record-scoped while its Hindsight document
  metadata remains available. At most 128 replay keys are retained per record
  by default. Existing keys are never evicted; a new key is explicitly rejected
  at the configured limit before the document is changed. Hindsight does not
  document an upstream byte limit for item metadata, so the adapter uses this
  conservative count bound in addition to its HTTP/payload limits. There is no
  cross-record replay uniqueness.
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
timeout, payload bounds, recall token and document-lookup bounds, listing page
size, per-document memory-unit bound, and replay-key bound. The bank
must already exist and be configured by the operator; the adapter never changes
its mission or extraction mode.

Run the optional live test with:

```sh
HINDSIGHT_TEST_ENDPOINT=http://127.0.0.1:8888 \
HINDSIGHT_TEST_TOKEN=... \
HINDSIGHT_TEST_DOLL_ID=preconfigured-test-doll \
go test -tags=integration ./Core/Memory/Hindsight
```
