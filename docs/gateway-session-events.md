# Gateway structured Session event ABI

Core owns Gateway instance identity and lifecycle. A managed Gateway is
registered with `Core/Gateway.Host` under its configured instance ID; incoming
payloads cannot select or override that identity.

The Core adapter contract has two integer versions:

- version `0` preserves legacy request/response text interactions through
  `Ingress.HandleLegacy`;
- version `1` adds `Ingress.HandleSessionEvent` and outbound
  `OutboundEnvelope` delivery.

This version is distinct from Telegram's Wazero guest ABI version (`2`). Core
does not execute guest memory, project configuration, credentials, HTTPS, or
platform polling. Those remain owned by the existing Telegram Wazero host.

`OutboundEnvelope` deliberately matches Telegram's existing host/guest
`GatewayMessage` event shape: `{"v":0,"type":"session.event","payload":...}`.
The payload is the canonical `SessionEvent.Delivery`, including its stable
delivery ID and targeted principal. A Telegram-side adapter can therefore call
its existing `GatewayRuntime.HandleEvent(contract.Version0, envelope.Type,
string(envelope.Payload))` without another wire protocol. Delivery IDs must be
used for adapter deduplication. Media remains represented by references in
SessionEvent parts; the ABI does not define another file-transfer mechanism.

The Core-bound `Ingress` supplies the Gateway identity out of band. Adapters
supply only platform-verified sender metadata (platform and external subject
ID). Core resolves that tuple through its configured ExternalIdentity resolver,
then independently checks Session ownership, participant binding, and Owner
authority. Payload claims do not grant authority.

Telegram PR #4 emits `GatewayEventEnvelope` values with verified platform
`chat_id` and `sender_id` metadata. Its adapter maps `chat_id` to an opaque
external conversation reference and calls `Ingress.HandlePlatformMessage`.
Core resolves or creates the Session under the adapter's bound Gateway identity
and resolves `sender_id` through trusted ExternalIdentity configuration. A
Telegram ID is never a Core Session ID or an Owner claim.

Structured delivery to a disconnected Gateway stays pending. Registering an
instance replays durable pending deliveries with their original IDs. An
adapter can mark a failure terminal with `SessionEvent.TerminalDeliveryError`;
ordinary adapter errors remain retryable. Version 0 adapters reject structured
delivery terminally while legacy text continues to work.

`Runtime.GatewayRuntime` is the production composition boundary. It owns the
Gateway host, durable SessionEvent service, PermissionRequest manager, and
PermissionBridge for the daemon lifetime. Creating a permission through
`GatewayRuntime.CreatePermission` persists it before presentation. Approval or
denial records the decision only and never resumes or executes an action.

## Telegram follow-up boundary

The Telegram repository requires a separate follow-up adapter (not another
Wazero engine) that implements `gateway.Adapter` around its existing
`host.GatewayRuntime`:

1. `Start` starts the existing WASM runtime and retains the Core-bound
   `gateway.Ingress`.
2. Its existing poll loop converts `message.incoming` envelopes into
   `HandlePlatformMessage` calls using verified `sender_id` and `chat_id`.
3. `Deliver` decodes the Core `OutboundEnvelope` and calls the existing
   `HandleEvent` export path. The guest must add handling for `session.event`
   and Telegram rendering/sending; credentials, redacted configuration, HTTPS
   allowlisting, guest memory, and polling remain unchanged.
4. `Stop` delegates to the existing runtime shutdown.

No Telegram token, configuration projection, HTTP destination policy, or WASM
linear-memory behavior moves into Core.
