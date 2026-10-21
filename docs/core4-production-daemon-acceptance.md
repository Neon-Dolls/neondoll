# Core 4 production daemon acceptance

The reproducible acceptance test is
`TestProductionDaemonBodyLifecycleAcceptance` in the integration suite. It
builds and starts the real `cmd/neondolld` and `cmd/neondollctl` binaries and
runs the Phase 4A reference Body in a separate OS process. Every process uses
temporary configuration and persistent state.

## Verified lifecycle

| Stage | Result |
| --- | --- |
| Body identity | A real Ed25519 signing key and independent X25519/WireGuard key are generated and stored with mode 0600. |
| Core identity discovery | The Body fetches the persistent CoreID and unpadded-base64url Ed25519 public key from `GET /v1/core/identity`; no database access or pre-shared Core key is used. |
| Enrollment | The Body sends the canonical signed enrollment request to the daemon's production HTTP handler. |
| Owner approval | The built `neondollctl` approves through the daemon's production owner-control Unix socket. |
| Receipt | The Body polls with newly signed nonces and verifies the Core-signed enrollment receipt and all identity bindings. |
| Authentication | The Body completes `body.auth.hello` → challenge → proof → accept over the daemon's real WebSocket listener and verifies Core signatures. |
| Registration | The authenticated Body advertises `test.echo`; the daemon's RemoteRegistrar updates the production Registry. |
| Execution | A harmless request crosses Guard → RemoteBody → Doll Link → Body Dispatcher and returns a correlated echo result. |
| Body restart | The reference process reloads the same signing key, WireGuard key, BodyID, and receipt, then reconnects without enrollment. |
| Core restart | `neondolld` reloads its persisted network, membership, signing identity, and credentials; the Body reconnects and executes again. |
| Revocation | `neondollctl` persists revocation, disconnects the active session, prevents execution, and subsequent authentication is rejected. |
| Fail closed | Pre-authentication capability advertisements are disconnected. A claimed BodyID cannot register capabilities or make a disconnected Body executable. |

## Test-only execution trigger

Core 4 intentionally has no public direct-execution HTTP endpoint. The
acceptance build tag adds a 0600 Unix socket that accepts only the harmless
`test.echo` request and invokes the production Guard. The hook is compiled out
of normal `neondolld` binaries, binds no network address, and adds no production
administrative or protocol surface.

## Production wiring correction

The audit found that HTTP-enabled daemon configurations still replaced the
configured host listener with the WireGuard netstack listener. This made the
bootstrap enrollment handlers unreachable to an unenrolled Body. HTTP-enabled
deployments now retain the configured host listener; explicitly overlay-only
configurations continue to use netstack. Normal `http.ErrServerClosed` during
daemon shutdown is also treated as successful shutdown, enabling clean restart
acceptance.

## Remaining limitations

- TLS termination and discovery of the Core's network URL remain external
  deployment concerns; public identity discovery occurs through that URL and
  the isolated acceptance test uses loopback TCP.
- The reference Body owns a real WireGuard identity and enrollment binds its
  public key, but this test targets Doll Link over loopback and does not repeat
  the separate WireGuard/Relay transport acceptance suites.
- The acceptance-only Guard trigger is not a user-facing execution API and is
  unavailable in production builds.

## Commands

```sh
go build ./...
go vet ./...
go test ./...
go test -tags=integration -run '^TestProductionDaemonBodyLifecycleAcceptance$' -count=1 ./test
go test -race -tags=integration -run '^TestProductionDaemonBodyLifecycleAcceptance$' -count=1 ./test
```
