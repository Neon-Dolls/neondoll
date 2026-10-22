# Core 5 Interaction Session foundation

Core owns Interaction Session lifecycle and durable owner/conversation mapping
through `Core/Session`. A session belongs to one authenticated Body identity or
one Core-assigned Gateway instance identity. Transport addresses and
caller-claimed identity fields are not session owners.

The transport-independent interface for Gateway work is:

```go
type Service interface {
	Create(context.Context, CreateRequest) (*InteractionSession, error)
	Lookup(context.Context, ID) (*InteractionSession, error)
	Resume(context.Context, ResumeRequest) (*InteractionSession, error)
	Close(context.Context, ID) (*InteractionSession, error)
	ListActive(context.Context, *ActiveFilter) ([]*InteractionSession, error)
}
```

`CreateRequest` contains the owning Body or Gateway instance and an optional
opaque external conversation reference. `ResumeRequest` accepts either a Core
session ID or an owner plus external conversation reference, allowing a Gateway
to recover Core's persisted mapping after restart. `ListActive` accepts an
optional owner filter.

Sessions have `active` and `closed` lifecycle states. Close is terminal. A
Session Restart closes the old session and creates a new session, as specified
by the canonical session architecture; `Resume` never reopens a closed session.
Only one active session may map a particular non-empty owner/conversation pair.
A Body or Gateway may otherwise own multiple simultaneous sessions.

The SQLite-backed implementation persists sessions in the existing Core
database. The service keeps no transcript, model/provider context, or other
continuity-bearing state in memory. Reconstructing active sessions after a Core
restart is therefore a normal `ListActive` or `Resume` against reopened Core
storage.

This milestone intentionally does not add Gateway transport integration,
context construction, inference routing, Body resolution, compaction, or model
context lifecycle.

## Runtime integration

`Core/Runtime.Services` constructs the session manager over the production
Core persistence store during `neondolld` startup. Core components access it
through `Services.Sessions()`, whose return type is the transport-independent
`session.Service` interface above. Startup verifies that persisted active
sessions can be listed before the service is reported ready.

The runtime service container owns no socket, goroutine, model context, or
database handle, so it adds no shutdown ordering. The daemon continues to own
and close the shared persistence store. After restart, constructing a new
runtime service container over the reopened store makes existing active
sessions immediately available; closed sessions remain closed.

Rin's future Gateway host needs only a `session.Service` supplied from
`runtime.Services.Sessions()`. It must supply its Core-assigned Gateway instance
identity and opaque platform conversation reference to `Create` or `Resume`;
it does not need persistence access and must not infer owner authority from a
platform sender identity.

## Interaction routing

`Core/Interaction.Router` is the internal session-aware interaction entry point.
Core components obtain one with
`runtime.Services.InteractionRouter(handler)`. A trusted ingress passes a Core
session ID, the already-authenticated Body or Core-assigned Gateway instance
identity, and the inbound event. The router resolves the persisted session,
compares the exact owner identity, atomically rechecks that the session remains
active, updates its activity timestamp, and then passes both the resolved
session and event to the handler.

Unknown, wrong-owner, and closed-session routes return distinct errors and do
not invoke the interaction handler. The router holds no mutable model context
or global dispatch lock, so independent sessions remain isolated and may enter
their handlers concurrently.

The existing Doll Link event envelope does not contain a Core Interaction
Session ID. Authenticated ingress must therefore select a Core session outside
that envelope and call the internal router with its trusted owner identity;
event fields such as `source` are never accepted as identity proof.

## Authenticated Body ingress

The production Doll Link WebSocket now supplies its connection-bound BodyID to
`Core/Interaction.BodyIngress` after authentication. If a session-aware handler
has no authenticated BodyID, event ingress fails closed. Handlers that do not
implement authenticated ingress retain the legacy Core 4 callback behavior.

Because the canonical Doll Link event envelope has no Core session selector,
each authenticated Body currently maps to one implicit persisted conversation,
`doll-link/default`. The mapping is scoped to the authenticated Body owner, so
forged event `source`, user, channel, or Doll fields cannot select another
Body's session. Reconnecting as the same authenticated Body resumes the same
active session; independently authenticated Bodies always receive independent
sessions.

Closing the implicit session is terminal for that session. An interaction that
has already passed router admission continues on the resolved immutable session
snapshot; closing a session does not cancel already-admitted handler work. If
closure wins before router admission, that interaction is rejected. A later
Body interaction creates a fresh session for the implicit conversation, which
implements the canonical close-plus-create Session Restart semantics.

## Doll Link session selection

Authenticated Bodies can manage their Core-owned Interaction Sessions with the
integer-versioned session-control protocol. Requests use the common shape
`{"version":1,"type":"session.create|session.list|session.select|session.close","id":"...","payload":{...}}`.
Create accepts an optional `external_conversation_ref`; select and close require
`session_id`; list has an empty payload. The Core replies with
`session.result`, a Core-generated message ID, the request ID in
`correlation_id`, and a payload containing the operation, `ok` or `error`
status, and any session data or deterministic error code. Malformed requests,
unauthenticated requests, and missing handlers receive the same correlated
response whenever the transport can recover a string request ID.

All operations are scoped to the Body identity authenticated on that WebSocket.
List returns only that Body's active sessions. Unknown, foreign, and closed
session IDs all return `session_unavailable`, preventing ownership disclosure.
Unsupported versions return `unsupported_version` and never change selection.

Selection is stored only on the WebSocket connection and is discarded at
disconnect. Without a selection, events retain the implicit
`doll-link/default` behavior. Closing the selected session clears selection, so
later events again use the implicit default. Reconnecting never restores an
old explicit selection.

Request IDs are scoped to one connection. The transport caches the 128 most
recent responses and returns the original response for a duplicate ID without
reapplying the operation. Reusing an ID with a different operation or payload
returns `request_id_conflict` and does not mutate selection. After reconnect, a client must list current sessions
instead of replaying an uncertain create request. This bounds retry state while
making live-connection retries safe.

An authenticated Body may override the connection default for one interaction
with a compact, versioned wrapper around the unchanged event:

```json
{
  "version": 1,
  "type": "interaction",
  "session_id": "core-assigned-session-id",
  "event": { "id": "event-id", "type": "message", "payload": {} }
}
```

The inner event ID remains the response correlation ID. The Core resolves the
selector against the connection's authenticated Body identity and routes it
through the same Interaction Router used by default-selected events. Unknown,
closed, and foreign selectors produce the same generic correlated error and
never fall back to either the connection default or the implicit session.

An override does not mutate connection selection. Concurrent overrides can be
admitted independently while the transport retains one read-loop owner and
serialized writes. Their workers are scoped to the connection and canceled and
joined on disconnect. Each connection admits at most 16 explicit interactions
by default; saturation returns an immediate correlated `session interaction
unavailable` error rather than blocking the read loop. Capacity is released
when work completes or is canceled. Legacy event envelopes are unchanged and
continue to use the connection default, or `doll-link/default` when no default
is selected.
