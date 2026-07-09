#!/usr/bin/env bash
# Node-failure swap live proof (CASCADE-3 + CASCADE-3b): the whole control plane
# on a real 2-node kind cluster (manager + scheduler plugin), holding a spare
# and swapping onto it when a node fails — provenance preserved, plugin as the
# sole committer:
#
#   Run (1 active GPU + 1 spare), 1 GPU per node
#     -> manager emits an Active intent pod AND a held RoleSpare intent pod
#     -> plugin binds both and mints an Active lease (node X) + a RoleSpare lease
#        (node Y) from the run's cover -> Running, spare HELD live
#     -> cordon node X: assert NOTHING happens (a cordon is not a failure, R21)
#     -> delete node X => the manager's NodeReconciler runs
#        HandleNodeFailure: closes the active + spare leases, deletes the held
#        spare pod on node Y, and emits a SWAP pod hard-targeted at node Y,
#        stamped with the spare's funding provenance
#     -> plugin binds the swap on node Y and mints a "Swap" lease FROM THAT
#        PROVENANCE (owner/budget/envelope preserved) -> Run stays Running
#
# No controller mint anywhere; the plugin is the sole committer of the swap too.
# This is the live proof that CASCADE-3b's held spares make a real swap landable.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${KIND_CLUSTER_NAME:-jobtree-swap}"
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

echo "==> kind up (2 nodes) + CRDs"
cat > "$WORK/kind.yaml" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
EOF
kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --wait 120s >/dev/null
kubectl apply -f "$ROOT/config/crd/bases/" >/dev/null

# Both nodes carry the flavor labels + exactly 1 nvidia.com/gpu, so the 1 active
# and 1 spare GPU are forced onto different nodes.
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
  [ "$(grep -c 'Added node to NodeTree' "$WORK/scheduler.log")" -ge 2 ] && break
  sleep 1
done
sleep 2

echo "==> creating a Budget and a Run (1 active GPU + 1 held spare)"
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
metadata: { name: resilient, namespace: default }
spec:
  owner: org:ai:team
  resources: { gpuType: H100-80GB, totalGPUs: 1 }
  sparesPerGroup: 1
  roles:
    - name: trainer
      width: 1
      gpusPerPod: 1
      template:
        spec:
          containers:
            - name: workload
              image: busybox:1.36
              command: ["sh", "-c", "sleep 900"]
EOF

fail() {
  echo "FAIL: $1"
  kubectl get run resilient -n default -o yaml || true
  kubectl get pods -n default -o wide || true
  kubectl get leases.rq.davidlangworthy.io -n default \
    -o custom-columns='NAME:.metadata.name,ROLE:.spec.slice.role,NODES:.spec.slice.nodes,REASON:.spec.reason,OWNER:.spec.owner,ENV:.spec.paidByEnvelope,CLOSED:.status.closed' || true
  exit 1
}

echo "==> waiting for the Run to reach Running (active bound + spare held)"
kubectl wait --for=jsonpath='{.status.phase}'=Running run/resilient -n default --timeout=120s || fail "run never reached Running"

# The spare must be HELD live: a RoleSpare lease minted by the plugin.
for _ in $(seq 1 30); do
  spares="$(kubectl get leases.rq.davidlangworthy.io -n default \
    -o jsonpath='{range .items[?(@.spec.slice.role=="Spare")]}{.metadata.name}{"\n"}{end}' | grep -c . || true)"
  [ "$spares" -ge 1 ] && break
  sleep 2
done
[ "${spares:-0}" -ge 1 ] || fail "no held RoleSpare lease — CASCADE-3b spare not materialized live"

# Identify the active node (to fail) and the spare node (to swap onto).
active_node="$(kubectl get leases.rq.davidlangworthy.io -n default \
  -o jsonpath='{range .items[?(@.spec.slice.role=="Active")]}{.spec.slice.nodes[0]}{"\n"}{end}' | head -1 | cut -d'#' -f1)"
spare_node="$(kubectl get leases.rq.davidlangworthy.io -n default \
  -o jsonpath='{range .items[?(@.spec.slice.role=="Spare")]}{.spec.slice.nodes[0]}{"\n"}{end}' | head -1 | cut -d'#' -f1)"
[ -n "$active_node" ] || fail "could not find the active lease's node"
[ -n "$spare_node" ] || fail "could not find the spare lease's node"
[ "$active_node" != "$spare_node" ] || fail "active and spare landed on the same node ($active_node)"
echo "    active on $active_node, spare held on $spare_node"

# Capture the run's funding provenance to prove the swap preserves it.
want_owner="$(kubectl get leases.rq.davidlangworthy.io -n default \
  -o jsonpath='{range .items[?(@.spec.slice.role=="Spare")]}{.spec.owner}{"\n"}{end}' | head -1)"
want_env="$(kubectl get leases.rq.davidlangworthy.io -n default \
  -o jsonpath='{range .items[?(@.spec.slice.role=="Spare")]}{.spec.paidByEnvelope}{"\n"}{end}' | head -1)"

# A CORDON IS NOT A FAILURE (R21). This script used to cordon the active node and
# expect a swap -- which is the corruption: the original pod keeps running on a
# cordoned node, so the swap starts a SECOND live copy of the same rank. Prove the
# no-op first, then fail the node for real by deleting it.
echo "==> cordoning the active node ($active_node): this must NOT trigger a swap"
kubectl cordon "$active_node" >/dev/null
sleep 10
if kubectl get leases.rq.davidlangworthy.io -n default \
     -o jsonpath='{range .items[?(@.spec.reason=="Swap")]}{.metadata.name}{"\n"}{end}' | grep -q .; then
  fail "a bare cordon triggered a swap -- R21 has regressed, and two copies of a rank may now be live"
fi
echo "    ok: cordon changed nothing"

echo "==> failing the active node ($active_node) for real: delete the Node => HandleNodeFailure => swap onto the spare"
kubectl delete node "$active_node" --wait=false >/dev/null

echo "==> waiting for the plugin to mint the Swap lease on $spare_node (from the spare's provenance)"
swap_name=""
for _ in $(seq 1 60); do
  swap_name="$(kubectl get leases.rq.davidlangworthy.io -n default \
    -o jsonpath='{range .items[?(@.spec.reason=="Swap")]}{.metadata.name}{"|"}{.status.closed}{"\n"}{end}' \
    | awk -F'|' '$2!="true"{print $1; exit}')"
  [ -n "$swap_name" ] && break
  sleep 2
done
[ -n "$swap_name" ] || fail "no open plugin-minted Swap lease appeared after node failure"

swap_node="$(kubectl get lease.rq.davidlangworthy.io "$swap_name" -n default -o jsonpath='{.spec.slice.nodes[0]}' | cut -d'#' -f1)"
swap_owner="$(kubectl get lease.rq.davidlangworthy.io "$swap_name" -n default -o jsonpath='{.spec.owner}')"
swap_env="$(kubectl get lease.rq.davidlangworthy.io "$swap_name" -n default -o jsonpath='{.spec.paidByEnvelope}')"

[ "$swap_node" = "$spare_node" ] || fail "swap lease landed on '$swap_node', want the spare node '$spare_node'"
[ "$swap_owner" = "$want_owner" ] || fail "swap owner '$swap_owner' != spare provenance owner '$want_owner'"
[ "$swap_env" = "$want_env" ] || fail "swap envelope '$swap_env' != spare provenance envelope '$want_env'"

# The run keeps running through the swap.
phase="$(kubectl get run resilient -n default -o jsonpath='{.status.phase}')"
[ "$phase" = "Running" ] || fail "run phase '$phase' after swap, want Running"

# The held spare's pod on the swap node was freed (its lease closed with reason Swap).
spare_closed="$(kubectl get leases.rq.davidlangworthy.io -n default \
  -o jsonpath='{range .items[?(@.spec.slice.role=="Spare")]}{.status.closureReason}{"\n"}{end}' | grep -c '^Swap$' || true)"
[ "$spare_closed" -ge 1 ] || fail "spare lease was not closed with reason Swap"

echo
echo "PASS: Run resilient held a live RoleSpare lease on $spare_node; failing $active_node drove"
echo "      HandleNodeFailure to reclaim + emit a swap pod, and the PLUGIN minted the Swap lease"
echo "      $swap_name on $spare_node preserving provenance (owner=$swap_owner envelope=$swap_env)."
echo "      Run stayed Running. The plugin is the sole committer of the swap; no injected state."
