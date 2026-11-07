# Core 6 Skill and DollCard contracts

Core 6 M1.3 defines contracts only. The production `DollCard.Encode` and
`DollCard.Decode` paths remain version 1 and are deliberately unchanged.

## Portable Agent Skills

`Core/Skill` uses a standard Agent Skills directory rooted at `SKILL.md`.
NeonDoll does not replace Agent Skills frontmatter or directory semantics.
The Core catalog descriptor adds only portable orchestration metadata: stable
ID and revision, concise name and description, relative installation
directory, dependencies, compatibility requirements, and desired activation.

A portable Skill package contains every file below that root, with relative
path, exact bytes, and SHA-256 integrity. Scripts, references, assets, and
unknown future Agent Skills resources are retained without interpretation.
Paths must be canonical relative slash paths; absolute paths, traversal,
backslashes, duplicate files, duplicate Skill IDs/directories, malformed
metadata, and integrity failures are rejected. Transfer validation is bounded
by Skill, file, and byte counts.

Installation and activation are separate. An imported Skill remains installed
when dependencies are missing, but resolves to `dormant` with explicit missing
dependencies. It is never discarded merely because a runtime, Plugin,
capability, credential, or permission is unavailable. Import does not consult
the original Package Source.

Skills are knowledge, not execution grants. `SKILL.md` instructions, scripts,
and fields such as `allowed-tools` remain untrusted Skill content and cannot
modify ActionPolicy, Body Guard, host capabilities, or external consent.

## Selective loading

The `Catalog` contract separates concise `Discover` results from explicit
`Load` of one selected revision and requested resources. This prevents a Skill
catalog from becoming automatic context injection. The same contract supports
Interaction, Orient, Plan, and Reflection. `SelectionRecord` retains only the
task reference, selected revision, considered alternatives, a short explicit
summary, and observable outcome; it is not a hidden chain-of-thought journal.
M1.3 does not implement discovery, selection, context loading, or execution.

## DollCard version 2 index

`DollCard.IndexV2` is the proposed integer format revision for `card.json`.
Every portable section has an ID, kind, schema version, required flag,
an explicit empty marker, compatibility requirements, and complete file list
with relative path, declared size, and SHA-256. A present section with
`"empty": true` is intentionally empty and has no entries. It is not
equivalent to an omitted section; `"empty": false` requires at least one
entry. The index explicitly represents:

- identity;
- Soul and the latest active Self;
- Avatar and Preferences;
- existing Doll-owned Secrets;
- the Skill catalog and complete Skill directory payloads;
- MemoryRecord v1 JSONL segments exported by the selected Memory Provider;
- Peer identities, Primary Peer lifecycle, and Doll-authored relationship
  descriptions;
- authority decisions, delegations, revocations, and one-time consumption
  history;
- Drives, Goals, and durable Intentions;
- portable extension data; and
- legacy Owner and Auth data needed for explicit migration.

All known portable areas are required index sections, even when empty. This
prevents a syntactically valid v2 card from appearing compatible after silently
omitting an area. Legacy Owner data is migration evidence, not an automatic
Primary Peer designation. Legacy Auth data is migration evidence, not an
active credential or authority grant.

Representative paths include `skills/catalog.json`, `skills/<dir>/SKILL.md`,
`memories/*.jsonl`, `peers/identities.json`,
`peers/primary-events.json`, `peers/descriptions/*.json`, and separate
authority decision/delegation/consumption files. Paths are index data rather
than hard-coded production extraction behavior.

Version 2 validation rejects traversal, duplicate/conflicting archive paths,
duplicate known sections, malformed hashes, unsupported schema versions, and
declared transfer bounds. A future archive reader must reject duplicate ZIP
entries before extraction and verify actual bytes against each indexed size
and hash. M1.3 supplies index and entry validation but does not replace the v1
archive pipeline.

## Compatibility and import reporting

- Version 1 remains a legacy input for the existing reader. Missing v2
  authority is reported as `unknown/unrepresented`, never allow or prohibit.
- `IndexV2`, `SectionIndex`, and `ArchiveEntry` retain unknown fields as opaque
  `json.RawMessage` values. Decode/encode preserves their JSON values,
  including nested objects and arrays, without assigning semantics. JSON
  whitespace and object-key ordering may change; the semantic value does not.
  Duplicate keys at any nesting level are rejected because choosing one value
  would make preservation ambiguous.
- Unknown optional sections remain compatible when their complete index and
  payload can be preserved. Unknown required sections are incompatible with
  activation or interpretation, but their metadata, entry references, and
  payloads remain preservable. Unknown data never becomes authority,
  execution permission, or trusted instructions.
- `PreserveIndexedPayloads` is the contract-level payload pass-through helper:
  it verifies every referenced size and digest and returns independent copies
  under identical archive paths. This proves path-and-byte preservation for
  in-memory fixtures without implementing the production v2 ZIP pipeline.
- If an extension field contains invalid or duplicate-key JSON, collides with
  a known field, or an indexed payload is absent or fails integrity, encoding
  or pass-through fails explicitly rather than producing a lossy card.
- Missing dependencies yield an installed, dormant Skill.
- Skill path conflicts and integrity failures are explicit rejection results.
- Unverifiable authority records are retained as
  `requires_verification`; they are never silently activated or discarded.
- Memory import uses the M1.1 outcomes so transformations and rejections remain
  explicit.
- Missing or unusable credentials remain portable but report
  `requires_setup`; they are availability failures, not permission denials.
- Unavailable destination capabilities do not erase Skill contents.

The index contains Doll-owned state only. Provider endpoints, local paths,
extension installations, host credentials, runtime caches, and other
installation configuration remain outside DollCard.

## Known compatibility limitations

The current v1 implementation uses a minimal `{ "version": 1 }` index,
structured `self/self.json`, legacy `owner/owner.md`, Core 1 memory items, and
does not encode complete Skill directories, Secrets, Peer state, or authority.
Changing that production pipeline requires a later migration PR with real
archive round-trip and no-silent-loss tests. M1.3 does not claim v2 cards can
yet be imported or exported by production Core.

Open decisions for that work include archive streaming implementation,
section/file size defaults, atomic conflict handling, whole-container
protection, and the exact trusted verification process for imported Peer and
authority provenance.
