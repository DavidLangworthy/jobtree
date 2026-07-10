#!/usr/bin/env bash
# Distributed-training rendezvous live proof (R9 9A-1 + 9A-2): a real 2-node kind
# cluster, a width-2 gang, and the two ranks actually finding each other over the
# injected rendezvous env + the per-run headless Service DNS — no torch needed, a
# busybox `nc` handshake is a faithful proof that the address plumbing works:
#
#   Run (2 active GPUs, 1 per node) -> two Active pods, one per worker node
#     -> 9A-1: each pod gets hostname=<pod> + subdomain=<run>; the manager creates
#        the headless Service <run>, so <run>-active-0.<run>.<ns>.svc resolves to
#        rank 0's pod (publishNotReadyAddresses, so it resolves DURING rendezvous)
#     -> 9A-2: each container gets MASTER_ADDR=<run>-active-0.<run>.<ns>.svc,
#        MASTER_PORT, WORLD_SIZE=2, NNODES=2, NODE_RANK=<its ordinal>
#     -> rank 0 listens on MASTER_PORT; rank 1 dials MASTER_ADDR:MASTER_PORT until
#        it connects. Both exit 0 => the whole gang Succeeds => Run Completed
#
# If either the DNS (9A-1) or the env (9A-2) were wrong, rank 1 could never reach
# rank 0 and the run would never complete. Run Completed IS the rendezvous.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER="${KIND_CLUSTER_NAME:-jobtree-rendezvous}"
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

echo "==> kind up (2 workers) + CRDs"
cat > "$WORK/kind.yaml" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
EOF
kind create cluster --name "$CLUSTER" --config "$WORK/kind.yaml" --wait 120s >/dev/null
kubectl apply -f "$ROOT/config/crd/bases/" >/dev/null

# One nvidia.com/gpu per node forces the two ranks onto separate nodes, so the
# rendezvous genuinely crosses the network (and its DNS) rather than localhost.
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

echo "==> creating a Budget and a width-2 Run whose ranks rendezvous over DNS"
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
metadata: { name: ddp, namespace: default }
spec:
  owner: org:ai:team
  resources: { gpuType: H100-80GB, totalGPUs: 2 }
  roles:
    - name: worker
      width: 2
      gpusPerPod: 1
      template:
        spec:
          containers:
            - name: workload
              image: busybox:1.36
              command:
                - sh
                - -c
                - |
                  echo "rank=$NODE_RANK world=$WORLD_SIZE nnodes=$NNODES master=$MASTER_ADDR:$MASTER_PORT"
                  if [ "$NODE_RANK" = "0" ]; then
                    echo "rank 0 listening"; echo ready | nc -l -p "$MASTER_PORT"; echo "rank 0 met a peer"
                  else
                    echo "rank $NODE_RANK dialing $MASTER_ADDR"
                    for i in $(seq 1 60); do
                      if nc -w 2 "$MASTER_ADDR" "$MASTER_PORT" < /dev/null; then echo "rank $NODE_RANK rendezvoused"; exit 0; fi
                      echo "retry $i"; sleep 2
                    done
                    echo "rank $NODE_RANK could not reach the master"; exit 1
                  fi
EOF

echo "==> waiting for Running (both ranks bound), then for the rendezvous to complete the run"
kubectl wait --for=jsonpath='{.status.phase}'=Running run/ddp -n default --timeout=180s || fail "run never reached Running"

# The manager created the per-run headless Service (9A-1).
kubectl get service ddp -n default >/dev/null 2>&1 || fail "the per-run headless Service 'ddp' was not created (9A-1)"

# Both ranks exit 0 only if rank 1 reached rank 0 over MASTER_ADDR (9A-2 env +
# 9A-1 DNS). Run Completed is the rendezvous.
kubectl wait --for=jsonpath='{.status.phase}'=Completed run/ddp -n default --timeout=240s \
  || fail "the gang never rendezvoused/completed; phase=$(kubectl get run ddp -n default -o jsonpath='{.status.phase}')"

echo "PASS: two ranks on two nodes found each other over the injected MASTER_ADDR + headless DNS and completed — rendezvous is real."
