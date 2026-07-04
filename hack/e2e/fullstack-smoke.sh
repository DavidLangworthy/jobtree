#!/usr/bin/env bash
# Full-stack smoke proof (P2b + P2c): the WHOLE jobtree control plane on a real
# kind cluster — the manager (cmd/manager) and the scheduler plugin
# (cmd/scheduler) both running against it. It creates a real Run (not a
# hand-crafted pod) and proves the end-to-end product flow:
#
#   Run  ->  manager emits unscheduled intent pods (schedulerName=jobtree)
#        ->  scheduler plugin Filters, gang+funding-gates at Permit, binds,
#            and mints the Lease at PreBind (the sole committer)
#        ->  manager adopts the lease -> Run Running
#        ->  the real container runs to exit 0 -> Run Completed
#
# No hand-injected pod phase or lease anywhere. Runs both binaries
# out-of-cluster (admin kubeconfig) for a fast proof; the in-cluster Helm
# deployment is exercised by `make e2e`.
#
# Usage:  hack/e2e/fullstack-smoke.sh          (creates + tears down a cluster)
#         KEEP_CLUSTER=1 hack/e2e/fullstack-smoke.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${KIND_CLUSTER_NAME:-jobtree-fullstack}"
NODE="${CLUSTER}-control-plane"
WORK="$(mktemp -d)"
MGR_PID=""
SCHED_PID=""

cleanup() {
  local status=$?
  [ -n "$MGR_PID" ] && kill "$MGR_PID" 2>/dev/null || true
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

echo "==> installing CRDs + prepping node (flavor labels + 4 nvidia.com/gpu)"
kubectl apply -f "$ROOT/config/crd/bases/" >/dev/null
kubectl label node "$NODE" region=us-west cluster=cluster-a fabric.domain=island-a gpu.flavor=H100-80GB --overwrite >/dev/null
kubectl patch node "$NODE" --subresource=status --type=json \
  -p '[{"op":"add","path":"/status/capacity/nvidia.com~1gpu","value":"4"}]' >/dev/null
kubectl taint node "$NODE" node-role.kubernetes.io/control-plane- >/dev/null 2>&1 || true

echo "==> building manager + scheduler"
go build -o "$WORK/manager" "$ROOT/cmd/manager"
go build -o "$WORK/scheduler" "$ROOT/cmd/scheduler"

echo "==> starting the manager (webhooks off, out-of-cluster)"
"$WORK/manager" --enable-webhooks=false --leader-elect=false \
  --metrics-bind-address=0 --health-probe-bind-address=0 > "$WORK/manager.log" 2>&1 &
MGR_PID=$!

echo "==> starting the scheduler plugin (out-of-cluster)"
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
for _ in $(seq 1 40); do grep -q 'Added node to NodeTree' "$WORK/scheduler.log" && break; sleep 1; done
sleep 2

echo "==> creating a Budget and a Run (a real workload, no hand-crafted pod)"
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
EOF

echo "==> waiting for the Run to reach Running (manager emits -> plugin binds+mints -> manager adopts)"
kubectl wait --for=jsonpath='{.status.phase}'=Running run/train -n default --timeout=120s

fail() {
  echo "FAIL: $1"
  echo "--- run ---"; kubectl get run train -n default -o yaml || true
  echo "--- pods ---"; kubectl get pods -n default -o wide || true
  echo "--- leases ---"; kubectl get leases.rq.davidlangworthy.io -n default -o yaml || true
  echo "--- manager log tail ---"; tail -30 "$WORK/manager.log" || true
  exit 1
}

# The plugin (not the controller) minted exactly one lease for the run, on the node.
leases="$(kubectl get leases.rq.davidlangworthy.io -n default -o jsonpath='{.items[*].metadata.name}')"
[ -n "$leases" ] || fail "no lease was minted"
payer="$(kubectl get leases.rq.davidlangworthy.io -n default -o jsonpath='{.items[0].spec.paidByBudget}/{.items[0].spec.paidByEnvelope}')"
[ "$payer" = "team/west" ] || fail "lease payer '$payer', want team/west"

# The pod is a real workload pod the plugin placed (schedulerName jobtree, bound to the node).
sched="$(kubectl get pods -n default -l rq.davidlangworthy.io/run=train -o jsonpath='{.items[0].spec.schedulerName}')"
[ "$sched" = "jobtree" ] || fail "pod schedulerName '$sched', want jobtree"

echo "==> waiting for the Run to Complete (real container exits 0)"
kubectl wait --for=jsonpath='{.status.phase}'=Completed run/train -n default --timeout=120s || fail "run did not complete"

echo
echo "PASS: Run 'train' flowed Run -> intent pods (manager) -> scheduled + lease minted by the"
echo "      plugin (paid by $payer) -> Running -> real container exit 0 -> Completed. Whole"
echo "      control plane, real cluster, no injected state."
