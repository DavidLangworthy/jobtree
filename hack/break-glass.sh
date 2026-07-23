#!/usr/bin/env bash
#
# break-glass.sh — give the cluster back to its operator when jobtree is the problem.
#
# Three independent levers, in the order you almost always want them (R18):
#
#   gpu-scheduling   Delete the R6 ValidatingAdmissionPolicyBinding, so GPU pods no
#                    longer HAVE to use schedulerName: jobtree. This is the single
#                    most important one: without it, a wedged jobtree means nobody in
#                    the cluster can start a GPU pod at all.
#   committing       Scale the jobtree scheduler Deployment to 0, so nothing new is
#                    bound or funded. Pods that already say schedulerName: jobtree
#                    will PEND — that is the intended, visible outcome; repoint
#                    urgent work with `kubectl ... --type=json` on schedulerName,
#                    which the first lever has just made legal again.
#   crd-writes       Flip the CRD webhooks to failurePolicy=Ignore, so Run/Budget/
#                    GPULease writes stop being blocked by a webhook that is down or
#                    wrong. Cross-object validation is GONE until you undo this.
#
# What this deliberately does NOT do: it never scales down or deletes the CONTROLLER
# manager. The manager is what closes leases, and an open lease charges a budget and
# holds GPUs for as long as it stays open. Leaving it running keeps the accounting
# honest while you debug. If the manager itself is the faulting component, scale it
# down by hand and know that you are stopping the ledger.
#
# Usage:
#   hack/break-glass.sh gpu-scheduling
#   hack/break-glass.sh committing crd-writes
#   hack/break-glass.sh all
#   hack/break-glass.sh --undo committing crd-writes
#   hack/break-glass.sh --dry-run all
#
# Options:
#   -n, --namespace NS   jobtree's namespace (default: jobtree-system)
#   -r, --release NAME   Helm release name; only needed if several are installed
#       --dry-run        print what would run, change nothing
#       --undo           reverse `committing` and `crd-writes`
#       --replicas N     replicas to restore with --undo committing (default 1)
#       --probe-namespace NS  where `gpu-scheduling` dry-runs its probe pod (default:
#                        default). Must not be a namespace the policy exempts.
#
# Objects are found by the chart's own label (app.kubernetes.io/instance), never by a
# hardcoded name — the chart prefixes most names with the release, so a hardcoded name
# is wrong for every install but one. That is not hypothetical: R18's spec named a
# binding `jobtree-gpu-mandatory`, which has never existed.

set -euo pipefail

NAMESPACE="jobtree-system"
RELEASE=""
DRY_RUN=0
UNDO=0
REPLICAS=1
# The namespace the gpu-scheduling lever dry-runs its probe pod in. It must NOT be one
# the policy exempts (the release namespace always is), or the probe would be admitted
# whether or not the lever worked.
PROBE_NAMESPACE="${PROBE_NAMESPACE:-default}"
LEVERS=()

die() { echo "break-glass: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    -n|--namespace) NAMESPACE="${2:?--namespace needs a value}"; shift 2 ;;
    -r|--release)   RELEASE="${2:?--release needs a value}"; shift 2 ;;
    --replicas)     REPLICAS="${2:?--replicas needs a value}"; shift 2 ;;
    --probe-namespace) PROBE_NAMESPACE="${2:?--probe-namespace needs a value}"; shift 2 ;;
    --dry-run)      DRY_RUN=1; shift ;;
    --undo)         UNDO=1; shift ;;
    -h|--help)      sed -n '2,45p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    all)            LEVERS+=(gpu-scheduling committing crd-writes); shift ;;
    gpu-scheduling|committing|crd-writes) LEVERS+=("$1"); shift ;;
    *)              die "unknown argument '$1' (try --help)" ;;
  esac
done

[ ${#LEVERS[@]} -gt 0 ] || die "name at least one lever: gpu-scheduling | committing | crd-writes | all"
command -v kubectl >/dev/null 2>&1 || die "kubectl is not on PATH"

run() {
  if [ "$DRY_RUN" = 1 ]; then
    printf 'would run: %s\n' "$*"
  else
    echo "+ $*"
    "$@"
  fi
}

# selector returns the label selector that identifies this install's objects.
selector() {
  if [ -n "$RELEASE" ]; then
    echo "app.kubernetes.io/instance=$RELEASE"
  else
    echo "app.kubernetes.io/managed-by=Helm,app.kubernetes.io/component=scheduler"
  fi
}

# names lists the objects of KIND that belong to this install. Cluster-scoped kinds
# take no namespace; namespaced ones do.
names() {
  local kind="$1"
  local scope="${2:-cluster}"
  local args
  args=(get "$kind" -l "$(selector)" -o "jsonpath={range .items[*]}{.metadata.name}{'\n'}{end}")
  if [ "$scope" = "namespaced" ]; then
    args+=(-n "$NAMESPACE")
  fi
  kubectl "${args[@]}" 2>/dev/null || true
}

require_some() {
  local what="$1" list="$2"
  if [ -z "$list" ]; then
    echo "break-glass: no $what found for selector '$(selector)' — nothing to do." >&2
    echo "             (Is --release right? \`helm list -A\` shows the installs.)" >&2
    return 1
  fi
  return 0
}

lever_gpu_scheduling() {
  if [ "$UNDO" = 1 ]; then
    cat >&2 <<EOF
break-glass: --undo does not re-create the policy binding, on purpose.

The binding is owned by the Helm release, so re-creating it by hand would leave an
object Helm does not know it owns. Restore it with the release itself:

    helm upgrade <release> <chart> -n $NAMESPACE --set podPolicy.enabled=true

Do that only once you are sure jobtree can schedule again — re-enabling the policy is
what makes GPU pods depend on jobtree once more.
EOF
    return 0
  fi

  local bindings; bindings="$(names validatingadmissionpolicybinding)"
  require_some "ValidatingAdmissionPolicyBinding" "$bindings" || return 0
  local b
  for b in $bindings; do
    run kubectl delete validatingadmissionpolicybinding "$b"
  done
  echo "The policy object itself is left in place; only the BINDING enforces it, so"
  echo "re-enabling later is one step."
  [ "$DRY_RUN" = 1 ] && return 0
  wait_until_gpu_pods_are_admitted
}

# wait_until_gpu_pods_are_admitted blocks until a GPU pod on the default scheduler is
# actually admitted.
#
# This is not belt-and-braces. The apiserver evaluates admission policies from a cached
# snapshot that refreshes on its own schedule, so for a few seconds AFTER the binding is
# deleted, GPU pods are still denied — by name, by a binding that no longer exists.
# Measured on kind: the delete returns, the very next create is refused quoting the
# deleted binding, and a moment later the same create succeeds.
#
# An operator in an incident types the lever, retries immediately, sees the same denial
# and concludes the break-glass does not work. That is the single worst outcome this
# script can produce, so the script waits for its own effect instead of announcing one
# it has not yet caused.
wait_until_gpu_pods_are_admitted() {
  local probe deadline out
  probe="jobtree-break-glass-probe-$$"
  deadline=$(( $(date +%s) + 90 ))
  echo -n "waiting for the apiserver to stop enforcing the policy"
  while :; do
    if out="$(probe_manifest "$probe" | kubectl apply --dry-run=server -f - 2>&1)"; then
      echo " — done."
      echo "GPU pods may now use any scheduler."
      return 0
    fi
    # Only OUR policy is worth waiting out. Anything else (RBAC, a quota, another
    # admission plugin) will not resolve itself, and pretending to wait would hide it.
    if ! grep -qE "ValidatingAdmissionPolicy|schedulerName" <<<"$out"; then
      echo
      echo "break-glass: the probe pod was refused for an unrelated reason; the policy" >&2
      echo "             binding IS deleted, so the lever itself is done:" >&2
      echo "$out" | sed 's/^/               /' >&2
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo
      echo "break-glass: 90s after deleting the binding, GPU pods are STILL denied:" >&2
      echo "$out" | sed 's/^/               /' >&2
      echo "             Check for another binding outside this release:" >&2
      echo "               kubectl get validatingadmissionpolicybinding" >&2
      return 1
    fi
    echo -n "."
    sleep 2
  done
}

# A pod that the R6 policy exists to refuse: it requests a GPU and does not name the
# jobtree scheduler. Server-side dry run only — nothing is ever created.
probe_manifest() {
  cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $1
  namespace: ${PROBE_NAMESPACE}
spec:
  restartPolicy: Never
  containers:
    - name: probe
      image: registry.k8s.io/pause:3.10
      resources:
        limits:
          nvidia.com/gpu: "1"
EOF
}

lever_committing() {
  local deploys; deploys="$(names deployment namespaced)"
  require_some "Deployment" "$deploys" || return 0
  local d found=0
  for d in $deploys; do
    case "$d" in
      *scheduler*) found=1 ;;
      *) continue ;;
    esac
    if [ "$UNDO" = 1 ]; then
      run kubectl scale deployment "$d" -n "$NAMESPACE" --replicas="$REPLICAS"
    else
      run kubectl scale deployment "$d" -n "$NAMESPACE" --replicas=0
    fi
  done
  if [ "$found" = 0 ]; then
    echo "break-glass: no scheduler Deployment in $NAMESPACE — the plugin may not be installed" >&2
    return 0
  fi
  if [ "$UNDO" = 0 ]; then
    echo "Nothing will be bound or funded from now on. Pods with schedulerName: jobtree"
    echo "will PEND until the scheduler is back or they are repointed."
  fi
}

lever_crd_writes() {
  local policy='Ignore'
  [ "$UNDO" = 1 ] && policy='Fail'

  local vwcs mwcs; vwcs="$(names validatingwebhookconfiguration)"; mwcs="$(names mutatingwebhookconfiguration)"
  if [ -z "$vwcs$mwcs" ]; then
    echo "break-glass: no webhook configurations found for selector '$(selector)'" >&2
    return 0
  fi

  # Patch every webhook in the configuration by index; a configuration holds one
  # entry per Kind, and a partial flip would leave one Kind still blocked.
  local cfg kind n i patch
  for kind in validatingwebhookconfiguration mutatingwebhookconfiguration; do
    for cfg in $(names "$kind"); do
      n="$(kubectl get "$kind" "$cfg" -o 'jsonpath={.webhooks[*].name}' | wc -w)"
      patch='['
      for ((i = 0; i < n; i++)); do
        [ "$i" -gt 0 ] && patch+=','
        patch+="{\"op\":\"replace\",\"path\":\"/webhooks/$i/failurePolicy\",\"value\":\"$policy\"}"
      done
      patch+=']'
      [ "$n" -gt 0 ] && run kubectl patch "$kind" "$cfg" --type=json -p "$patch"
    done
  done

  if [ "$UNDO" = 1 ]; then
    echo "Cross-object validation is enforced again."
  else
    cat <<'EOF'
Run/Budget/GPULease writes are unblocked. Until you undo this, NOTHING checks the
cross-object rules the webhook alone can express — an aggregate cap may name an
envelope that does not exist, and a run may follow itself. Field-level rules and
lease immutability still hold: R14 put those in the CRD schema precisely so this
lever would not turn validation off entirely.
EOF
  fi
}

for lever in "${LEVERS[@]}"; do
  echo "== $lever$([ "$UNDO" = 1 ] && echo ' (undo)')"
  case "$lever" in
    gpu-scheduling) lever_gpu_scheduling ;;
    committing)     lever_committing ;;
    crd-writes)     lever_crd_writes ;;
  esac
  echo
done
