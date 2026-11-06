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

Reconciliation is deterministic:

1. Validate and sort by timestamp, tie-breaker, and decision ID.
2. Apply valid revocations. An ordinary Peer may revoke only its own ordinary
   decision; it cannot revoke a Primary Peer decision.
3. Ignore expired, future, revoked, or inapplicable records for the queried
   action without deleting them from portable history.
4. The latest applicable Primary Peer decision wins over every ordinary Peer
   decision, regardless of cross-issuer timestamp.
5. Otherwise the latest applicable ordinary decision wins.
6. With no applicable prohibition, autonomy defaults to allow.

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
- Trusted import verification of historical issuer role and delegation needs a
  later persistence/DollCard integration design.
- Ordinary-Peer delegation lifecycle and relationship-description revision
  storage are later milestones; this PR does not invent either subsystem.
- Equal-timestamp tie-breaker production generation must be standardized when
  persisted authority writes are introduced.
