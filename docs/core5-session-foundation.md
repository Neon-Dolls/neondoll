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

The existing wire contract cannot explicitly select among multiple concurrent
sessions owned by one Body. Supporting that requires an independently specified
authenticated ingress/session-selection contract; this implementation does not
infer selection from untrusted event fields or add an ad hoc protocol field.
