# Gateway structured Session event ABI

Core owns Gateway instance identity and lifecycle. A managed Gateway is
registered with `Core/Gateway.Host` under its configured instance ID; incoming
payloads cannot select or override that identity.

The host ABI has two integer versions:

- version `0` preserves legacy request/response text interactions through
  `Ingress.HandleLegacy`;
- version `1` adds `Ingress.HandleSessionEvent` and outbound
  `OutboundEnvelope` delivery.

`OutboundEnvelope` is JSON-serializable and contains the ABI version plus the
canonical `SessionEvent.Delivery`. WASM or native Gateway adapters use the same
envelope. Delivery IDs are stable across retries and must be used for adapter
deduplication. Media remains represented by references in SessionEvent parts;
the ABI does not define another file-transfer mechanism.

The Core-bound `Ingress` supplies the Gateway identity out of band. Adapters
supply only platform-verified sender metadata (platform and external subject
ID). Core resolves that tuple through its configured ExternalIdentity resolver,
then independently checks Session ownership, participant binding, and Owner
authority. Payload claims do not grant authority.

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
