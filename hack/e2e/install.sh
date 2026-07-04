#!/usr/bin/env bash
# Installs the real Helm chart (deploy/helm/gpu-fleet) against whatever
# cluster kubectl's current context points at, with the e2e values overlay.
# Split out from run-e2e.sh so CI (.github/workflows/e2e.yaml) can call the
# same install step individually, with its own diagnostics-on-failure and
# teardown steps around it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT/hack/e2e/versions.env"

echo "Installing the real chart (deploy/helm/gpu-fleet) with the e2e values overlay..."
helm upgrade --install "$E2E_HELM_RELEASE" "$ROOT/deploy/helm/gpu-fleet" \
  --namespace "$E2E_NAMESPACE" --create-namespace \
  -f "$ROOT/hack/e2e/values-e2e.yaml" \
  --set "controller.image=$E2E_IMAGE" \
  --set "scheduler.enabled=true" \
  --set "scheduler.image=$E2E_SCHEDULER_IMAGE" \
  --wait --timeout 180s
