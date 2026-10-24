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

This foundation performs static configuration and name resolution only. It
does not select models by task, dynamically discover models, or change the
current Interaction and DollMind inference routing.
