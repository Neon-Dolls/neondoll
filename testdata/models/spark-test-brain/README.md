# Spark Test Brain

This directory is reserved for NeonDoll's committed real-inference E2E fixture.

## Canonical source

- Upstream model: `sdobson/tinystories-llama-15m`
- Architecture: Llama
- Parameters: ~15.2M
- Upstream license: MIT
- Purpose: test fixture only; not a supported production inference model
- Upstream: https://huggingface.co/sdobson/tinystories-llama-15m

The upstream model is deliberately used instead of depending on a third-party GGUF conversion. The committed GGUF must be produced mechanically from this model with llama.cpp.

## Required committed files

When the inference milestone is implemented, this directory must contain:

```text
spark-test-brain/
├── model.gguf
├── LICENSE
└── README.md
```

`model.gguf` MUST be committed to normal Git. Do not use Git LFS and do not download the model during tests or CI.

`LICENSE` MUST be the exact MIT license shipped by the pinned upstream model revision.

## Producing model.gguf

Pin both the upstream model revision and the llama.cpp revision used for conversion. Do not build from moving `main` revisions without recording them here.

The intended conversion flow is:

```text
sdobson/tinystories-llama-15m
        │
        │ llama.cpp convert_hf_to_gguf.py
        ▼
      GGUF
        │
        │ llama-quantize
        ▼
      Q4_K_M
        │
        ▼
      model.gguf
```

Example procedure:

```bash
# 1. Obtain a pinned checkout/snapshot of:
#    https://huggingface.co/sdobson/tinystories-llama-15m
#
# 2. Obtain a pinned llama.cpp revision:
git clone https://github.com/ggml-org/llama.cpp
cd llama.cpp
git checkout <PINNED_LLAMA_CPP_REVISION>

# 3. Build llama.cpp tools:
cmake -B build
cmake --build build -j --target llama-quantize llama-cli llama-server

# 4. Convert the pinned Hugging Face model:
python3 convert_hf_to_gguf.py <PATH_TO_PINNED_MODEL> \
  --outfile /tmp/spark-test-brain-f16.gguf \
  --outtype f16

# 5. Quantize:
./build/bin/llama-quantize \
  /tmp/spark-test-brain-f16.gguf \
  testdata/models/spark-test-brain/model.gguf \
  Q4_K_M

# 6. Copy the exact upstream LICENSE:
cp <PATH_TO_PINNED_MODEL>/LICENSE \
  testdata/models/spark-test-brain/LICENSE

# 7. Record provenance below and calculate:
sha256sum testdata/models/spark-test-brain/model.gguf
```

If Q4_K_M is unsupported for this very small architecture in the pinned llama.cpp revision, choose the smallest stable llama.cpp-supported quantization that produces valid output and remains comfortably below roughly 30 MB. Record the reason here.

## Provenance

Fill these values when `model.gguf` is committed:

```text
Upstream revision: <PIN>
llama.cpp revision: <PIN>
Quantization: <Q4_K_M or actual>
model.gguf size: <BYTES>
model.gguf SHA-256: <SHA256>
```

Once committed and proven working, this fixture is pinned. Do not update it merely because a newer model or quantization exists.

## E2E contract

The real-inference E2E test must be self-contained after a normal Git clone.

It may require/build a llama.cpp executable/runtime, but it MUST NOT:

- download model weights,
- contact Hugging Face,
- contact another model registry,
- require Git LFS,
- require an API key,
- require network inference.

The test should launch a local llama.cpp-compatible inference endpoint against `model.gguf` and exercise the real NeonDoll inference path.

The model is a tiny base model trained on TinyStories. It is not instruction-tuned. Do not test answer quality or exact prose.

The test should assert structural behavior only:

- the committed GGUF exists and is readable,
- the model can be loaded by the test inference runtime,
- inference completes,
- at least one real generated token is returned,
- generated response text is non-empty,
- the response belongs to the addressed Doll,
- Doll Link request/response correlation survives,
- no mock/fallback inference provider handled the request.

A simple completion-style prompt is preferable to an instruction-following prompt.

## Fixture integrity test

When `model.gguf` is added, add a fast test that verifies:

1. the file exists,
2. its first four bytes are the GGUF magic `GGUF`,
3. its SHA-256 equals the pinned value recorded above.

This integrity test should not launch inference and should run with the normal test suite.

The slower real-inference test belongs with the E2E/integration suite.

## Why this file lives in Git

The model is part of NeonDoll's test fixture, not an external service dependency.

A future disappearance or mutation of an upstream model repository must not make an old NeonDoll revision untestable.

The intended invariant is:

> A normal clone contains the exact tiny neural network required to prove real local inference.

## License note

The upstream model repository declares the model MIT licensed. Before committing the weights, preserve the exact upstream `LICENSE` from the pinned source revision next to `model.gguf`.
