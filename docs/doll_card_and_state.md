# Doll Card & Doll State

This doc explains the doll identity & memory architecture in Core.

## Doll Card (CORE dollcard)

A doll card is JSON persisted at `doll_id.json` in `DollCard/` (or via the central
master store when DollPersistence is enabled). It carries doll identity.

### doll_metadata
- `doll_id`: unique doll identifier (e.g. `drs-whiteout`).
- `doll_version`: integer version.
- `identity_internal_drive_json` / `identity_internal_goal_json`: doll-global
  internal drives/goals.

### drives
Static, unchangeable drives (e.g. self-preservation). See Core/Drive.

### goals
Dynamic, addressable goals. A goal is either pending (laid but not yet
processed) or complete.

## Intentions (Core/Persistence + Core/DollState)

Intentions are the doll's own durable recollections of what it intends to wake
for. Key facts:

- Stored as `intentions_json` TEXT (default `[]`) on the
  `dolls_json->drives`-style persistence. DollPersistence's column is
  `dollstate.DollState[].Intention.Items`.
- `ID`: stable identifier. `WakeTime`: RFC3339 UTC; a `wake_time` of `0` is
  "no wake time set yet".
- Full doll state: when SaveDoll is invoked the whole structure (drives,
  goals, intentions) is saved at once. LoadDoll decodes it back.
- Migration: reopening an older DB (created before the intentions column
  existed) auto-migrates. Migration is idempotent: opening a doll with an
  absent column adds it; opening with an existing column decodes as-is.

### Run intentions
Intentions capture *what*, *wake time*, and doll state. Example doll: a doll
with ID `int-001`, Subject "review supply chain", State `pending`.

```go
loaded.Intentions.Items = []dollstate.IntentionItem{
    {
        ID:       "int-001",
        Subject:  "review supply chain",
        WakeTime: "2026-09-20T12:00:00Z",
        State:    "pending",
    },
}
```

## Doll State (Core/Persistence state persistence)

Doll state lives in `Core/Persistence/dollstate`. The persistence store uses
a SQLite DB with `dolls_json->drives` and `dolls_json->goals` columns; the
intentions are a doll-owned row of JSON by default; the drive struct is
marshaled to `Core/Persistence/drives.go:Drives` with the intentions living
in the `doll_id`-scoped `intentions_json` column.