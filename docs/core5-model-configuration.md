# Core 5 model configuration

Core configuration contains an explicit default model and a static map of
named model configurations:

```json
{
  "inference": {
    "default_model": "local",
    "models": {
      "local": {
        "provider": "openai",
        "model": "example-model",
        "endpoint": "http://localhost:8080",
        "budget": {
          "context_tokens": 32768,
          "output_tokens": 1024,
          "required_overhead_tokens": 256
        }
      }
    },
    "tasks": {
      "interaction": "local",
      "orient": "local"
    }
  }
}
```

The map key is the Core-owned model configuration name. `model` identifies the
underlying provider model, while `endpoint` is its provider endpoint or local
reference. Multiple names may target the same model and endpoint with different
budgets.

An omitted `context_tokens` value becomes 32,768. Output and overhead reserves
must be configured, and the resulting input capacity must be positive. The
resolver never consults provider capabilities or expands the configured
context window. Unknown names and invalid default references fail explicitly.

At daemon startup Core resolves the explicit default, constructs its supported
provider against the configured endpoint, and injects the resolved model
identifier and budget into Interaction and DollMind. Orient, Plan, Pulse,
direct cognition, and tool continuations therefore carry the same configured
model identifier. Unsupported providers fail startup explicitly.

Core does not yet select models by task or dynamically discover models. The
configured default remains the only runtime selection.

## Task assignments

Core defines four inference tasks: `interaction`, `orient`, `plan`, and
`direct_cognition`. Each optional `tasks` entry names one configured model.
Assignments are validated together with model configuration, so an unknown
model or task prevents startup. `ResolveTask` returns the complete resolved
`ModelConfiguration`; an unassigned known task returns the explicit default.

Task resolution is configuration-only in M2.15. Runtime Interaction and
DollMind execution continue using the already-wired default model until the
next integration milestone.
