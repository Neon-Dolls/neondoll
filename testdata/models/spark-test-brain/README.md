# Spark Test Brain

This directory is reserved for NeonDoll's committed real-inference E2E fixture.

## Canonical source

- Upstream model: `sdobson/tinystories-llama-15m`
- Revision: `6cc2bab8c4a55cce6f7532317af84a94bb340d5e`
- Architecture: LlamaForCausalLM, 6 layers, 288 hidden dim, 6 heads
- Parameters: ~15.2M
- Upstream license: MIT
- Purpose: test fixture only; not a supported production inference model
- Upstream: https://huggingface.co/sdobson/tinystories-llama-15m

## Required committed files

```text
spark-test-brain/
├── model.gguf
├── LICENSE
└── README.md
```

`model.gguf` MUST be committed to normal Git. Do not use Git LFS and do not
download the model during tests or CI.

`LICENSE` is the exact MIT license from the pinned upstream model revision.

## Conversion

| Step | Tool | Details |
|------|------|---------|
| F16 GGUF | `convert_hf_to_gguf.py` | HuggingFace → F16 GGUF |
| Q4_K_M (mixed) | `llama-quantize` | F16 GGUF → Q4_K_M (288-dim tensors fall back to Q5_0/Q8_0 where block-alignment requires it) |

### llama.cpp Revision

`5b59b83f4e2101ea173d4f853a0522d9971f48c6`

### Conversion Commands

```sh
# Convert HF model to F16 GGUF
python3 convert_hf_to_gguf.py /tmp/hf-models/tinystories \
    --outfile /tmp/hf-models/tinystories/model-f16.gguf --outtype f16

# Quantize to mixed Q4_K_M (falls back where alignment requires)
llama-quantize /tmp/hf-models/tinystories/model-f16.gguf \
    /tmp/hf-models/tinystories/model-q4_km.gguf Q4_K_M
```

Note: The GGUF converter was patched to emit `token_embd.weight` in addition
to `output.weight` for tied-embedding models (tinystories-llama-15m has
`tie_word_embeddings: true`, meaning the safetensors contain only
`lm_head.weight` and the converter must create both `token_embd.weight`
and `output.weight`).

## Smoke Test

```sh
llama-completion -m model.gguf \
    -p "Once upon a time there was a little robot named Spark who" \
    -n 30
```

Expected: coherent TinyStories-style output. The prose does not need to be
good — only non-empty.

## Fixture

| Property | Value |
|----------|-------|
| File | `model.gguf` |
| Size | 21,041,328 bytes (21 MiB) |
| SHA-256 | `9cda598c8eceae5385fd6cd51386daa1d9fd7f11564a37c811f1c62489` |
| Quantization | Mixed Q4_K_M / Q5_0 / Q8_0 |
| Model bits per weight | 6.64 BPW |

## E2E contract

The real-inference E2E test must be self-contained after a normal Git clone.

It may require/build a llama.cpp executable/runtime, but it MUST NOT:

- download model weights,
- contact Hugging Face,
- contact another model registry,
- require Git LFS,
- require an API key,
- require network inference.

## Why this file lives in Git

The model is part of NeonDoll's test fixture, not an external service dependency.

A future disappearance or mutation of an upstream model repository must not
make an old NeonDoll revision untestable.

The intended invariant is:

> A normal clone contains the exact tiny neural network required to prove
> real local inference.