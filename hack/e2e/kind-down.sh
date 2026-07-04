#!/usr/bin/env bash
# Tears down the e2e kind cluster. Idempotent: a missing cluster is not an
# error (repeated teardown, e.g. from a trap on both success and failure
# paths, must not itself fail the run).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT/hack/e2e/versions.env"

if ! command -v kind >/dev/null 2>&1; then
  exit 0
fi

if kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER_NAME"; then
  echo "Deleting kind cluster '$KIND_CLUSTER_NAME'..."
  kind delete cluster --name "$KIND_CLUSTER_NAME"
else
  echo "kind cluster '$KIND_CLUSTER_NAME' does not exist; nothing to do."
fi
