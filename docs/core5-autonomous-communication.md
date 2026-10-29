# Autonomous outbound communication

Core exposes `core.communication__send_text` to the shared cognitive execution
engine. Its provider-neutral arguments are:

```json
{"recipient_principal_id":"owner-1","text":"I wanted to tell you something."}
```

The recipient is a Core principal from trusted external-identity
configuration—not a Gateway ID, Telegram/chat ID, credential, or platform
address. Core selects the most recently observed active Gateway Session in
which that principal is a durable participant. Equal timestamps use Session ID
ordering. The selected Session is revalidated by SessionEvent delivery.

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

The tool reports success only for `accepted`. A pending durable delivery is
reported as `delivery_pending`, including its delivery ID as the execution ID,
so cognition does not mistake queuing for confirmed Gateway delivery. Existing
replay retries that same delivery ID and Gateway deduplication prevents restart
duplicates. This acknowledgement is the Gateway contract boundary; it does not
claim that a human read the message.

SessionEvent correlation preserves Cognitive Run, Turn, tool-call, and
Intention IDs. Pulse, intention wakes, direct cognition, and Interaction all
share the same ToolExecutor, so no separate messaging loop exists.

Unsupported in this increment: arbitrary new recipients, model-selected
Gateway/platform addresses, creating conversations without an existing active
Session, media composition, read receipts, and human-read confirmation.
