#!/usr/bin/env bash
# Plugin smoke proof (PLUGIN-2): stands up a kind cluster, runs the real jobtree
# scheduler binary against it, and proves the plugin actually SCHEDULES a GPU pod
# and MINTS its jobtree Lease — then the real container runs to exit 0. No
# hand-injected pod phase, no controller-minted lease: the scheduler plugin is
# the sole committer (docs/project/borrow-vs-build.md §9).
#
# This runs the scheduler out-of-cluster (admin kubeconfig, no RBAC/image
# needed) — the fast path to prove the plugin. The in-cluster deployment
# (image + Helm + RBAC to create leases in the workload namespace) is the P2c
# follow-on that makes this a CI e2e.
#
# Usage:  hack/e2e/plugin-smoke.sh          (creates + tears down a cluster)
#         KEEP_CLUSTER=1 hack/e2e/plugin-smoke.sh   (leave it up for inspection)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${KIND_CLUSTER_NAME:-jobtree-plugin-smoke}"
NODE="${CLUSTER}-control-plane"
WORK="$(mktemp -d)"
SCHED_PID=""

cleanup() {
  local status=$?
  [ -n "$SCHED_PID" ] && kill "$SCHED_PID" 2>/dev/null || true
  if [ "${KEEP_CLUSTER:-0}" != "1" ]; then
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
  exit $status
}
trap cleanup EXIT

echo "==> creating kind cluster $CLUSTER"
kind create cluster --name "$CLUSTER" --wait 90s >/dev/null

echo "==> installing CRDs"
kubectl apply -f "$ROOT/config/crd/bases/" >/dev/null

echo "==> prepping node $NODE (flavor labels + advertise 4 nvidia.com/gpu)"
kubectl label node "$NODE" region=us-west cluster=cluster-a fabric.domain=island-a gpu.flavor=H100-80GB --overwrite >/dev/null
kubectl patch node "$NODE" --subresource=status --type=json \
  -p '[{"op":"add","path":"/status/capacity/nvidia.com~1gpu","value":"4"}]' >/dev/null
kubectl taint node "$NODE" node-role.kubernetes.io/control-plane- >/dev/null 2>&1 || true

echo "==> building + starting the jobtree scheduler (out-of-cluster)"
go build -o "$WORK/scheduler" "$ROOT/cmd/scheduler"
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
# Wait for the scheduler to be serving AND to have synced its node cache, so the
# pod's first scheduling attempt sees the advertised nvidia.com/gpu (otherwise
# the first try fails "Insufficient nvidia.com/gpu" and only a retry binds).
for _ in $(seq 1 40); do grep -q 'Added node to NodeTree' "$WORK/scheduler.log" && break; sleep 1; done
sleep 2

echo "==> creating Budget, Run, and an unscheduled GPU pod (schedulerName=jobtree)"
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
metadata: { name: train, namespace: default }
spec:
  owner: org:ai:team
  resources: { gpuType: H100-80GB, totalGPUs: 1 }
---
apiVersion: v1
kind: Pod
metadata:
  name: train-pod-0
  namespace: default
  labels: { rq.davidlangworthy.io/run: train, rq.davidlangworthy.io/role: Active }
  annotations:
    rq.davidlangworthy.io/gpus: "1"
    rq.davidlangworthy.io/expected-width: "1"
    rq.davidlangworthy.io/flavor: H100-80GB
spec:
  schedulerName: jobtree
  restartPolicy: Never
  containers:
    - name: workload
      image: busybox:1.36
      command: ["sh", "-c", "echo jobtree-workload-ran; true"]
      resources:
        requests: { nvidia.com/gpu: "1" }
        limits:   { nvidia.com/gpu: "1" }
EOF

echo "==> waiting for the pod to be scheduled and to complete"
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded pod/train-pod-0 -n default --timeout=120s

fail() { echo "FAIL: $1"; kubectl get pod train-pod-0 -n default -o yaml; exit 1; }

node="$(kubectl get pod train-pod-0 -n default -o jsonpath='{.spec.nodeName}')"
[ "$node" = "$NODE" ] || fail "pod bound to '$node', want '$NODE'"

leases="$(kubectl get gpuleases.rq.davidlangworthy.io -n default -o jsonpath='{.items[*].metadata.name}')"
[ "$leases" = "train-pod-0-lease" ] || fail "expected one lease 'train-pod-0-lease', got '$leases'"
payer="$(kubectl get lease.rq.davidlangworthy.io train-pod-0-lease -n default -o jsonpath='{.spec.paidByBudget}/{.spec.paidByEnvelope}')"
[ "$payer" = "team/west" ] || fail "lease payer '$payer', want 'team/west'"

echo
echo "PASS: plugin scheduled the pod to $node, minted lease train-pod-0-lease (paid by $payer),"
echo "      and the real container ran to exit 0 (Succeeded) — sole committer, no injected state."
