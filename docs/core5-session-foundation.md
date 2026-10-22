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
