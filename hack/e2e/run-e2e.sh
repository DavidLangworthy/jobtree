#!/usr/bin/env bash
# Full kind e2e flow (Track F — TESTINFRA; `make e2e`): stands up a cluster,
# builds and loads the real manager image, installs the real Helm chart
# (deploy/helm/gpu-fleet) against it, runs the test/e2e suite (build tag
# `e2e`), and tears the cluster down — even on failure, unless
# KEEP_CLUSTER=1 (handy for interactive debugging). On failure it dumps
# cluster diagnostics (hack/e2e/diagnostics.sh) before tearing down.
#
# Fail-hard-don't-skip: every prerequisite check lives in kind-up.sh and is a
# hard error, not a skip. This script's own steps (image build, helm
# install, go test) are not wrapped in any "skip if unavailable" logic
# either — a broken harness must show up red, per docs/project/make-it-
# real-plan.md Track F.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT/hack/e2e/versions.env"

KEEP_CLUSTER="${KEEP_CLUSTER:-0}"

cleanup() {
  local status=$?
  if [ $status -ne 0 ]; then
    echo "===== e2e run failed (exit $status); dumping cluster diagnostics ====="
    "$ROOT/hack/e2e/diagnostics.sh" || true
  fi
  if [ "$KEEP_CLUSTER" = "1" ]; then
    echo "KEEP_CLUSTER=1 set; leaving cluster '$KIND_CLUSTER_NAME' up for inspection (kubectl config use-context kind-$KIND_CLUSTER_NAME)."
  else
    "$ROOT/hack/e2e/kind-down.sh" || true
  fi
  exit $status
}
trap cleanup EXIT

"$ROOT/hack/e2e/kind-up.sh"

echo "Building the manager image ($E2E_IMAGE)..."
docker build -t "$E2E_IMAGE" "$ROOT"

echo "Building the scheduler image ($E2E_SCHEDULER_IMAGE)..."
docker build -f "$ROOT/Dockerfile.scheduler" -t "$E2E_SCHEDULER_IMAGE" "$ROOT"

echo "Loading both images into kind cluster '$KIND_CLUSTER_NAME'..."
kind load docker-image "$E2E_IMAGE" --name "$KIND_CLUSTER_NAME"
kind load docker-image "$E2E_SCHEDULER_IMAGE" --name "$KIND_CLUSTER_NAME"

"$ROOT/hack/e2e/install.sh"

echo "Running test/e2e (build tag e2e)..."
(cd "$ROOT" && JOBTREE_E2E_NAMESPACE="$E2E_NAMESPACE" go test ./test/e2e/... -tags=e2e -v -count=1)
