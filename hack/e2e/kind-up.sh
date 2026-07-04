#!/usr/bin/env bash
# Stands up (or reuses) the kind cluster for the e2e harness and installs the
# jobtree CRDs. Fail-hard-don't-skip (Track F — TESTINFRA-1): a missing
# prerequisite is a hard error, never a silent skip, because a harness that
# quietly no-ops on a missing dependency is exactly the kind of fake this
# track exists to make impossible.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT/hack/e2e/versions.env"

if ! command -v kind >/dev/null 2>&1; then
  echo "ERROR: kind not found on PATH. Install it (https://kind.sigs.k8s.io) — this harness will not silently skip." >&2
  exit 1
fi
if ! command -v kubectl >/dev/null 2>&1; then
  echo "ERROR: kubectl not found on PATH." >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "ERROR: no Docker daemon reachable. kind needs a working Docker (or podman, via KIND_EXPERIMENTAL_PROVIDER=podman)" \
       "daemon to run its nodes-as-containers. If you're inside a container yourself, this means Docker-in-Docker" \
       "isn't available in this environment — that is a real, environmental blocker, not something this script papers" \
       "over. See docs/project/testing-and-simulation.md." >&2
  exit 1
fi

if kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER_NAME"; then
  echo "kind cluster '$KIND_CLUSTER_NAME' already exists; reusing it (run 'make kind-down' first for a clean start)"
else
  echo "Creating kind cluster '$KIND_CLUSTER_NAME' (node image $KIND_NODE_IMAGE)..."
  kind create cluster --name "$KIND_CLUSTER_NAME" --image "$KIND_NODE_IMAGE"
fi

kubectl config use-context "kind-$KIND_CLUSTER_NAME" >/dev/null

echo "Waiting for all nodes to be Ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=120s

echo "Installing jobtree CRDs (config/crd/bases)..."
kubectl apply -f "$ROOT/config/crd/bases"

echo "kind-up complete: cluster '$KIND_CLUSTER_NAME' ready, jobtree CRDs installed."
