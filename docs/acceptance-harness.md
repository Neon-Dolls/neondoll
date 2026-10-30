# Acceptance harness

Run the current behavioral acceptance profile from a repository checkout:

```sh
make acceptance PROFILE=core5
```

Run the cross-repository profile with the pinned Shell Body checkout:

```sh
git clone https://github.com/Neon-Dolls/neondoll-shellbody.git /tmp/neondoll-shellbody
git -C /tmp/neondoll-shellbody checkout 10b2d510d9a5887800c8e6f72c2ef05530bf6c46
make acceptance-external PROFILE=core5 SHELLBODY_SOURCE=/tmp/neondoll-shellbody
```

External mode fails when the checkout is missing or not at the pinned
revision. It never substitutes the simulated M1 Body. Developers may provide
an independently built executable with `SHELLBODY_BINARY`; in that workflow
`SHELLBODY_REVISION` is the operator's explicit artifact attestation. CI
always builds from the pinned checkout.

The command prints one result per scenario, exits nonzero when any required
scenario fails or is skipped, and writes `acceptance-report.json`. Set
`ACCEPTANCE_REPORT` to choose another path. Unknown profiles fail explicitly.

## Structure

Profiles are ordered declarations of required scenario identifiers. They do
not copy scenario implementations. The initial `core5` profile contains:

1. `conversation`
2. `authorized-execution`
3. `autonomous-communication`

External mode appends, without replacing those scenarios:

4. `shellbody-authenticated-connection`
5. `shellbody-remote-execution`
6. `shellbody-connection-recovery`

Scenarios drive small participant implementations through production Core
interfaces. The runner deliberately has no generic plugin or protocol layer;
future profiles can select these scenarios and add new ones. Each JSON result
records participant kind, whether it was real or simulated, and the boundary
used. A failure does not prevent later scenarios from running, so the report
retains useful diagnostic coverage.

The report records the tested Core Git revision. Cross-repository runs may set
comma-separated component revisions, for example:

```sh
ACCEPTANCE_EXTERNAL_REVISIONS='shellbody=abc123,telegram=def456' \
  make acceptance PROFILE=core5
```

No external revisions are claimed by the default Core 5 profile because it
does not check out or execute external repositories.

## Core 5 boundaries

`conversation` uses a real SQLite Core store, runtime Session services,
ExternalIdentity resolution, Gateway Host/Ingress, Interaction Router,
Context Builder, and unified cognitive execution. Its Gateway adapter and
inference provider are deterministic in-process participants implementing the
production interfaces.

`authorized-execution` uses the production Body Registry, Body Guard,
ToolExecutor, resolver/coordinator, and shared CognitiveExecution loop. The
Body is a deterministic in-process implementation of the production Body
contract; it is not Doll Link or Shell Body process coverage.

`autonomous-communication` establishes a participant through trusted Gateway
ingress, starts cognition through `EnterPulseWake`, discovers the principal
through `core.communication__list_peers`, sends through
`core.communication__send_text`, and observes the canonical durable
SessionEvent at the Gateway adapter. Gateway and inference are deterministic
in-process participants.

The Shell Body scenarios build and launch the independently versioned
`neondoll-shellbody` executable. Core runs in-process but uses its production
Enrollment service, invitation authorization, Ed25519 Body authenticator,
Doll Link WebSocket server, RemoteRegistrar, Registry, and Body Guard. The
Shell Body enrolls through Core identity discovery and a real single-use
invitation, then authenticates and advertises `process/run` over the loopback
WebSocket transport.

Remote execution uses a harness-owned fixed `printf` invocation; no model or
external input supplies shell content. A separate deny-by-default Guard probe
uses a file marker to prove denied execution never reaches the Body. Recovery
interrupts and reaps the process, observes the registered capability become
unavailable, restarts from the same state directory, and executes again under
the same enrolled Body identity.

Child output is retained only in a bounded diagnostic buffer and appears in a
failure reason only as a short tail. Enrollment secrets and private keys are
never copied into reports. Every scenario uses dynamic loopback ports and
temporary state, signals children during cleanup, then kills and reaps them if
graceful termination exceeds its bound.

These are integration-level acceptance scenarios, not fully black-box daemon
E2E tests. The external Body is a real independent process, while Core remains
an in-process runtime and is labeled that way in JSON. They use no test-only
authorization or identity bypass. Temporary stores are removed and runtime
shutdown is invoked after every scenario.

## Remaining acceptance gaps

- Run the independently compiled Telegram WASM artifact as the Gateway driver.
- Add a separate real-daemon Core driver; current Shell Body evidence uses the
  production in-process Core runtime rather than a `neondolld` process.
- Add an embedded-device profile only when that participant is required.
- Add restart/recall scenarios for Core 6 memory providers through observable
  behavior. The harness does not inspect SQLite or assume a memory-provider
  storage layout.

External drivers should replace only the simulated participant at the same
production boundary. Revisions must be pinned and recorded; a mock must never
be relabeled as cross-repository compatibility evidence.
