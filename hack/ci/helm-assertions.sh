#!/usr/bin/env bash
# Render the chart and assert the properties CI must never regress.
# Extracted from .github/workflows/ci.yaml so `make verify` and CI run the
# SAME checks: a gate that exists only in CI is a gate nobody can run before
# pushing, and a gate that exists only locally is one CI cannot enforce.
set -euo pipefail

rendered="$(helm template ci deploy/helm/gpu-fleet --namespace jobtree-system)"

# R22: no wildcard RBAC rules ship — catch both the inline array form
# (apiGroups: ["*"]) and the YAML list form (- "*").
if grep -qE '(apiGroups|resources|verbs):[[:space:]]*\[[^]]*"\*"' <<<"$rendered" \
   || grep -qE "^[[:space:]]*-[[:space:]]*['\"]?\*['\"]?[[:space:]]*$" <<<"$rendered"; then
  echo "::error::chart grants wildcard RBAC" >&2
  exit 1
fi

# R29: the chart must provision webhook serving so the deployed manager can
# admit objects, and the probes it serves.
for needle in \
  "kind: MutatingWebhookConfiguration" \
  "kind: ValidatingWebhookConfiguration" \
  "caBundle:" \
  "path: /healthz" \
  "path: /readyz" \
  "path: /jobtree"; do
  if ! grep -qF "$needle" <<<"$rendered"; then
    echo "::error::rendered chart missing: $needle" >&2
    exit 1
  fi
done

echo "helm chart assertions OK"
