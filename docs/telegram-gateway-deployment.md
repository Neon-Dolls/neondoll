# Telegram WASM Gateway deployment

Telegram runs as an independently built WASM extension. Core owns its
installation identity, Session routing, external-identity resolution,
authorization, durable deliveries, and inference. Installing or updating the
Gateway does not rebuild `neondolld`.

## Install one bot

1. Build the Gateway from `dividebyzero/neondoll-gateway-telegram` (or obtain
   its release artifact):

   ```sh
   tinygo build -target=wasi -opt=z -no-debug \
     -o wasm/build/gateway.wasm wasm/gateway/main.go
   sha256sum wasm/build/gateway.wasm
   ```

2. Copy the artifact beneath Core's configured `paths.data_dir`. Artifact
   paths cannot escape that directory, including through symlinks.

   ```sh
   install -D -m 0644 wasm/build/gateway.wasm \
     /var/lib/neondoll/extensions/telegram/gateway.wasm
   ```

3. Store the Bot API token beneath `paths.secrets_dir`. The manifest contains
   only the relative secret name; the token is neither projected into guest
   configuration nor logged.

   ```sh
   install -d -m 0700 /var/lib/neondoll/secrets
   printf '%s' "$TELEGRAM_BOT_TOKEN" | \
     install -m 0600 /dev/stdin /var/lib/neondoll/secrets/telegram-main.token
   unset TELEGRAM_BOT_TOKEN
   ```

4. Add the installation and the trusted sender mapping to `config.json`. Use
   the SHA-256 printed in step 1 and the numeric Telegram user ID reported by
   Telegram, not a username or a role claim from a message.

   ```json
   {
     "core": {
       "profile": "my-doll",
       "external_identity_mappings": [{
         "identity": {
           "gateway_instance_id": "telegram-main",
           "platform": "telegram",
           "subject_id": "123456789"
         },
         "principal": {
           "principal_id": "owner",
           "authority": "owner"
         }
       }]
     },
     "paths": {
       "state_dir": "/var/lib/neondoll/state",
       "secrets_dir": "/var/lib/neondoll/secrets",
       "data_dir": "/var/lib/neondoll"
     },
     "extensions": [{
       "version": 1,
       "extension_id": "telegram",
       "instance_id": "telegram-main",
       "contract": "gateway",
       "abi_version": 2,
       "enabled": true,
       "artifact": {
         "path": "extensions/telegram/gateway.wasm",
         "sha256": "<artifact sha256>"
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
         "doll_id": "my-doll",
         "conversation_prefix": "telegram/chat/"
       }
     }]
   }
   ```

   Configure `inference.models` and the `interaction` task to a reachable
   OpenAI-compatible endpoint as described by the main Core configuration.
   The Gateway does not select the model.

5. Start `neondolld` from the directory containing `config.json`. On the first
   message, Core creates (or resumes) the active Session owned by
   `telegram-main` with conversation reference `telegram/chat/<chat-id>`.
   `gateway.doll_id` selects the addressed Doll. Subsequent messages resume
   that same Session; no Telegram-supplied Session ID is trusted.

## Smoke test

Send a unique text message from the mapped Telegram account and verify:

- Core logs extension activation without printing the token;
- the configured inference endpoint receives one Interaction request;
- the bot returns the model response to the same chat;
- resending the same Telegram update does not create a second turn;
- after stopping and restarting Core, a new message resumes the same Session.

For a failure check, temporarily make the Bot API unavailable after inference.
The response remains a pending Core delivery with its stable delivery ID and
is retried after service recovery; the admitted interaction is not rerun.
An unmapped sender can never acquire Owner authority from Telegram fields or
message text. If an installation should reject all unmapped conversation,
enforce that as Core action/session policy rather than treating Telegram IDs as
authorization.

## Delivery and restart behavior

The Gateway durably stages updates before Core ingress and advances Telegram's
cursor only after `inbound.accepted`. Core durably stages the response
SessionEvent before attempting `sendMessage`. Pending outbound deliveries are
retried while Core is running and replayed after restart or Gateway
re-registration. Two installations have distinct Core-assigned instance IDs,
secret scopes, state namespaces, Sessions, cursors, and delivery IDs.

Text and small media references travel in the event envelope. Binary media is
not downloaded or embedded; retrieval remains a future separately authorized
capability.
