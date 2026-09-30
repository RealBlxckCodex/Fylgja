#!/bin/bash
# Startet vLLM (nur auf 127.0.0.1) und den Node-Agent mit ausgehendem Tunnel.
# VLLM_MODEL: Hugging-Face-ID oder Pfad im Network Volume (/runpod-volume/models/...)
# VLLM_ARGS:  zusätzliche Argumente, z. B. "--max-model-len 65536 --enable-auto-tool-choice --tool-call-parser hermes"
set -euo pipefail
: "${VLLM_MODEL:?VLLM_MODEL fehlt}"
export HF_HOME="${HF_HOME:-/runpod-volume/hf}"
python3 -m vllm.entrypoints.openai.api_server --host 127.0.0.1 --port 8000 --model "$VLLM_MODEL" \
  --served-model-name "${VLLM_SERVED_NAME:-$VLLM_MODEL}" --enable-prefix-caching ${VLLM_ARGS:-} &
export FYLGJA_UPSTREAM="${FYLGJA_UPSTREAM:-127.0.0.1:8000}"
export FYLGJA_VLLM_METRICS="${FYLGJA_VLLM_METRICS:-http://127.0.0.1:8000/metrics}"
if [ -z "${FYLGJA_DEPLOYMENTS:-}" ]; then
  export FYLGJA_DEPLOYMENTS="[{\"model\":\"${FYLGJA_LOGICAL_MODEL:-worker-default}\",\"served_model\":\"${VLLM_SERVED_NAME:-$VLLM_MODEL}\",\"engine\":\"vllm\",\"max_concurrency\":${FYLGJA_MAX_CONCURRENCY:-16}}]"
fi
exec /usr/local/bin/fylgja-node
