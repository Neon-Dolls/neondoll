# Owner enrollment control

`neondolld` exposes owner enrollment operations only on the local Unix socket
`<data_dir>/neondoll-admin.sock`. The socket is created with mode `0600`; no
owner operation is registered on the network HTTP/WebSocket listener.

Use the companion CLI from the same host:

```sh
neondollctl body pending
neondollctl body approve J7KP-42MX --permissions sensor.read
neondollctl body deny J7KP-42MX
neondollctl body invite --expires 30m --permissions sensor.read --endpoint https://core.example
neondollctl body list
neondollctl body revoke body_example
```

Pass `-socket /path/to/neondoll-admin.sock` before `body` when the daemon uses a
non-default data directory. Output is JSON. Invitation secrets appear only in
the successful `body invite` response and are not retained in plaintext or
written to daemon logs.

Revocation atomically persists the credential and membership state, closes the
current authenticated Doll Link generation, releases pending executions, and
refreshes the live WireGuard peer set. Revoked credentials cannot authenticate
again after disconnect or Core restart.
