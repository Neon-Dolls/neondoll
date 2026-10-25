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

At daemon startup Core resolves each task through the explicit assignments and
default, constructs its supported provider against the selected endpoint, and
injects each complete model configuration into the corresponding runtime path.
Unsupported providers fail startup explicitly.

Core does not dynamically discover models. Runtime selection is limited to the
four statically configured Core tasks and their explicit default fallback.

## Task assignments

Core defines four inference tasks: `interaction`, `orient`, `plan`, and
`direct_cognition`. Each optional `tasks` entry names one configured model.
Assignments are validated together with model configuration, so an unknown
model or task prevents startup. `ResolveTask` returns the complete resolved
`ModelConfiguration`; an unassigned known task returns the explicit default.

At runtime Core resolves all four task configurations before starting its
services and constructs each selected provider at its configured endpoint.
Interaction uses `interaction`; DollMind direct runs use `direct_cognition`;
Orient and Plan use their corresponding tasks. Pulse and intention wakes flow
through those Orient/Plan selections, and every tool continuation retains the
Plan model and budget. Any unmapped task receives the explicit default through
the same resolver path.
