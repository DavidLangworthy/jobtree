#!/usr/bin/env bash
# Dumps cluster diagnostics for a failed e2e run (Track F — TESTINFRA-7):
# node/pod state, events, and the manager's own logs. Called from
# run-e2e.sh's failure path and from .github/workflows/e2e.yaml so CI
# uploads/prints something actionable instead of just "exit 1".
#
# Deliberately best-effort (no `set -e`): a single missing resource (e.g. the
# manager never even started) must not stop the rest of the dump.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT/hack/e2e/versions.env"

echo "===== nodes ====="
kubectl get nodes -o wide
echo "===== pods (all namespaces) ====="
kubectl get pods -A -o wide
echo "===== events ($E2E_NAMESPACE) ====="
kubectl get events -n "$E2E_NAMESPACE" --sort-by=.lastTimestamp
echo "===== events (default) ====="
kubectl get events -n default --sort-by=.lastTimestamp
echo "===== manager logs ($E2E_NAMESPACE/gpu-fleet-controller) ====="
kubectl logs -n "$E2E_NAMESPACE" deploy/gpu-fleet-controller --all-containers --tail=1000
echo "===== jobtree objects (default namespace) ====="
kubectl get runs,budgets,leases,reservations -n default -o yaml
