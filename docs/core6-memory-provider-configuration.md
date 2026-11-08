# Core 6 Memory Provider configuration

Core selects at most one named Memory Provider for a Doll. The current Go
constructor is a transitional composition boundary: Memory Providers remain
NeonDoll modules, and a future module loader can replace construction without
changing `memory.Provider` consumers.

No provider is started when `memory` is absent. When it is present, `active`
must name exactly one entry. Core never falls back to another provider and
never migrates memories when selection changes.

## SQLite

```json
{
  "memory": {
    "active": "local",
    "providers": {
      "local": {
        "instance_id": "doll-memory-local",
        "contract_version": 1,
        "implementation": "sqlite",
        "sqlite": { "path": "memory/neondoll.sqlite" }
      }
    }
  }
}
```

Relative SQLite paths are resolved and confined beneath `paths.data_dir`.
Traversal and symlink escapes fail startup before a database is opened or
created. Explicit absolute paths remain supported for operator-managed storage.
SQLite creates and maintains its private schema, while every record remains
scoped by the persisted Doll ID supplied by Core.

## OpenViking

```json
{
  "memory": {
    "active": "remote",
    "providers": {
      "remote": {
        "instance_id": "doll-memory-openviking",
        "contract_version": 1,
        "implementation": "openviking",
        "openviking": {
          "endpoint": "http://127.0.0.1:1933",
          "user_id": "neondoll",
          "namespace": "production",
          "api_key_secret": "openviking-api-key"
        }
      }
    }
  }
}
```

## Hindsight

```json
{
  "memory": {
    "active": "subjective",
    "providers": {
      "subjective": {
        "instance_id": "doll-memory-hindsight",
        "contract_version": 1,
        "implementation": "hindsight",
        "hindsight": {
          "endpoint": "http://127.0.0.1:8888",
          "bank_namespace": "neondoll",
          "token_secret": "hindsight-token"
        }
      }
    }
  }
}
```

Secret references are relative files beneath `paths.secrets_dir`; traversal
and symlink escapes are rejected. Omit a reference only for a backend deployed
without authentication. Core does not log secret values.

At startup Core validates configuration, constructs the selected provider with
the persisted Doll ID, and requires a successful health check. Any failure is
fatal and partial resources are closed. If any later daemon component fails to
start, normal function unwinding closes the already initialized provider before
the process exits. Normal shutdown also closes the provider idempotently.
OpenViking and Hindsight banks must already be provisioned; Core does not
implicitly create or destroy remote memory stores.

Changing `active`, an endpoint, namespace, or database path only changes which
store is opened. It does not transfer memories. Migration is an explicit,
potentially lossy export/import operation.

Legacy `instance_id`/`contract_version` references without `implementation`
remain valid contract configuration for future module-managed installations.
The transitional built-in runtime rejects such an active reference explicitly
until a module loader can resolve it; it does not silently choose SQLite.
