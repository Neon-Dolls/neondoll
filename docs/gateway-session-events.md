# Core 5 extension runtime and Gateway contract

Core owns extension installation identity, lifecycle, capability grants,
Interaction Sessions, participant resolution, permissions, and durable Session
event delivery. Guest-supplied identity or authority fields are never trusted.

## Package structure

- `Core/Extension`: manifest v1, Extension Manager, shared Wazero runtime,
  namespaced durable state, and the Gateway contract adapter.
- `Core/Gateway`: the authenticated in-process Gateway Host from PR #113.
- `Core/Runtime.GatewayRuntime`: durable Session events, permission requests,
  replay, and production Gateway registration.

The runtime supports Gateway contract `gateway` with guest ABI version `2`.
The identifiers `capability` and `package_source` are reserved and rejected as
unsupported; they are not partially implemented.

## Manifest and grants

Each installation declares an extension ID, Core-assigned instance ID,
contract, ABI, artifact path and SHA-256, redacted guest configuration,
requested capabilities, separately granted capabilities, HTTPS allowlist,
scoped secret-file names, and Gateway routing configuration. A request has no
effect unless trusted Core configuration grants it.

Relative artifacts resolve under `paths.data_dir`; absolute paths and paths
which escape that directory are rejected. Relative secrets resolve under
`paths.secrets_dir`; contents never enter the configuration projection. State
lives beneath `paths.data_dir/extensions/state`, namespaced per instance.

There are two explicit installation sources:

- configuration-managed installations are declared in the Core configuration
  and reference an integrity-pinned artifact already located beneath
  `paths.data_dir`;
- locally installed artifacts are integrity-checked at the trusted local source,
  copied to `paths.data_dir/extensions/installed/<instance>/module.wasm`, and
  reconstructed from the persisted staged manifest after restart.

Gateway events cannot install extensions or modify either source.

```json
{
  "extensions": [{
    "version": 1,
    "extension_id": "telegram",
    "instance_id": "telegram-main",
    "contract": "gateway",
    "abi_version": 2,
    "enabled": true,
    "artifact": {
      "path": "extensions/telegram/gateway.wasm",
      "sha256": "<64 lowercase hex characters>"
    },
    "configuration": {
      "version": 1,
      "telegram": {"host": "https://api.telegram.org"},
      "polling": {"timeout_seconds": 25, "allowed_updates": ["message"]}
    },
    "requested_capabilities": ["http", "secrets", "state", "log"],
    "granted_capabilities": ["http", "secrets", "state", "log"],
    "http_allowlist": ["api.telegram.org"],
    "secrets": {"telegram_token": "telegram-main.token"},
    "gateway": {
      "platform": "telegram",
      "doll_id": "default",
      "conversation_prefix": "telegram/chat/"
    }
  }]
}
```

## ABI compatibility

The shared runtime preserves the independently built Telegram guest ABI v2:

- imports use module `gateway_host`;
- exports are `gateway_abi_version`, `gateway_event_buf`,
  `gateway_event_buf_size`, `gateway_init`, `gateway_poll`,
  `gateway_handle_event`, and optional `gateway_shutdown`;
- envelope v0 remains `{ "v": 0, "type": ..., "payload": ... }`;
- `log`, `outgoing`, `http_get`, `config_projection`, and `telegram_token`
  remain available subject to grants;
- granted `state_get` and `state_put` provide durable per-instance state.

ABI v2 negotiates the host-to-guest event buffer through
`gateway_event_buf_size`. Existing 4 KiB guests remain loadable, while Core
supports envelopes up to 64 KiB when the guest advertises sufficient space.
Guest-to-host events are likewise limited to 64 KiB and 64 events per guest
call. Extension state values are limited to 4 MiB and mediated HTTP responses
to 4 MiB. These byte limits are checked before copying; events are rejected in
full and are never truncated.

`telegram_token` is an ABI compatibility name for the installation-scoped
secret of that name, not Telegram-specific authority in Core. `session.route`
also includes the existing `chat_id` alias alongside the generic conversation
reference.

The Telegram PR #5 guest builds independently against ABI v2. The compatibility
test sends `session.route` plus `session.event` through that actual guest and
verifies that its `delivery.result` repeats the stable delivery ID. Reproduce
that behavioral check by independently building the Telegram guest and passing
the resulting artifact to Core:

```sh
docker run --rm -v "$TELEGRAM_REPO:/src" -w /src tinygo/tinygo:0.39.0 \
  tinygo build -target=wasi -opt=z -no-debug \
  -o /src/wasm/build/gateway-current.wasm /src/wasm/gateway/main.go
NEONDOLL_TELEGRAM_WASM="$TELEGRAM_REPO/wasm/build/gateway-current.wasm" \
  go test ./Core/Extension \
  -run TestTelegramABICompatibilityWhenArtifactAvailable -count=1 -v
```

Core durably stages each emitted `message.incoming` before invoking trusted
ingress, retries ingress failures without polling past the event, and replies
with `inbound.accepted` containing the stable event ID only after acceptance.
The Telegram guest matches that event ID and durably advances only the accepted
cursor prefix through `state_get`/`state_put`.

Gateway message payloads may carry small `media` references containing a media
type, provider-scoped media and unique IDs, filename, MIME type, declared size,
caption, and provider message/conversation identifiers. Binary media is never
embedded in the Gateway envelope or extension state. A future separately
authorized capability may use the provider reference to retrieve content; the
reference itself grants no authority and Core's generic runtime contains no
provider-specific download behavior.

## Security and lifecycle

Guests receive no preopened filesystem, unrestricted sockets, or process
capability. HTTPS is mediated with TLS, hostname allowlists, deadlines,
redirect validation, and response limits. Wazero memory is bounded and calls
inherit cancellation. Disable and shutdown use the existing Gateway Host
lifecycle before stopping and releasing the module.

Configuration reconstructs enabled installations after daemon restart.
Gateway Sessions and pending deliveries remain Core-owned and replay through
stable delivery IDs. A single per-installation dispatcher classifies inbound
events and correlated `delivery.result` envelopes, so polling and delivery can
never consume one another's output. Durable extension state isolates inbound
pending records, cursors, and deduplication data by Core-assigned installation
identity.
