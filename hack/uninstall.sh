#!/usr/bin/env bash
#
# uninstall.sh — remove jobtree in the one order that does not orphan anything (R18).
#
# The order exists because of what a GPULease is. An OPEN lease charges a budget and
# holds GPUs; the ledger is a fold over leases. So the teardown has exactly two
# obligations, and they point in opposite directions:
#
#   1. Every open lease must be CLOSED before the accounting goes away, or the last
#      thing the ledger ever recorded is a fiction — work that ran forever.
#   2. The thing that closes leases is the Run finalizer, which needs the manager
#      alive to run. Scale the manager down first and every Run wedges Terminating.
#
# Hence: delete Runs and let the finalizer drain WHILE the manager is still up; only
# then take the workloads away; only then, and only if you ask, the CRDs.
#
# Deleting the CRDs deletes every Budget, GPULease and Reservation with them — the
# entire usage history. That is off by default and needs --delete-crds.
#
# Usage:
#   hack/uninstall.sh --release jobtree                      # keep the CRDs + ledger
#   hack/uninstall.sh --release jobtree --delete-crds --yes  # take everything
#   hack/uninstall.sh --release jobtree --dry-run
#
# Options:
#   -r, --release NAME    Helm release to uninstall (required unless --no-helm)
#   -n, --namespace NS    release namespace (default: jobtree-system)
#       --no-helm         do not call helm; delete the workloads by label instead
#       --delete-crds     also delete the four CRDs AND every object of those kinds
#       --force           proceed past open leases that would not drain
#       --drain-timeout S seconds to wait for Run finalizers (default 300)
#       --yes             do not prompt
#       --dry-run         print what would run, change nothing

set -euo pipefail

NAMESPACE="jobtree-system"
RELEASE=""
USE_HELM=1
DELETE_CRDS=0
FORCE=0
ASSUME_YES=0
DRY_RUN=0
DRAIN_TIMEOUT=300

CRDS=(
  runs.rq.davidlangworthy.io
  gpuleases.rq.davidlangworthy.io
  budgets.rq.davidlangworthy.io
  reservations.rq.davidlangworthy.io
)

die() { echo "uninstall: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    -r|--release)     RELEASE="${2:?--release needs a value}"; shift 2 ;;
    -n|--namespace)   NAMESPACE="${2:?--namespace needs a value}"; shift 2 ;;
    --drain-timeout)  DRAIN_TIMEOUT="${2:?--drain-timeout needs a value}"; shift 2 ;;
    --no-helm)        USE_HELM=0; shift ;;
    --delete-crds)    DELETE_CRDS=1; shift ;;
    --force)          FORCE=1; shift ;;
    --yes)            ASSUME_YES=1; shift ;;
    --dry-run)        DRY_RUN=1; shift ;;
    -h|--help)        sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)                die "unknown argument '$1' (try --help)" ;;
  esac
done

command -v kubectl >/dev/null 2>&1 || die "kubectl is not on PATH"
[ "$USE_HELM" = 0 ] || [ -n "$RELEASE" ] || die "--release is required (or pass --no-helm)"
[ "$USE_HELM" = 0 ] || command -v helm >/dev/null 2>&1 || die "helm is not on PATH (or pass --no-helm)"

run() {
  if [ "$DRY_RUN" = 1 ]; then
    printf 'would run: %s\n' "$*"
  else
    echo "+ $*"
    "$@"
  fi
}

confirm() {
  [ "$ASSUME_YES" = 1 ] && return 0
  [ "$DRY_RUN" = 1 ] && return 0
  printf '%s [y/N] ' "$1"
  local reply; read -r reply
  case "$reply" in y|Y|yes|YES) return 0 ;; *) die "aborted" ;; esac
}

selector() {
  if [ -n "$RELEASE" ]; then echo "app.kubernetes.io/instance=$RELEASE"
  else echo "app.kubernetes.io/managed-by=Helm,app.kubernetes.io/component=scheduler"; fi
}

crds_present() {
  local crd
  for crd in "${CRDS[@]}"; do
    kubectl get crd "$crd" >/dev/null 2>&1 && return 0
  done
  return 1
}

echo "== 1/5  stop the mandatory-scheduler policy blocking GPU pods mid-teardown"
# Same lever as break-glass. Doing it FIRST means that if anything below stalls, the
# cluster is already usable: GPU pods can fall back to the default scheduler.
for b in $(kubectl get validatingadmissionpolicybinding -l "$(selector)" \
             -o "jsonpath={range .items[*]}{.metadata.name}{'\n'}{end}" 2>/dev/null || true); do
  run kubectl delete validatingadmissionpolicybinding "$b"
done

echo
echo "== 2/5  delete Runs and wait for their finalizers to close the leases"
if crds_present; then
  run kubectl delete runs.rq.davidlangworthy.io --all --all-namespaces --wait=false
  if [ "$DRY_RUN" = 1 ]; then
    echo "would wait up to ${DRAIN_TIMEOUT}s for Run finalizers to drain"
  else
    deadline=$(( $(date +%s) + DRAIN_TIMEOUT ))
    while :; do
      remaining="$(kubectl get runs.rq.davidlangworthy.io --all-namespaces --no-headers 2>/dev/null | wc -l)"
      [ "$remaining" -eq 0 ] && { echo "all Runs drained"; break; }
      if [ "$(date +%s)" -ge "$deadline" ]; then
        echo
        echo "uninstall: $remaining Run(s) still Terminating after ${DRAIN_TIMEOUT}s." >&2
        echo "           They are held by the rq.davidlangworthy.io/funding-closure finalizer," >&2
        echo "           which the CONTROLLER MANAGER runs. Check it is alive:" >&2
        echo "               kubectl get deploy -n $NAMESPACE -l $(selector)" >&2
        echo "               kubectl logs -n $NAMESPACE deploy/<controller> --tail=50" >&2
        echo "           Removing the finalizer by hand leaves OPEN LEASES behind: the ledger" >&2
        echo "           will say that work is still running and still charging." >&2
        [ "$FORCE" = 1 ] || die "refusing to continue (pass --force if you accept an unclosed ledger)"
        break
      fi
      sleep 5
    done
  fi
else
  echo "jobtree CRDs are not installed; nothing to drain"
fi

echo
echo "== 3/5  check the ledger closed cleanly"
if crds_present && [ "$DRY_RUN" = 0 ]; then
  open="$(kubectl get gpuleases.rq.davidlangworthy.io --all-namespaces \
            -o "jsonpath={range .items[?(@.status.closed!=true)]}{.metadata.namespace}/{.metadata.name}{'\n'}{end}" 2>/dev/null || true)"
  if [ -n "$open" ]; then
    echo "uninstall: these leases are still OPEN — each one is charging a budget and holding GPUs:" >&2
    echo "$open" | sed 's/^/             /' >&2
    if [ "$DELETE_CRDS" = 1 ] && [ "$FORCE" != 1 ]; then
      die "refusing to delete the CRDs with open leases (pass --force to accept the loss)"
    fi
  else
    echo "every lease is closed"
  fi
else
  echo "skipped (dry run, or CRDs absent)"
fi

echo
echo "== 4/5  remove the control plane"
if [ "$USE_HELM" = 1 ]; then
  run helm uninstall "$RELEASE" -n "$NAMESPACE"
  # helm uninstall does NOT delete CRDs installed from the chart's crds/ directory.
  # That is Helm's rule, not ours, and it is the right default: it is why the ledger
  # survives an uninstall unless you ask for it below.
else
  for kind in deployment service configmap secret serviceaccount; do
    run kubectl delete "$kind" -l "$(selector)" -n "$NAMESPACE" --ignore-not-found
  done
  for kind in clusterrole clusterrolebinding validatingwebhookconfiguration \
              mutatingwebhookconfiguration validatingadmissionpolicy; do
    run kubectl delete "$kind" -l "$(selector)" --ignore-not-found
  done
fi

echo
echo "== 5/5  CRDs"
if [ "$DELETE_CRDS" = 1 ]; then
  confirm "Delete the four jobtree CRDs? This deletes every Budget, GPULease and Reservation with them — the whole usage history."
  for crd in "${CRDS[@]}"; do
    run kubectl delete crd "$crd" --ignore-not-found
  done
else
  cat <<EOF
Left in place, with every Budget, GPULease and Reservation they hold. This is the
default because the lease set IS the audit trail — "who ran where, when, and who paid"
— and nothing else in the cluster records it. Delete them when you no longer need it:

    hack/uninstall.sh --release ${RELEASE:-<release>} --delete-crds
EOF
fi

echo
echo "uninstall: done"
