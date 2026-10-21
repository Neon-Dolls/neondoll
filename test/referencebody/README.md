# Reference Body integration harness

This package is a test-only reference Body for Core 4 acceptance. It persists
an Ed25519 signing identity, an independent X25519/WireGuard identity, the
verified Core identity, and the signed enrollment receipt in an owner-protected
state file.

Before enrollment it discovers the CoreID and Ed25519 public key from
`GET /v1/core/identity`. The key is not preconfigured; the owner approval-code
ceremony remains the trust-establishment step.

The integration test composes the production enrollment wire types, owner Unix
socket, Doll Link authentication frames, WebSocket client, capability
dispatcher, RemoteRegistrar, Registry, and Guard. Core and Body run as separate
OS processes with temporary state. No authentication fixture or claimed
`body_id` shortcut is used.

Run the lifecycle proof with:

```sh
go test -tags=integration -run '^TestReferenceBodyEnrollmentAuthenticationRestartAndRevocation$' -count=1 ./test
```

The harness is intentionally isolated under `test/`; it is not a production
Body runtime and does not modify or replace `neondoll-shellbody`.
