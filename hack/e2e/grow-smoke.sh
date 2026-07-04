#!/usr/bin/env bash
# Elastic-grow live proof (CASCADE-2): the whole control plane on a real kind
# cluster (manager + scheduler plugin), growing a malleable Run and proving the
# grow flows through the single committer:
#
#   malleable Run (base 2 GPUs, desired 4)
#     -> manager admits the base; plugin binds + mints -> Running at width 2
#     -> reconcileElasticRun emits a +2 GROW COHORT of unscheduled intent pods
#     -> plugin funds the DELTA incrementally (cohort gang) and mints "Grow"
#        leases on the free capacity -> width grows to 4
#
# No controller mint anywhere; the plugin is the sole committer of the grow too.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${KIND_CLUSTER_NAME:-jobtree-grow}"
NODE="${CLUSTER}-control-plane"
WORK="$(mktemp -d)"
MGR_PID=""
SCHED_PID=""

cleanup() {
  local status=$?
  [ -n "$MGR_PID" ] && kill "$MGR_PID" 2>/dev/null || true
  [ -n "$SCHED_PID" ] && kill "$SCHED_PID" 2>/dev/null || true
  [ "${KEEP_CLUSTER:-0}" != "1" ] && kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$WORK"
  exit $status
}
trap cleanup EXIT

echo "==> kind up + CRDs + node (flavor labels + 4 nvidia.com/gpu, room to grow)"
kind create cluster --name "$CLUSTER" --wait 90s >/dev/null
kubectl apply -f "$ROOT/config/crd/bases/" >/dev/null
kubectl label node "$NODE" region=us-west cluster=cluster-a fabric.domain=island-a gpu.flavor=H100-80GB --overwrite >/dev/null
kubectl patch node "$NODE" --subresource=status --type=json \
  -p '[{"op":"add","path":"/status/capacity/nvidia.com~1gpu","value":"4"}]' >/dev/null
kubectl taint node "$NODE" node-role.kubernetes.io/control-plane- >/dev/null 2>&1 || true

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
for _ in $(seq 1 40); do grep -q 'Added node to NodeTree' "$WORK/scheduler.log" && break; sleep 1; done
sleep 2

echo "==> creating a Budget and a malleable Run (base 2 GPUs, desired 4, long-running role)"
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
metadata: { name: trainer, namespace: default }
spec:
  owner: org:ai:team
  resources: { gpuType: H100-80GB, totalGPUs: 2 }
  malleable: { minTotalGPUs: 2, maxTotalGPUs: 4, stepGPUs: 2, desiredTotalGPUs: 4 }
  roles:
    - name: trainer
      width: 2
      gpusPerPod: 1
      template:
        spec:
          containers:
            - name: workload
              image: busybox:1.36
              command: ["sh", "-c", "sleep 900"]
EOF

echo "==> waiting for the Run to reach Running (base 2 GPUs)"
kubectl wait --for=jsonpath='{.status.phase}'=Running run/trainer -n default --timeout=120s

fail() {
  echo "FAIL: $1"
  kubectl get run trainer -n default -o yaml || true
  kubectl get pods -n default -o wide || true
  kubectl get leases.rq.davidlangworthy.io -n default -o custom-columns='NAME:.metadata.name,GPUS:.spec.slice.nodes,REASON:.spec.reason' || true
  exit 1
}

echo "==> waiting for the elastic grow to width 4 (plugin funds the +2 delta cohort)"
for _ in $(seq 1 60); do
  alloc="$(kubectl get run trainer -n default -o jsonpath='{.status.width.allocated}' 2>/dev/null || true)"
  [ "$alloc" = "4" ] && break
  sleep 2
done
alloc="$(kubectl get run trainer -n default -o jsonpath='{.status.width.allocated}')"
[ "$alloc" = "4" ] || fail "run allocated width = '$alloc', want 4 after grow"

# The grow was minted by the plugin as "Grow"-reason leases, not by the controller.
grows="$(kubectl get leases.rq.davidlangworthy.io -n default -o jsonpath='{range .items[*]}{.spec.reason}{"\n"}{end}' | grep -c '^Grow$' || true)"
[ "$grows" -ge 1 ] || fail "expected >=1 plugin-minted Grow lease, got $grows"

echo
echo "PASS: malleable Run trainer went Running at 2 GPUs, then the plugin funded the +2"
echo "      grow-cohort delta and minted $grows Grow lease(s) -> allocated width 4. The plugin"
echo "      is the sole committer of the grow too; no controller mint, no injected state."
