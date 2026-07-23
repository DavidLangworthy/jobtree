#!/usr/bin/env bash
# Render the chart and assert the properties CI must never regress.
# Extracted from .github/workflows/ci.yaml so `make verify` and CI run the
# SAME checks: a gate that exists only in CI is a gate nobody can run before
# pushing, and a gate that exists only locally is one CI cannot enforce.
set -euo pipefail

CHART=deploy/helm/gpu-fleet
NS=jobtree-system

# Comments are rendered verbatim by `helm template`, and several of them quote the
# very strings these assertions grep for. Strip them, or a comment can satisfy a
# check that the manifest does not.
strip_comments() { grep -vE '^[[:space:]]*#' | grep -vE '^[[:space:]]*$'; }

# Declare the Prometheus Operator's CRDs so the ServiceMonitor renders: the
# /jobtree scrape endpoint lives on it, and R29 asserts that endpoint exists. The
# "does a bare install work without them" case is checked separately, below.
rendered="$(helm template ci "$CHART" --namespace "$NS" --api-versions monitoring.coreos.com/v1)"

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

# --- R15: the chart may only deploy images this repository actually builds ----
#
# The chart shipped a default-ON `notifier` Deployment pointing at
# ghcr.io/davidlangworthy/jobtree-notifier — an image with no source anywhere in
# the repo and no job that ever pushed it. `helm install --wait` hung on
# ImagePullBackOff forever. The Dockerfile has exactly two targets, so the
# rendered chart may name exactly two image repositories.
all_rendered="$(helm template ci "$CHART" --namespace "$NS" \
                 --api-versions monitoring.coreos.com/v1 \
                 --set scheduler.enabled=true | strip_comments)"

while read -r ref; do
  case "${ref%%:*}" in
    */jobtree-controller|*/jobtree-scheduler|jobtree-controller|jobtree-scheduler) ;;
    *) echo "::error::chart deploys '$ref', which this repository does not build" >&2; exit 1 ;;
  esac
done < <(grep -E '^[[:space:]]*image:[[:space:]]' <<<"$all_rendered" | awk '{print $2}')

# --- R15: `image.tag` must actually reach the containers ----------------------
#
# The operator guide documented `--set image.tag=$(git rev-parse --short HEAD)`
# against a chart that had no `image.tag` key at all: a silent no-op that left
# every install on `:latest`. A flag the docs promise and the template ignores
# is worse than no flag, because nothing fails.
pinned="$(helm template ci "$CHART" --namespace "$NS" \
           --set scheduler.enabled=true --set image.tag=pin-me-9a1b | strip_comments)"
pinned_count="$(grep -cE '^[[:space:]]*image:[[:space:]].*:pin-me-9a1b$' <<<"$pinned" || true)"
if [ "$pinned_count" -ne 2 ]; then
  echo "::error::--set image.tag reached $pinned_count of the 2 chart containers; the flag is (partly) a no-op" >&2
  exit 1
fi

# A component tag overrides the global one, and neither may silently vanish.
override="$(helm template ci "$CHART" --namespace "$NS" \
             --set scheduler.enabled=true \
             --set image.tag=global-tag \
             --set scheduler.image.tag=sched-tag | strip_comments)"
if ! grep -qE '^[[:space:]]*image:[[:space:]].*jobtree-scheduler:sched-tag$' <<<"$override" \
   || ! grep -qE '^[[:space:]]*image:[[:space:]].*jobtree-controller:global-tag$' <<<"$override"; then
  echo "::error::a per-component image.tag does not override image.tag" >&2
  exit 1
fi

# With nothing set, the tag must be the chart's appVersion — the property the
# release workflow relies on when it packages with --app-version.
appversion="$(awk '/^appVersion:/{gsub(/"/,"",$2); print $2}' "$CHART/Chart.yaml")"
if ! grep -qE "^[[:space:]]*image:[[:space:]].*jobtree-controller:${appversion}\$" <<<"$all_rendered"; then
  echo "::error::the default controller image tag is not the chart appVersion ($appversion)" >&2
  exit 1
fi

# --- R16: the ServiceMonitor must actually select the metrics Service ---------
#
# It selected `app.kubernetes.io/name`, which `gpu-fleet.labels` never emitted, so
# it matched no Service and nothing was ever scraped. A ServiceMonitor that matches
# nothing is indistinguishable from a healthy one until you go looking for a metric.

svc="$(helm template ci "$CHART" --namespace "$NS" \
        --api-versions monitoring.coreos.com/v1 \
        --show-only templates/service.yaml | strip_comments)"

if ! grep -qF "kind: ServiceMonitor" <<<"$svc"; then
  echo "::error::ServiceMonitor is not rendered even when monitoring.coreos.com/v1 is available" >&2
  exit 1
fi

# The Service's own METADATA labels, and the labels the ServiceMonitor selects on.
# The extraction must stop at `spec:` — a Service's spec.selector carries the same
# label key to find its pods, and reading that instead compares the selector with
# itself, which is true no matter how broken the chart is.
service_doc="$(awk '/^kind: Service$/,/^---/' <<<"$svc")"
selected="$(awk '/matchLabels:/{f=1;next} f&&/app.kubernetes.io\/name:/{print $2;exit}' <<<"$svc")"
carried="$(awk '/^metadata:/{f=1;next} /^spec:/{f=0} f&&/app.kubernetes.io\/name:/{print $2;exit}' <<<"$service_doc")"

if [ -z "$selected" ] || [ -z "$carried" ]; then
  echo "::error::could not extract the ServiceMonitor selector ($selected) or the Service label ($carried)" >&2
  exit 1
fi
if [ "$selected" != "$carried" ]; then
  echo "::error::ServiceMonitor selects app.kubernetes.io/name=$selected but the Service carries $carried — nothing will be scraped" >&2
  exit 1
fi

# A ServiceMonitor parked in another namespace must be told where to look.
smns="$(helm template ci "$CHART" --namespace "$NS" \
         --api-versions monitoring.coreos.com/v1 \
         --set monitoring.serviceMonitorNamespace=monitoring \
         --show-only templates/service.yaml | strip_comments)"
if ! grep -qF "namespaceSelector:" <<<"$smns"; then
  echo "::error::a ServiceMonitor in monitoring.serviceMonitorNamespace has no namespaceSelector; it will select Services in its own namespace and find none" >&2
  exit 1
fi

# ...and the Prometheus Operator must not be a hard dependency: a bare install on a
# cluster without its CRDs used to fail with "no matches for kind ServiceMonitor".
bare="$(helm template ci "$CHART" --namespace "$NS" --show-only templates/service.yaml | strip_comments)"
if grep -qF "kind: ServiceMonitor" <<<"$bare"; then
  echo "::error::ServiceMonitor renders without the Prometheus Operator CRDs; a bare 'helm install' will fail" >&2
  exit 1
fi

# --- R17: replicas > 1 requires leader election -------------------------------
#
# The prod overlay ran three managers with no --leader-elect flag at all: three
# concurrent engines, each emitting intent pods and writing Run status against its
# own view of the ledger. Admission is serialized on one worker on purpose
# (specs/BudgetConservation.tla).

# assert_leader_election <label> <template> <flag-prefix> [values-file...]
assert_leader_election() {
  local label="$1" tmpl="$2" flag="$3"; shift 3
  local args=() doc replicas
  for f in "$@"; do args+=(-f "$f"); done

  doc="$(helm template ci "$CHART" --namespace "$NS" "${args[@]}" \
          --show-only "templates/$tmpl" 2>/dev/null | strip_comments)" || return 0
  [ -n "$doc" ] || return 0

  replicas="$(awk '/^spec:/{f=1;next} f&&/^  replicas:/{print $2;exit}' <<<"$doc")"
  [ -n "$replicas" ] || return 0
  [ "$replicas" -gt 1 ] || return 0

  if ! grep -qF -- "${flag}=true" <<<"$doc"; then
    echo "::error::$label runs $replicas replicas without ${flag}=true — concurrent engines will race on the ledger" >&2
    exit 1
  fi
}

assert_leader_election "controller (default values)" deployment.yaml           --leader-elect
assert_leader_election "scheduler (default values)"  scheduler-deployment.yaml --leader-elect

for overlay in deploy/kustomize/*/values-*.yaml; do
  assert_leader_election "controller ($overlay)" deployment.yaml           --leader-elect "$overlay"
  assert_leader_election "scheduler ($overlay)"  scheduler-deployment.yaml --leader-elect "$overlay"
done

# The manager must be given the flag at all; a chart that omits it silently accepts
# cmd/manager's default of false, which is how R17 hid.
if ! helm template ci "$CHART" --namespace "$NS" --show-only templates/deployment.yaml \
     | strip_comments | grep -qF -- "--leader-elect="; then
  echo "::error::the manager Deployment never passes --leader-elect; its value is unreachable from the chart" >&2
  exit 1
fi

# --- R17: production must run the committer ----------------------------------
#
# The scheduler plugin is the sole committer of GPU funding. A production install
# without it has no committer: nothing mints leases, so nothing is ever funded.
if ! helm template ci "$CHART" --namespace "$NS" -f deploy/kustomize/prod/values-prod.yaml \
     | grep -qF "name: gpu-fleet-scheduler"; then
  echo "::error::the prod overlay does not enable the scheduler plugin — nothing would mint leases" >&2
  exit 1
fi

# --- R13: our lease kind must never be spelled `leases` in the rq group -------
#
# `rq.davidlangworthy.io/Lease` collided with `coordination.k8s.io/Lease`, which
# every leader election in the cluster uses. The dangerous half was RBAC: an
# `apiGroups: ["rq.davidlangworthy.io"] resources: ["leases"]` rule LOOKS like a
# grant on our objects and, once the CRD is renamed, grants nothing at all — the
# committer's PreBind mint fails 403 and no work is ever funded. It is also one
# character away from granting the leader-election resource in the wrong group.
# So: in the rq group the resource is `gpuleases`, and `leases` there is an error.
#
# The coordination.k8s.io rule below it is a DIFFERENT resource and must survive;
# assert it does, or a future "fix" to this check silently breaks leader election.
rbac_rendered="$(helm template ci "$CHART" --namespace "$NS" --set scheduler.enabled=true | strip_comments)"

if awk '
    /apiGroups:.*rq\.davidlangworthy\.io/ { inrq = 1; next }
    /apiGroups:/                          { inrq = 0 }
    inrq && /resources:/ && /[]["]leases["]/ { found = 1 }
    END { exit !found }
  ' <<<"$rbac_rendered"; then
  echo "::error::an RBAC rule grants 'leases' in the rq.davidlangworthy.io group; the kind is GPULease and the resource is 'gpuleases' (R13)" >&2
  exit 1
fi

for needle in \
  'resources: ["budgets", "runs", "reservations", "gpuleases"]' \
  'resources: ["runs", "budgets", "gpuleases"]'; do
  if ! grep -qF "$needle" <<<"$rbac_rendered"; then
    echo "::error::rendered RBAC is missing the gpuleases grant: $needle" >&2
    exit 1
  fi
done

if ! grep -qF 'apiGroups: ["coordination.k8s.io"]' <<<"$rbac_rendered"; then
  echo "::error::the controller lost its coordination.k8s.io leases grant — leader election cannot acquire its lock" >&2
  exit 1
fi

# The webhook must intercept the renamed resource; a stale `leases` rule matches
# nothing, so every GPULease would be admitted unvalidated and silently.
if ! grep -qF 'resources: ["gpuleases"]' <<<"$rbac_rendered"; then
  echo "::error::the validating webhook does not select gpuleases (R13)" >&2
  exit 1
fi

# The shipped CRD is the renamed one, and the old one is gone from the chart.
if [ -e "$CHART/crds/rq.davidlangworthy.io_leases.yaml" ]; then
  echo "::error::the chart still ships the pre-R13 leases CRD; the rename is a clean break, not a side-by-side" >&2
  exit 1
fi
if ! grep -qF "kind: GPULease" "$CHART/crds/rq.davidlangworthy.io_gpuleases.yaml"; then
  echo "::error::the chart's gpuleases CRD does not declare kind GPULease" >&2
  exit 1
fi

echo "helm chart assertions OK"
