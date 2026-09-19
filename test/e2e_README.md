# Spark Speaks — Real Inference E2E Test

This test (`test/e2e_test.go`) proves that NeonDoll can generate a response
through real neural network inference using the committed
`testdata/models/spark-test-brain/model.gguf`.

## Vertical Slice

    Doll Link request → Core Interaction → Persistence → Inference Provider
    → llama.cpp → Spark Test Brain GGUF → generated tokens → response

## Prerequisites

- A llama.cpp `llama-server` binary built for your platform.
- No model download required — the GGUF is committed in the repository.

## Running the Test

```sh
# Set the path to your llama-server executable
export NEONDOLL_LLAMA_SERVER=/path/to/llama-server

# Run the real-inference E2E test
go test -tags=e2e ./test/... -v -run TestE2E_SparkSpeaks
```

### What It Does

1.  Locates `llama-server` via `NEONDOLL_LLAMA_SERVER`.
2.  Finds a free TCP port.
3.  Starts `llama-server` with the committed `model.gguf`.
4.  Waits for the server to be ready.
5.  Decodes `spark.dollcard`, persists Spark, starts Core + Doll Link.
6.  Sends a real WebSocket message to Spark.
7.  Loads Spark from persistence, builds a minimal prompt, calls
    inference through the OpenAI-compatible HTTP provider.
8.  Verifies the response is non-empty, belongs to Spark's DollID,
    preserves the correlation ID, and is not a deterministic fallback.
9.  Terminates `llama-server` cleanly.

### Skip Without llama.cpp

The test skips with a clear message when `NEONDOLL_LLAMA_SERVER` is
unset. The regular test suite remains fully usable without llama.cpp.

## Error Path Test

A second E2E test (`TestE2E_InferenceFails`) verifies that when the
inference endpoint is unreachable, the system returns a clean error
response and Core remains alive.

## Building llama-server

The pinned llama.cpp revision is `5b59b83f4e2101ea173d4f853a0522d9971f48c6`.

```sh
git clone https://github.com/ggml-org/llama.cpp
cd llama.cpp
git checkout 5b59b83f4e2101ea173d4f853a0522d9971f48c6
mkdir build && cd build
cmake .. -DLLAMA_BUILD_SERVER=ON
cmake --build . --target llama-server -j$(nproc)
```

## Model

- Model: `sdobson/tinystories-llama-15m` (MIT license)
- Format: GGUF Q4_K_M, 21 MiB
- Location: `testdata/models/spark-test-brain/model.gguf`
- Provenance: see `testdata/models/spark-test-brain/README.md`