# Body Enrollment and Authentication v1

The canonical protocol is defined in `dividebyzero/NeonDoll-Concepts` by
`body-enrollment-authentication-v1.md` and `body-enrollment-wire-v1.md`.

## Phase 1 enrollment authority

Core now owns a durable Ed25519 signing identity independent of its WireGuard
identity. The SQLite enrollment store persists pending requests, Body signing
key bindings, approved permissions, membership references, attempt counters,
and signed receipts. Completion commits the credential, existing Doll Network
membership row, receipt, pending-state transition, and invitation consumption
in one SQLite transaction.

Body-initiated requests are accepted at:

- `POST /v1/body/enrollment/begin`
- `POST /v1/body/enrollment/result`

Core-initiated invitations are redeemed at:

- `POST /v1/body/enrollment/redeem`

The daemon registers these handlers on the same HTTP server that owns Doll
Link. Deployments must expose bootstrap endpoints through HTTPS as required by
the wire specification; the handlers do not treat transport reachability or a
claimed BodyID as authority.

Owner approval is represented by `enrollment.OwnerAuthorizer`. It defaults to
deny, so merely knowing an approval code cannot approve an enrollment. A
production owner adapter and its CLI/API endpoints are Phase 3 work.

## Security boundaries

- BodyID is allocated by Core only at atomic enrollment completion.
- Body and WireGuard public keys are separately validated and bound.
- Invitation secrets and approval codes are single-use enrollment material,
  never Doll Link credentials.
- Approval codes are stored only as HMAC verifiers keyed by the Core signing
  secret. Invitation secrets retain their existing high-entropy hash storage.
- Core responses and receipts use the exact length-prefixed Ed25519 transcript;
  serialized JSON is never signed.
- The Core database is restricted to owner-only filesystem permissions.
- The legacy `/v1/pair` exchange remains unchanged and is not silently treated
  as authenticated Body enrollment.

## Remaining phases

Phase 2 installs the `body.auth.hello` through `body.auth.accept` handshake and
uses the persisted credential binding to activate `RemoteRegistrar`. Until
then, production Doll Link execution remains fail-closed because the daemon
does not configure the trusted execution authenticator.

Phase 3 adds authorized owner invitation/approve/deny/revoke commands and live
revocation invalidation. Phase 4 adds the persistent reference Body flows and
complete end-to-end conformance coverage.
