# Core 6 Peer identity and authority contracts

Core 6 M1.2 defines portable contracts in `Core/Peer`. It does not replace or
run beside Core 5 authorization. Production policy, permission decisions,
pairing, Gateways, and persisted schemas remain unchanged.

## Peer identity and evidence

A `Peer.ID` is stable across Gateway accounts, devices, sessions, Bodies, and
Core installations. Claims, authenticated transport/account/device evidence,
and a verified Core binding are separate records. A claim or familiar endpoint
never becomes a Peer ID or authority by itself. Account authentication proves
control of that account at a time; it does not prove physical human authorship.
Unknown evidence remains explicitly `unknown` and cannot carry a Peer ID.

Bindings record verification method, provenance, scope, assurance, and
freshness. The deliberately small assurance vocabulary is:

- `none`: no authenticated identity evidence;
- `authenticated_account`: sufficient for configured routine conversation;
- `reverified`: fresh explicit verification required for Primary Peer changes,
  credential changes, and comparably sensitive operations.

These are ordered assurance classes, not trust scores. A caller must validate
scope, freshness, revocation, and minimum assurance. Biometric identity and
universal human-authorship claims are outside this contract.

## Relationships and Primary Peer lifecycle

Ordinary relationships carry stable Peer identity and recognition time but do
not impose a rigid friendship or trust taxonomy. Exactly one Primary Peer may
be active. The ordered portable lifecycle contains establishment, affirmation,
Doll release request, Primary Peer release grant, and Primary-Peer-authorized
transfer. The Doll may request release but cannot grant it or transfer its own
allegiance. Establishment, release, and transfer require re-verification.

Replay orders equal timestamps by the portable `tie_breaker`, then event ID,
and rejects duplicate, second-primary, non-primary release, and non-primary
transfer records. Exceptional recovery for a permanently unavailable or
compromised Primary Peer is intentionally unresolved by the canonical roadmap;
this contract does not create a unilateral reset path.

Doll-authored relationship descriptions are not defined or edited here. A
legacy Owner prose document is preserved as attributed migration source, not
as an externally writable Core 6 relationship description.

## Authority history and reconciliation

Authority decisions have stable IDs, issuer Peer IDs and role-at-issuance,
subject, exact capability/operation/resource scope, effect, timestamp, stable
tie-breaker, provenance, assurance, and optional absolute expiry. Reusable
allowances default to no expiry. One-time approvals require an exact
correlation ID and never become reusable. There is no session-duration grant.

An ordinary Peer decision is effective only when accompanied by a verified,
currently applicable delegation from the Primary Peer for the exact subject,
capability, operation, and resource. Delegations record stable identities,
absolute lifetime/revocation, strong assurance, and provenance. This is the
minimum reconciliation boundary, not a full delegation-management lifecycle.
Authentication or Peer recognition alone never supplies authority. Primary
Peer decisions require no delegation and retain unconditional precedence.

One-time approval consumption is an append-only portable record containing the
approval decision, correlation, stable execution-attempt ID, and consumption
time. Reconciliation returns a pending consumption record on first use. Core
must durably commit that record before dispatching the attempt. Once present,
the approval cannot authorize another attempt—including a replay using the
same attempt ID or a retry using a different one. This fail-closed ordering
avoids duplicate execution across retries, restart, and DollCard migration. A
crash after consumption but before dispatch can strand the approval; resolving
that ambiguous boundary requires operational continuation state and remains
outside this contract-only milestone. Approval-gated queries report
`require_approval` after consumption rather than falling through to autonomy.
Ordinary queries retain the autonomy default.

Reconciliation is deterministic:

1. Validate and sort by timestamp, tie-breaker, and decision ID.
2. Validate delegations and durable one-time consumption records.
3. Apply valid revocations. An ordinary Peer may revoke only its own ordinary
   decision and only under applicable delegation; it cannot revoke a Primary
   Peer decision.
4. Ignore expired, future, revoked, consumed, undelegated, or inapplicable records for the queried
   action without deleting them from portable history.
5. The latest applicable Primary Peer decision wins over every ordinary Peer
   decision, regardless of cross-issuer timestamp.
6. Otherwise the latest applicable delegated ordinary decision wins.
7. With no applicable prohibition, autonomy defaults to allow unless the
   caller explicitly identifies an approval-gated operation.

The reconciliation helper assumes provenance and role-at-issuance were
verified at the trusted import/ingress boundary. Missing credentials,
unavailable Bodies, provider refusals, and absent capabilities are execution
conditions, not authority prohibitions.

## Core 5 terminology inventory and migration

| Existing use | Current meaning | Core 6 mapping |
| --- | --- | --- |
| `DollState.Owner`, `owner/owner.md` | portable distinguished relationship and prose | candidate Primary Peer plus preserved legacy prose; requires explicit re-verification before establishment |
| `OwnerControl` local Unix boundary / `local-admin` | infrastructure administrator and credential holder | remains an installation administrator; never automatically a Peer or Primary Peer |
| `PermissionRequest.OwnerID`, `AuthenticatedOwner` | authenticated decision actor in the Core 5 permission lifecycle | legacy authorization issuer; later migration resolves it to a verified Peer without changing M1.2 production behavior |
| `ExternalIdentity.AuthorityOwner` | configured external principal authority classification | legacy identity binding input; not itself a Primary Peer relationship event |
| `Session.Owner` | Body/Gateway transport instance | remains transport ownership; never a human Peer relationship |
| `ActionPolicy` Owner/Primary-Peer wording | action-wide Core 5 production policy | unchanged until a later migration consumes verified Peer authority history |
| Body Guard authority | per-Body execution authorization | remains an independent execution boundary |
| deployment, secrets, host credentials | infrastructure control | no interpersonal relationship is inferred |

Compatibility is additive in M1.2. No existing JSON field, database schema, API,
or configuration key is renamed. A future migration must preserve stable legacy
issuer references and history, report unverifiable bindings, and must not infer
Primary Peer status from deployment ownership.

## Open questions

- The exceptional recovery process for a permanently unreachable or
  compromised Primary Peer requires architectural approval.
- Trusted import verification of historical issuer role, delegation
  provenance, and consumption history needs a later persistence/DollCard
  integration design.
- Atomic persistence of one-time consumption with production execution
  continuation is deferred; M1.2 deliberately fails closed after consumption.
- Ordinary-Peer delegation management and relationship-description revision
  storage are later milestones; this PR does not invent either subsystem.
- Equal-timestamp tie-breaker production generation must be standardized when
  persisted authority writes are introduced.
