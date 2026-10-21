# Core 4 conformance and acceptance

This report audits the repository at the completion of the Core 4 M1–M9
implementation series. The normative sources are the Core 4 build plan,
`body-contract.md`, `body-protocol.md`, and `doll-link.md` in
`dividebyzero/NeonDoll-Concepts`.

## Recommendation

The implementation is an **integration-complete development baseline**, but it
is **not production-acceptable for remote capability execution**. The network,
Relay, Doll Link execution transport, authorization boundaries, reconnection,
migration, and multi-Body behavior have reproducible test coverage. Production
Doll Link authentication is intentionally absent, so the daemon cannot bind a
remote connection to a Body identity or safely enable the execution path.

This is a security boundary, not a test failure. The transport fails closed
when no authenticator is installed. Tests use explicit trusted identity
fixtures; WireGuard membership, overlay addresses, and message `body_id` fields
are never treated as application-layer authentication.

## Requirement coverage

| Milestone | Evidence | Assessment |
| --- | --- | --- |
| M1 — network identity and membership | `Core/Network`, `Core/Persistence`, `Core/Runtime`; network, portable-state, reconstruction, and persistence tests | Verified. Stable Core/network/peer identities, WG keys, IPv6 allocation, membership lifecycle, and reconstruction are covered. Membership endpoints are excluded from portable authority state. |
| M2 — pairing | `Core/Pairing`, `Body/pairing.go`, `test/pairing_rendezvous_test.go` | Verified for invitation validation, one-time consumption, durable membership, key validation, duplicate/revoked identities, and real HTTP rendezvous. |
| M3 — direct overlay and Doll Link | `test/m3_e2e_test.go`, `test/body_m3_e2e_test.go` | Verified with a real userspace WireGuard path and real Doll Link traffic. Authorization remains a separate Core boundary. |
| M4 — Relay | `Core/Relay`, `test/m4_6_e2e_test.go`, `test/m47_isolation_e2e_test.go`, authenticated Relay tests | Verified for one-Relay/one-Core topology, route ownership, bounds, liveness, cleanup, opaque forwarding, and isolation. |
| M5 — hostile-network fallback and remote execution foundation | `test/m54_wg_over_body_wss_integration_test.go`, `test/m56a_wss_tls_integration_test.go`, `test/m56b_hostile_network_conformance_test.go`, `Core/Body/remote_e2e_test.go` | Verified as integration components: WG over Body WSS, TLS WSS, Guard-to-RemoteBody-to-real-WebSocket-to-Dispatcher execution, local permission, terminal outcomes, and cleanup. Production application-layer authentication remains blocked as described below. |
| M6 — reconnect and path mobility | `test/m61_loss_detection_test.go`, `test/m62_recovery_test.go`, `test/m63_epoch_recovery_test.go`, `test/m64_probe_test.go`, `test/m65_cutover_test.go`, `test/m66_mobility_test.go`, `test/m67_supervisor_test.go`, `test/m68_supervisor_test.go` | Verified for detection, direct/Relay/WSS recovery, epoch supersession, rollback, probing, mobility return, retry, and shutdown. |
| M7 — Core reconstruction and migration | `Core/Runtime/reconstruct_test.go`, `test/m47_reconstruction_e2e_test.go`, `test/m74_body_reconnect_e2e_test.go` | Verified. Persisted identity and membership reconstruct; a Body reconnects without re-pairing or a persisted endpoint; stale sessions and pending execution are cleaned up; execution succeeds on reconstructed Core B. |
| M8 — multiple Bodies | `Core/Body/multi_remote_e2e_test.go` | Verified with three real WebSocket clients. Explicit authenticated routing, overlapping execution IDs, independent Core/Body permissions, cross-Body forgery rejection, saturation, cancellation, disconnect/reconnect, Events/Actions, and Local Body coexistence are covered. |
| M9 — conformance and final acceptance | This report plus the complete commands below | Development baseline accepted. Production remote execution rejected until the authentication and production-wiring blockers are closed. |

## Protocol and security findings

- `DollLink/Execution` uses the canonical `execution.request` and
  `execution.result` envelopes, sender directions, message-ID correlation,
  execution-ID correlation, Body identity correlation, JSON-object payloads,
  and terminal statuses.
- Execution wire types do not introduce their own version field or advertise a
  protocol range. Doll Link negotiation remains the owner of integer protocol
  version compatibility.
- Body Protocol v1 does not define a structured wire error-detail field even
  though the Body Contract describes structured errors semantically. The
  implementation preserves terminal status and does not invent a field.
- Core authorization (`Guard`) and Body-local permission (`Dispatcher`) are
  independent and fail closed. A Core denial does not transmit; a Body denial
  does not invoke the capability.
- Results are accepted only by the session authenticated for the target Body
  and only while their request is pending. Mismatched Body, Doll, execution,
  or correlation identities, unsolicited results, cross-Body forgeries, late
  results, and replayed terminal results are rejected.
- Each WebSocket has one read-loop owner and serialized writes. Disconnect and
  replacement close the old execution session and release pending waiters.
  Body-side execution is bounded and excess work receives a correlated
  `unavailable` result.

## Remaining production blockers

1. **No production Doll Link authentication mechanism.** `ws.Config` accepts
   an `AuthenticateExecution` callback, but `cmd/neondolld` deliberately does
   not install one. The canonical specifications do not yet provide a
   credential or handshake that securely proves the paired Body identity at
   this layer. Consequently production connections remain ineligible for
   execution, as required by the fail-closed boundary.
2. **No production Body-side authenticated execution service.** The reference
   `neondoll-body` command initializes and pairs durable identity state, but it
   does not run the long-lived network/Doll Link client with an authenticated
   `ExecutionRequestHandler`.
3. **No production remote-Body registration/capability-advertisement wiring.**
   `RemoteBody`, Registry integration, Guard integration, and WebSocket
   execution sessions exist, but the daemon creates only the Local Body. There
   is no authenticated production source from which to register a remote Body
   and its advertised capabilities.
4. **Revocation is durable at the Doll Network membership layer but is not
   wired to active Doll Link execution sessions.** A future authentication
   owner must reject revoked identities during authentication and terminate an
   already-authenticated session when revocation occurs. Merely revoking WG
   membership is not application-layer session revocation.
5. **No single production-system acceptance run spans every phase.** Real
   transports are exercised, but authentication-dependent execution uses a
   trusted fixture and the expensive migration, Relay fallback, and multi-Body
   scenarios are deliberately separate tests. Combining them would not remove
   blockers 1–4.

These blockers prevent a claim that a deployed Core 4 system can safely accept
and execute remote Body requests. They do not justify deriving identity from
membership, overlay IP, or unverified protocol fields.

## Deferred, not Core 4 defects

- Core 5 Interaction Sessions and semantic Body Resolver behavior.
- Semantic or automatic Body selection; Core 4 routes to an explicit Body ID.
- A new pairing credential scheme or independently designed Doll Link
  authentication protocol.
- A cancellation wire message; Body Protocol v1 defines none, so Core
  cancellation removes the local waiter while disconnect cancels Body work.
- A structured execution error wire shape until the canonical protocol defines
  one.

## Reproducible validation

Validation on 2026-10-21 passed for build, vet, the full unit suite, tagged
integration and E2E suites, the focused Core/network/Doll Link race suite, and
the complete tagged integration and E2E suites under the race detector.
Targeted race stress also passed for 20 multi-Body cycles, 20 execution-session
and WebSocket cycles, 20 Relay replacement/rate-limit/reconnect cycles, and 10
Core migration/reconstruction cycles.

The multi-Body stress run found and fixed one lifecycle defect during this
audit: a completed Body capability retained its concurrency slot until after
writing the terminal result. Core could receive that result and issue another
request before the slot was released, causing a spurious `unavailable`
response. Slots now bound capability execution and are released before result
delivery; saturation while capability work is genuinely active is unchanged.

Run from the repository root:

```sh
gofmt -w $(find . -name '*.go' -not -path './vendor/*')
go vet ./...
go build ./...
go test -short -count=1 ./...
go test -tags=integration -count=1 ./test/...
go test -tags=e2e -count=1 -timeout 300s ./test/...
go test -race -count=1 ./Core/Body ./Core/Network ./Core/Pairing ./Core/Relay ./Core/Runtime ./Core/WireGuard ./DollLink/Execution ./DollLink/WebSocket
go test -race -tags=integration -count=1 ./test/...
go test -race -tags=e2e -count=1 -timeout 600s ./test/...
```

Targeted repeatability checks:

```sh
go test -race -count=20 ./DollLink/Execution ./DollLink/WebSocket
go test -race -count=20 ./Core/Body -run 'TestM8|TestRemoteExecutionEndToEnd'
go test -race -tags=integration -count=10 ./test -run 'TestRecoverPath|TestM6|TestCutoverPath'
go test -race -tags=e2e -count=10 -timeout 600s ./test -run 'TestM47|TestM56|TestM74'
```

Some E2E tests use userspace WireGuard/netstack and local TCP/WebSocket
listeners. They require permission to bind local sockets; no physical hardware
or external Relay service is required. The inference E2E tests use the
repository's configured test model and are not evidence for Body networking.
