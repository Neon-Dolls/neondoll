# Production Remote Body registration

Core wires `body.RemoteRegistrar` into the production Doll Link WebSocket
server. The registrar owns the relationship between a stable Body identity, a
current connection generation, the Core Body Registry, and the Body's latest
canonical capability advertisement.

## Authentication integration boundary

`ws.Config.AuthenticateExecution` is the only identity trust boundary. A
future production implementation must authenticate the connection by a
mechanism defined outside the transport and return the verified stable BodyID.
It must not derive identity from an overlay IP, remote socket address, Doll
Network membership, or a message `body_id` field.

The WebSocket server assigns an opaque session ID after authentication and
passes both values to `ExecutionSessionLifecycle.SessionConnected`. The
production `RemoteRegistrar` then independently requires an active,
non-revoked persisted Doll Network membership for that BodyID. Authentication
proves identity; membership determines eligibility. Neither substitutes for
the other.

`cmd/neondolld` installs the registrar but deliberately supplies no
`AuthenticateExecution` callback. Therefore production connections cannot
create an execution session, register a RemoteBody, advertise capabilities,
or execute remotely until a real authenticator is configured.

## Capability and connection lifecycle

An authenticated Body sends the canonical Body Protocol v1
`body.capabilities` message. The message's `body_id` must match the
authenticated session identity. The complete advertisement atomically
replaces that Body's prior capability set; advertisements do not add Guard
authority.

The first authenticated connection creates one stable RemoteBody Registry
entry. Reconnection reuses it. A newer connection generation immediately
makes the previous advertisement unavailable, and only the newer generation
may update it. Disconnect marks the last advertised capabilities unavailable
without deleting Body identity or affecting other Bodies. Core restart creates
a fresh runtime Registry and reconstructs the same Body when it reconnects
against its durable membership.

Execution continues through `Guard.ExecuteContext`, `RemoteBody`, and the
existing Doll Link execution session. The registrar rechecks active membership
and current connection state immediately before transport execution.

## Remaining production blockers

- No production Doll Link authenticator or credential scheme is defined or
  configured. This change intentionally does not invent one.
- The reference Body command does not yet run the long-lived authenticated
  Doll Link client and capability executor service.
- The canonical `body.hello` / `core.hello` version negotiation and
  `body.ready` transition are not yet implemented by the current WebSocket
  transport. Registration handles the canonical repeatable
  `body.capabilities` message but must not be described as full Body Protocol
  connection negotiation.
- There is no production revocation service that both persists membership
  revocation and actively closes its current Doll Link connection. Execution
  rechecks persisted membership and fails closed after revocation, but prompt
  session termination still requires that lifecycle wiring.

These limitations prevent production remote execution from being enabled in
the daemon today. The trusted authentication callbacks used by tests are
explicit fixtures and are not compiled into a production fallback.
