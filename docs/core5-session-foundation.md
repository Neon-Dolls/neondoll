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
