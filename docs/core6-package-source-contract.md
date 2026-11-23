# Core 6 Package Source contract v1

Package Sources own package discovery, version/dependency resolution,
acquisition strategy, source-specific approvals, and proactive update or
installation proposals. Core does not repeat that intelligence. It validates a
complete source-authored plan, acquires its declared artifacts, verifies their
declared transfer bounds, and hands accepted items to the existing Skill or
Extension installation lifecycle. Package-level integrity is the source's
responsibility. The Extension Manager retains its pre-existing manifest and
artifact checks as part of extension activation; the plan executor does not
add a second package checksum policy.

Package Source content is not authority. A manifest, plan, package, or Skill
cannot grant host capabilities, execution permission, or activation rights.
Core's existing configuration, ActionPolicy, extension grants, ABI validation,
and lifecycle boundaries remain authoritative.

## Embedded local source

Core always constructs source `local` beneath
`paths.data_dir/package_sources.local_directory` (default
`packages/local`). It operates offline and can bootstrap Skills or extension
artifacts, including another Package Source.

Each package has a JSON manifest in that directory:

```json
{
  "version": 1,
  "id": "runtime-inspection",
  "package_version": "1",
  "kind": "skill",
  "name": "Runtime inspection",
  "description": "Inspect the local runtime",
  "artifact_path": "runtime-inspection-1.zip",
  "artifact_sha256": "<sha256>",
  "artifact_media_type": "application/vnd.neondoll.skill+zip",
  "dependencies": [],
  "proposal": false
}
```

Skill ZIP entries are rooted at `SKILL.md`. Core rejects traversal, symlinks,
duplicates, invalid Skill metadata, count/size excesses, and integrity
mismatches. Installation stages outside the active catalog, validates first,
then atomically renames and refreshes the M4.1 catalog. A failed update restores
the previous installation. Installed Skills remain usable if every external
Package Source is unavailable.

An extension package additionally supplies `extension_manifest`. Core replaces
the proposed artifact path with the verified acquired artifact and uses the
existing Extension Manager `Install` and `Enable` operations. Requested host
capabilities still require explicit Core grants. Package-authored `granted`
entries are discarded during handoff; accepting an installation plan is not a
capability grant.

## WASM Package Source ABI v1

Package Source modules run in the existing Wazero runtime and use the common
`extension_host` capability imports. They do not receive filesystem, network,
secret, or state access unless the existing manifest grants the corresponding
host capability.

Required exports:

- `package_source_abi_version() -> i32` (returns `1`)
- `package_source_event_buf() -> i32`
- `package_source_event_buf_size() -> i32`
- `package_source_init() -> i32`
- `package_source_poll() -> i32`
- `package_source_handle_event(pointer, length) -> i32`

`package_source_shutdown()` is optional. Host requests use the existing atomic
event envelope with types `package_source.search`, `package_source.plan`,
`package_source.acquire`, `package_source.workflow.request`,
`package_source.workflow.status`, `package_source.workflow.cancel`, and
`package_source.proposals.ack`. Guest responses emitted through `outgoing` carry the
same `request_id` and the corresponding `.result`, `artifact.chunk`, or
`artifact.complete` event type. Artifact chunks are base64 encoded. Core's
assigned extension instance ID is the trusted Source ID; guest-supplied Source
IDs are replaced.

`package_source_poll` may emit `package_source.proposal` without an active user
Interaction. Core persists the stable proposal ID, source cursor, plan, state,
and execution report together. Repeated polls and restart recovery do not
duplicate a proposal. Cursors are opaque source values; Core never compares or
orders proposal IDs. The WASM adapter captures proposals emitted during every
guest call, durably retains them, and releases them only after Core has
persisted and acknowledged their stable IDs. Acceptance is explicit; polling never installs or
activates code by itself. Before execution, acceptance is durably marked
`executing`; a crash across an external installation side effect therefore
recovers as an explicit ambiguous/in-progress proposal rather than blindly
executing the plan twice.

Multi-item execution reports every attempted item. If a later item fails,
`completed` is false and `partial` is true when an earlier item installed.
Progress is persisted after each completed item for proposal-driven plans.
There is no cross-package transaction or generic rollback: after a crash, an
`executing` proposal and its durable item report require explicit recovery
rather than blind replay.

The deterministic ABI fixture is independently compiled with TinyGo 0.39:

```sh
tinygo build -target=wasi -opt=z -no-debug \
  -o Core/Extension/testdata/package_source.wasm \
  Core/Extension/testdata/package_source_guest/main.go
```

## Current limits

- The local source performs exact-version dependency resolution only. Rich
  version ranges and source-specific policy remain source-owned.
- Core does not provide marketplace, payment, procurement, publishing, or
  automatic background update execution.
- Proposals are durably recorded but require an explicit caller to accept or
  reject them. They are never treated as permission decisions.
- WASM acquisition is bounded by the existing 64 KiB event and 64-emission
  atomic-call limits. Larger source artifacts require a future bounded
  multi-call cursor protocol; Core does not silently truncate them.
- Skill updates are atomic. The current Extension Manager supports new managed
  installations; in-place extension upgrade/rollback remains deferred rather
  than being emulated by a second installer.
