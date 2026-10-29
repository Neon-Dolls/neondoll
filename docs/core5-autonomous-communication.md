# Autonomous outbound communication

Core exposes `core.communication__send_text` to the shared cognitive execution
engine. Its provider-neutral arguments are:

```json
{"recipient_principal_id":"owner-1","text":"I wanted to tell you something."}
```

Recipient discovery is available through `core.communication__list_peers`.
It takes an empty object and returns a bounded, on-demand projection such as:

```json
{
  "peer_count": 1,
  "peers": [{
    "principal_id": "owner-1",
    "authority": "owner",
    "platforms": ["telegram"],
    "identity_count": 1,
    "relationship_available": true,
    "active_gateway_route": true,
    "eligible_session_count": 1,
    "known_session_count": 1
  }]
}
```

The trusted source is the configured ExternalIdentity principal directory,
joined with durable, provenance-checked Session participant bindings. Multiple
external identities collapse into one principal. Discovery exposes platform
names only; external subject IDs, Gateway instance IDs, addresses, tokens, and
other transport credentials are never model-visible. Authority is the
classification already assigned by trusted Core configuration; Session
membership does not promote it.

`relationship_available` means a durable participant relationship exists,
including one whose Session is closed. `active_gateway_route` means the same
selector used by `send_text` found at least one active Gateway-owned Session.
It does not mean a person is online or has read anything, and the Gateway may
still be temporarily unavailable when delivery is attempted. Known principals
remain listed when no eligible route exists. Removed mappings disappear on
configuration reload/restart reconciliation; reassigned identity provenance
no longer grants the old principal a route.

The detailed peer list is not injected into every model context. The concise
tool description tells cognition that discovery is available, and all shared
cognition paths can request the current trusted projection when needed. This
keeps Context Builder inputs bounded while allowing a `list_peers` followed by
`send_text` in the same multi-step execution.

The recipient is a Core principal—not a Gateway ID, Telegram/chat ID,
credential, or platform address. Trusted ingress records the resolved external
identity as provenance for its durable Session participant binding. Core then
selects the most recently observed active Gateway Session in which that
principal is a participant. Equal timestamps use Session ID ordering. The
selected Session is revalidated by SessionEvent delivery.

External identity mappings are immutable runtime configuration. At daemon
startup Core reconciles persisted binding provenance with that configuration.
Removed mappings lose their derived binding; reassigned mappings lose the old
principal binding and grant the new principal nothing until trusted ingress is
observed. Another valid identity for the same principal, and unrelated
participants in a multi-party Session, remain intact. Communication trusts the
reconciled binding and does not repeat external authentication on each send.
Participant rows created before provenance support are removed from outbound
targeting during the first reconciliation because their mapping origin cannot
be proven. Their Sessions and conversation references remain intact; the next
trusted inbound event restores the binding.

The action passes through the existing action-wide policy before routing.
`deny` is terminal and `require_approval` creates the existing durable
PermissionRequest containing the exact recipient and text arguments. Approval
resumes the same tool call; it cannot substitute another recipient.

Messages use the canonical SessionEvent message envelope and durable delivery
store. A stable delivery ID is assigned before the Gateway attempt:

- `accepted`: the configured Gateway adapter acknowledged delivery;
- `pending`: persistence succeeded but a transient transport failure remains
  queued for replay;
- `failed`: delivery is terminally unavailable;
- no delivery ID: validation, identity resolution, or persistence failed.

The tool reports success only for `accepted`. A pending durable delivery is a
failure outcome with code `delivery_pending`, its delivery ID as the execution
ID, and this structured result:

```json
{
  "delivery_id": "stable-id",
  "delivery_status": "pending",
  "durably_queued": true,
  "retry_owner": "core_session_event_replay",
  "resend_required": false
}
```

Existing replay retries that same delivery ID and Gateway deduplication
prevents restart duplicates. Cognition should not submit another send merely
because the transport is temporarily unavailable. Gateway acceptance does not
claim that a human received or read the message.

SessionEvent correlation preserves Cognitive Run, Turn, tool-call, and
Intention IDs. Pulse, intention wakes, direct cognition, and Interaction all
share the same ToolExecutor, so no separate messaging loop exists.

Unsupported in this increment: arbitrary new recipients, model-selected
Gateway/platform addresses, creating conversations without an existing active
Session, media composition, read receipts, and human-read confirmation.
