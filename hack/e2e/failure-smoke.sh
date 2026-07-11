#!/usr/bin/env bash
# Workload-failure live proof (R9 9A-3 / R8): the whole control plane on a real
# kind cluster, a real container that exits non-zero, and the failure edge closing
# the loop:
#
#   Run (1 active GPU), busybox that runs a moment then `exit 1`
#     -> plugin binds it and mints an Active lease -> Run Running
#     -> the container exits 1 => kubelet marks the Pod Failed (RestartPolicy=Never)
#     -> the manager's PodFailed watch fires handleWorkloadFailure under the default
#        Fail policy: Run -> Failed, its open lease closed WorkloadFailed
#
# Pre-9A-3 this HANGS: the pod-watch only reacted to Succeeded, so a failed pod left
# the run Running and its lease charging forever. This is the live proof the failure
# edge exists — the antifake terminal-phase allowlist is capped-at-2/shrink-only, so
# a real kubelet (not a hand-set phase) must carry this proof.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${KIND_CLUSTER_NAME:-jobtree-failure}"
WORK="$(mktemp -d)"
MGR_PID=""
SCHED_PID=""

cleanup() {
  local status=$?
  [ $status -ne 0 ] && { echo "== manager.log tail =="; tail -30 "$WORK/manager.log" 2>/dev/null || true; }
  [ -n "$MGR_PID" ] && kill "$MGR_PID" 2>/dev/null || true
  [ -n "$SCHED_PID" ] && kill "$SCHED_PID" 2>/dev/null || true
  [ "${KEEP_CLUSTER:-0}" != "1" ] && kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORK"
  exit $status
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "==> kind up (1 worker) + CRDs"
cat > "$WORK/kind.yaml" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
EOF
kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --wait 120s >/dev/null
kubectl apply -f "$ROOT/config/crd/bases/" >/dev/null

for NODE in $(kubectl get nodes -o name); do
  kubectl label "$NODE" region=us-west cluster=cluster-a fabric.domain=island-a gpu.flavor=H100-80GB --overwrite >/dev/null
  kubectl patch "$NODE" --subresource=status --type=json \
    -p '[{"op":"add","path":"/status/capacity/nvidia.com~1gpu","value":"1"}]' >/dev/null
done
kubectl taint node "${CLUSTER}-control-plane" node-role.kubernetes.io/control-plane- >/dev/null 2>&1 || true

echo "==> building + starting manager and scheduler"
go build -o "$WORK/manager" "$ROOT/cmd/manager"
go build -o "$WORK/scheduler" "$ROOT/cmd/scheduler"
"$WORK/manager" --enable-webhooks=false --leader-elect=false \
  --metrics-bind-address=0 --health-probe-bind-address=0 > "$WORK/manager.log" 2>&1 &
MGR_PID=$!
cat > "$WORK/config.yaml" <<EOF
apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration
leaderElection: { leaderElect: false }
clientConnection: { kubeconfig: ${KUBECONFIG:-$HOME/.kube/config} }
profiles:
  - schedulerName: jobtree
    plugins:
      filter:     { enabled: [ {name: jobtree} ] }
      score:      { enabled: [ {name: jobtree} ] }
      reserve:    { enabled: [ {name: jobtree} ] }
      permit:     { enabled: [ {name: jobtree} ] }
      preBind:    { enabled: [ {name: jobtree} ] }
      postFilter: { enabled: [ {name: jobtree} ] }
EOF
"$WORK/scheduler" --config "$WORK/config.yaml" --leader-elect=false > "$WORK/scheduler.log" 2>&1 &
SCHED_PID=$!
for _ in $(seq 1 60); do
  [ "$(grep -c 'Added node to NodeTree' "$WORK/scheduler.log")" -ge 1 ] && break
  sleep 1
done
sleep 2

echo "==> creating a Budget and a Run whose container exits 1"
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: rq.davidlangworthy.io/v1
kind: Budget
metadata: { name: team, namespace: default }
spec:
  owner: org:ai:team
  envelopes:
    - name: west
      flavor: H100-80GB
      concurrency: 8
      selector: { region: us-west, cluster: cluster-a, fabric.domain: island-a }
---
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata: { name: crasher, namespace: default }
spec:
  owner: org:ai:team
  resources: { gpuType: H100-80GB, totalGPUs: 1 }
  roles:
    - name: worker
      width: 1
      gpusPerPod: 1
      # failurePolicy defaults to Fail.
      template:
        spec:
          containers:
            - name: workload
              image: busybox:1.36
              command: ["sh", "-c", "sleep 8; echo crashing; exit 1"]
EOF

echo "==> waiting for Running (lease minted), then for the crash to fail the run"
kubectl wait --for=jsonpath='{.status.phase}'=Running run/crasher -n default --timeout=120s || fail "run never reached Running"

# The container exits 1 -> Pod Failed -> the failure edge fails the run.
kubectl wait --for=jsonpath='{.status.phase}'=Failed run/crasher -n default --timeout=120s \
  || fail "a crashed container must fail the run (9A-3); it stayed $(kubectl get run crasher -n default -o jsonpath='{.status.phase}')"

echo "==> asserting the run's leases closed WorkloadFailed (funding stopped)"
open="$(kubectl get leases.rq.davidlangworthy.io -n default \
  -l rq.davidlangworthy.io/run=crasher -o jsonpath='{range .items[?(@.status.closed!=true)]}{.metadata.name} {end}')"
[ -z "$open" ] || fail "the failed run left open lease(s): $open"
reason="$(kubectl get leases.rq.davidlangworthy.io -n default \
  -l rq.davidlangworthy.io/run=crasher -o jsonpath='{.items[0].status.closureReason}')"
[ "$reason" = "WorkloadFailed" ] || fail "lease closure reason = '$reason', want WorkloadFailed"

echo "PASS: a real container exit(1) failed the run and closed its leases (WorkloadFailed) — the zombie is dead."
