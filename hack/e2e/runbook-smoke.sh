#!/usr/bin/env bash
# runbook-smoke.sh — R18: prove the break-glass levers and the uninstall order
# against a real cluster, because every one of them is a claim about what happens
# when a cluster is in a bad state, and prose cannot be wrong out loud.
#
# It runs the documented procedures verbatim (the same scripts an operator would
# type) and asserts the outcome the runbook promises:
#
#   1. With the R6 policy enforced, a GPU pod on the DEFAULT scheduler is denied.
#   2. `break-glass.sh gpu-scheduling` — the same pod is now admitted. This is the
#      lever that matters: without it, a wedged jobtree means no GPU pod in the
#      cluster can start at all.
#   3. `break-glass.sh committing`     — the scheduler Deployment is at 0 replicas.
#   4. `break-glass.sh crd-writes`     — the CRD webhooks are failurePolicy=Ignore,
#      a Run write goes through with the webhook pointed at a dead service, and
#      `--undo` puts Fail back.
#   5. `uninstall.sh` — Runs drain through the finalizer (which is what CLOSES the
#      leases), no finalizer is left behind, the ledger is closed BEFORE the CRDs
#      go, and by default the CRDs (the audit trail) survive.
#
# Assumes a cluster with the chart installed by hack/e2e/install.sh, with
# podPolicy.enabled=true. Run it via `make e2e-runbook`, which does that for you.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=/dev/null
source "$ROOT/hack/e2e/versions.env"

NS="$E2E_NAMESPACE"
REL="$E2E_HELM_RELEASE"
BG="$ROOT/hack/break-glass.sh"
UNINSTALL="$ROOT/hack/uninstall.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }

gpu_pod_manifest() {
  cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $1
  namespace: default
spec:
  restartPolicy: Never
  containers:
    - name: workload
      image: registry.k8s.io/pause:3.10
      resources:
        limits:
          nvidia.com/gpu: "1"
EOF
}

echo "==> 0. preconditions"
kubectl get ns "$NS" >/dev/null || fail "namespace $NS not found; run hack/e2e/install.sh first"
kubectl get validatingadmissionpolicybinding -l "app.kubernetes.io/instance=$REL" \
  -o name | grep -q . || fail "the R6 policy binding is not installed; install with --set podPolicy.enabled=true"
ok "chart installed in $NS with the mandatory-scheduler policy enforced"

echo
echo "==> 1. a GPU pod on the default scheduler is DENIED"
# The pod names no schedulerName, so it would be the default scheduler's — which is
# exactly the budget bypass R6 exists to close.
#
# Poll, because enforcement is not instant. The apiserver evaluates admission policies
# from a cached snapshot, so for a few seconds after `helm upgrade` creates the binding
# it is not yet enforcing — exactly the lag that break-glass.sh waits out in the other
# direction. Asserting immediately here would make this test flaky AND would hide the
# lag, which is a fact operators need.
deadline=$(( $(date +%s) + 90 ))
while :; do
  if gpu_pod_manifest bypass-attempt | kubectl apply --dry-run=server -f - >/tmp/runbook-deny.out 2>&1; then
    if [ "$(date +%s)" -ge "$deadline" ]; then
      cat /tmp/runbook-deny.out >&2
      fail "90s after install, the policy still admits a GPU pod that does not use the jobtree scheduler"
    fi
    sleep 2
    continue
  fi
  grep -q "schedulerName" /tmp/runbook-deny.out \
    || fail "denied, but not by the mandatory-scheduler rule: $(cat /tmp/runbook-deny.out)"
  break
done
ok "denied by the R6 policy, as documented"

echo
echo "==> 2. break-glass gpu-scheduling restores default GPU scheduling"
"$BG" --namespace "$NS" --release "$REL" gpu-scheduling
# Server-side dry run runs the whole admission chain, so this is the real verdict.
gpu_pod_manifest bypass-attempt | kubectl apply --dry-run=server -f - >/dev/null \
  || fail "a GPU pod is STILL denied after the break-glass lever; the runbook's most important promise is false"
ok "a GPU pod on the default scheduler is admitted again"

echo
echo "==> 3. break-glass committing stops the sole committer"
"$BG" --namespace "$NS" --release "$REL" committing
sched="$(kubectl get deploy -n "$NS" -l "app.kubernetes.io/instance=$REL" \
          -o "jsonpath={range .items[*]}{.metadata.name}{'\n'}{end}" | grep scheduler || true)"
[ -n "$sched" ] || fail "no scheduler Deployment found"
replicas="$(kubectl get deploy "$sched" -n "$NS" -o jsonpath='{.spec.replicas}')"
[ "$replicas" = "0" ] || fail "scheduler Deployment is at $replicas replicas, want 0"
ok "$sched scaled to 0 — nothing new is bound or funded"

"$BG" --namespace "$NS" --release "$REL" --undo committing
replicas="$(kubectl get deploy "$sched" -n "$NS" -o jsonpath='{.spec.replicas}')"
[ "$replicas" = "1" ] || fail "--undo left the scheduler at $replicas replicas, want 1"
ok "--undo restored it"

echo
echo "==> 4. break-glass crd-writes unblocks Run/Budget/GPULease writes"
# Make the lever's claim falsifiable: point EVERY webhook in the validating
# configuration at a service that does not exist, so the apiserver's call fails.
# With failurePolicy=Fail that must refuse the write; the lever's whole job is to
# turn that into an accept. Breaking only webhooks[0] would prove nothing — that
# entry is vbudget, and a Run never goes near it.
vwc="$(kubectl get validatingwebhookconfiguration -l "app.kubernetes.io/instance=$REL" -o jsonpath='{.items[0].metadata.name}')"
[ -n "$vwc" ] || fail "no validating webhook configuration for release $REL"
nwh="$(kubectl get validatingwebhookconfiguration "$vwc" -o 'jsonpath={.webhooks[*].name}' | wc -w)"
saved_svc="$(kubectl get validatingwebhookconfiguration "$vwc" -o jsonpath='{.webhooks[0].clientConfig.service.name}')"

patch_services() {
  local value="$1" patch='[' i
  for ((i = 0; i < nwh; i++)); do
    [ "$i" -gt 0 ] && patch+=','
    patch+="{\"op\":\"replace\",\"path\":\"/webhooks/$i/clientConfig/service/name\",\"value\":\"$value\"}"
  done
  patch+=']'
  kubectl patch validatingwebhookconfiguration "$vwc" --type=json -p "$patch" >/dev/null
}

run_manifest() {
  cat <<EOF
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata:
  name: $1
  namespace: default
spec:
  owner: org:team
  resources: {gpuType: H100-80GB, totalGPUs: 8}
EOF
}

patch_services gone-fishing
if run_manifest blocked-by-a-dead-webhook | kubectl apply --dry-run=server -f - >/dev/null 2>&1; then
  patch_services "$saved_svc"
  fail "a Run was accepted with failurePolicy=Fail and the webhook unreachable; the lever would be testing nothing"
fi
ok "with failurePolicy=Fail and the endpoint gone, Run writes are refused (the situation the lever is for)"

"$BG" --namespace "$NS" --release "$REL" crd-writes
for cfg in $(kubectl get validatingwebhookconfiguration -l "app.kubernetes.io/instance=$REL" -o name); do
  policies="$(kubectl get "$cfg" -o "jsonpath={range .webhooks[*]}{.failurePolicy}{'\n'}{end}" | sort -u)"
  [ "$policies" = "Ignore" ] || fail "$cfg still has failurePolicy: $policies"
done
ok "every CRD webhook is failurePolicy=Ignore"

run_manifest written-during-an-outage | kubectl apply -f - >/dev/null \
  || fail "a Run write was STILL blocked with failurePolicy=Ignore and the webhook unreachable — the lever does not work"
ok "a Run was written with the webhook unreachable; the lever does what it says"

patch_services "$saved_svc"
"$BG" --namespace "$NS" --release "$REL" --undo crd-writes
for cfg in $(kubectl get validatingwebhookconfiguration -l "app.kubernetes.io/instance=$REL" -o name); do
  policies="$(kubectl get "$cfg" -o "jsonpath={range .webhooks[*]}{.failurePolicy}{'\n'}{end}" | sort -u)"
  [ "$policies" = "Fail" ] || fail "--undo left $cfg at failurePolicy: $policies"
done
ok "--undo restored failurePolicy=Fail"

echo
echo "==> 4b. an additive CRD change does not disturb live objects"
# The upgrade guarantee in the runbook is "every CRD change so far is additive, so
# helm upgrade needs no conversion and loses nothing". Test the guarantee, not a
# no-op: add an optional property to a LIVE CRD's schema while an object of that
# kind exists, and check the object still reads back intact.
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: rq.davidlangworthy.io/v1
kind: Budget
metadata:
  name: survives-an-upgrade
  namespace: default
spec:
  owner: org:team
  envelopes:
    - name: west
      flavor: H100-80GB
      selector: {zone: west}
      concurrency: 8
EOF
before="$(kubectl get budget survives-an-upgrade -n default -o jsonpath='{.spec.envelopes[0].concurrency}')"

kubectl patch crd budgets.rq.davidlangworthy.io --type=json -p \
  '[{"op":"add","path":"/spec/versions/0/schema/openAPIV3Schema/properties/spec/properties/aFutureOptionalField","value":{"type":"string"}}]' >/dev/null \
  || fail "the apiserver refused an additive schema change on a CRD with live objects"

after="$(kubectl get budget survives-an-upgrade -n default -o jsonpath='{.spec.envelopes[0].concurrency}')"
[ "$before" = "$after" ] || fail "the live Budget changed across the schema update ($before -> $after)"
ok "the live Budget is unchanged after the additive schema change"

kubectl patch crd budgets.rq.davidlangworthy.io --type=json -p \
  '[{"op":"remove","path":"/spec/versions/0/schema/openAPIV3Schema/properties/spec/properties/aFutureOptionalField"}]' >/dev/null
kubectl delete budget survives-an-upgrade -n default >/dev/null
ok "schema restored"

echo
echo "==> 5. uninstall drains the finalizer, closes the ledger, and keeps the CRDs"
# The Run from step 4 is still there. Its finalizer is what closes leases, so this
# is the ordering claim under test: delete Runs while the MANAGER is still alive.
kubectl get run written-during-an-outage -n default >/dev/null \
  || fail "the test Run vanished before uninstall could drain it"

"$UNINSTALL" --release "$REL" --namespace "$NS" --yes

remaining="$(kubectl get runs.rq.davidlangworthy.io --all-namespaces --no-headers 2>/dev/null | wc -l)"
[ "$remaining" = "0" ] || fail "$remaining Run(s) survived uninstall — the finalizer did not drain"
ok "every Run drained through its finalizer"

open="$(kubectl get gpuleases.rq.davidlangworthy.io --all-namespaces \
         -o "jsonpath={range .items[?(@.status.closed!=true)]}{.metadata.name}{'\n'}{end}" 2>/dev/null | grep -c . || true)"
[ "$open" = "0" ] || fail "$open lease(s) are still open after uninstall — they would charge forever"
ok "no lease is left open"

kubectl get crd gpuleases.rq.davidlangworthy.io >/dev/null \
  || fail "the CRDs were deleted without --delete-crds; the audit trail is gone"
ok "the CRDs (and the ledger) survive an uninstall by default"

kubectl get deploy -n "$NS" -l "app.kubernetes.io/instance=$REL" -o name | grep -q . \
  && fail "the control-plane Deployments survived uninstall"
ok "the control plane is gone"

echo
echo "==> 6. --delete-crds removes the ledger, on purpose and only when asked"
"$UNINSTALL" --release "$REL" --namespace "$NS" --no-helm --delete-crds --yes
for crd in runs gpuleases budgets reservations; do
  kubectl get crd "$crd.rq.davidlangworthy.io" >/dev/null 2>&1 \
    && fail "$crd CRD survived --delete-crds"
done
ok "all four CRDs deleted"

echo
echo "runbook smoke PASSED"
