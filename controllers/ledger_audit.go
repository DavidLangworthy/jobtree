package controllers

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// R26 — the ledger auditor's pure core.
//
// funding.Evaluate is a fuzz-tested pure function, but it replays a ledger nobody
// audits: there is no loop that compares open leases against the pods and nodes
// that are actually there. Every known leak on this board (R8's failed-pod zombie,
// R25's spare-node leak, an evicted rank that never re-emits) is the same shape —
// an open lease charging a budget and holding GPUs with no live work behind it —
// and fixing each one individually leaves the unknown ones. The auditor enforces
// the invariant instead of the causes.
//
// This file is the JUDGEMENT, kept pure so it can be unit-tested against
// hand-built worlds and, unlike the controller around it, reasoned about without a
// cluster. The controller (controllers/kube) does the I/O, the grace timing, and
// the repair. The two invariants it checks:
//
//  1. lease -> reality: an OPEN lease must have a live node for every slot it
//     names, and — if it is an Active lease of a live, non-terminal run — a live
//     pod behind it. A violation is REPAIRABLE: the controller closes the lease
//     "Orphaned" after a grace window (budget-safe: stop the charge).
//  2. reality -> lease: a live, non-terminal run holding jobtree pods must hold at
//     least one open lease. A violation is REPORT-ONLY: a pod running for nobody is
//     either a mint-path bug or a forged pod (R5/R6), and killing it is a policy
//     call the auditor does not make — it alarms.
//
// Every rule reads only the PRESENT state and acts only on POSITIVE evidence, the
// same discipline as SettleLeases: it never infers a deletion from an absence it
// cannot explain, and it defers to the paths that own a state rather than racing
// them.
const (
	// ViolationLeaseDeadNode: an open lease names a slot on a Node object that no
	// longer exists. Absence of the Node — not NotReady, not cordoned — is the
	// signal, because only a deleted Node (or the out-of-service taint the node
	// reconciler acts on) is a real loss of the hardware the lease is holding.
	// A NotReady node still exists and its lease is not orphaned (fencing).
	ViolationLeaseDeadNode = "lease_dead_node"
	// ViolationLeaseNoPod: an open Active lease of a live, non-terminal run has no
	// live pod behind it — the run is charging for a rank that is not running.
	ViolationLeaseNoPod = "lease_no_pod"
	// ViolationPodNoLease: a live, non-terminal run holds jobtree pods but not one
	// open lease. Report-only.
	ViolationPodNoLease = "pod_no_lease"

	// ClosureReasonOrphaned is the DISTINCT closure reason the auditor uses. Never
	// "Completed": a run whose lease the auditor had to close did not complete, and
	// laundering a leak as a completion is how the leak stays invisible. Never
	// "SweptTerminalRun" either — that reason means the run was terminal; the
	// auditor closes leases of runs that are still, wrongly, alive.
	ClosureReasonOrphaned = "Orphaned"
)

// LedgerWorld is the auditor's view of the two planes. It is deliberately NOT a
// ClusterState: ClusterState.Nodes is, by the bridge's convention, only the
// USABLE nodes, and the dead-node rule must not fire on a merely-NotReady node.
// NodeNames here is every Node object that EXISTS, whatever its readiness.
type LedgerWorld struct {
	Runs      map[string]*v1.Run // by namespaced key
	Leases    []v1.GPULease
	Pods      []LedgerPod
	NodeNames map[string]bool // every existing Node object's name (not usability-filtered)
}

// LedgerPod is the slice of a jobtree pod the auditor reasons about.
type LedgerPod struct {
	Namespace   string
	Name        string
	RunName     string // from binder.LabelRunName
	Phase       string // corev1.PodPhase
	Terminating bool   // has a DeletionTimestamp
}

// live reports whether a pod is a running container the ledger should have work
// behind: not terminated, and not draining.
func (p LedgerPod) live() bool {
	if p.Terminating {
		return false
	}
	return p.Phase != string(corev1.PodFailed) && p.Phase != string(corev1.PodSucceeded)
}

// LedgerViolation is one discrepancy between the ledger and reality.
type LedgerViolation struct {
	Kind string
	// Lease identifies the offending lease for the lease_* kinds.
	LeaseNamespace string
	LeaseName      string
	// PodName names one example pod for pod_no_lease (there may be more).
	PodName string
	RunKey  string
	Detail  string
	// Repairable is true for the lease_* kinds (the controller may close the lease
	// after grace) and false for pod_no_lease (alarm only).
	Repairable bool
}

// AuditLedger evaluates both invariants against a world and returns every current
// violation. It is a snapshot judgement: the caller (the controller) owns the
// grace timing that decides which violations have PERSISTED long enough to act on,
// so a transient mint/swap window never triggers a repair here.
func AuditLedger(w LedgerWorld) []LedgerViolation {
	var out []LedgerViolation

	// Pods that are live right now, indexed by (namespace, name), so a lease can ask
	// "is my annotated pod actually running?" in O(1).
	livePods := map[string]bool{}
	for _, p := range w.Pods {
		if p.live() {
			livePods[keys.NamespacedKey(p.Namespace, p.Name)] = true
		}
	}

	// Invariant 1: every open lease against reality.
	for i := range w.Leases {
		lease := &w.Leases[i]
		if lease.Status.Closed {
			continue
		}
		runKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)

		// (a) dead node — the more specific, root-cause violation. A lease whose
		// hardware is gone is reported as dead-node and not also as no-pod, because
		// the missing pod is a CONSEQUENCE of the missing node, and the operator
		// wants the cause.
		if node := firstDeadNode(lease, w.NodeNames); node != "" {
			out = append(out, LedgerViolation{
				Kind:           ViolationLeaseDeadNode,
				LeaseNamespace: lease.Namespace,
				LeaseName:      lease.Name,
				RunKey:         runKey,
				Repairable:     true,
				Detail: fmt.Sprintf("open lease holds a slot on node %q, which no longer exists; the hardware it charges for is gone",
					node),
			})
			continue
		}

		// (b) no pod behind an Active lease.
		if lease.Spec.Slice.Role != binder.RoleActive {
			continue // a Spare's worklessness is closeWorklessSpareLeases' job, not this.
		}
		run := w.Runs[runKey]
		if run == nil {
			// An absent Run is not the auditor's business: the FundingClosureFinalizer
			// holds a deleting Run in the API until its leases close, and a genuine
			// orphan is closed by cleanupDeletedRun on positive evidence. Guessing here
			// would re-introduce the R27c orphan-run reaper R12 deleted.
			continue
		}
		if isTerminalRun(run) {
			continue // SettleLeases' terminal-run rule owns a corpse's open leases.
		}
		podName := lease.Annotations[binder.AnnotationPodName]
		if podName == "" {
			// A lease minted before durable pod identity (R2 pt3). Without the pod
			// name there is no sound pod match, and matching by node would misfire on
			// swaps — so decline the no-pod verdict rather than guess. The dead-node
			// rule above still protects these.
			continue
		}
		if !livePods[keys.NamespacedKey(lease.Spec.RunRef.Namespace, podName)] {
			out = append(out, LedgerViolation{
				Kind:           ViolationLeaseNoPod,
				LeaseNamespace: lease.Namespace,
				LeaseName:      lease.Name,
				RunKey:         runKey,
				Repairable:     true,
				Detail: fmt.Sprintf("open Active lease for pod %q of a %s run, but no live pod of that name exists; the run is charging for a rank that is not running",
					podName, run.Status.Phase),
			})
		}
	}

	// Invariant 2: every live, non-terminal run that holds jobtree pods must hold an
	// open lease. Checked at RUN granularity, not pod granularity: a run with SOME
	// open leases but a not-yet-minted pod is the healthy assembly/swap window, and
	// only a run with live pods and ZERO open leases is charging nobody. That makes
	// the rule immune to the AwaitingMint transient a pod-level check would trip on.
	livePodsByRun := map[string]string{} // runKey -> one example live pod name
	for _, p := range w.Pods {
		if !p.live() || p.RunName == "" {
			continue
		}
		rk := keys.NamespacedKey(p.Namespace, p.RunName)
		if _, ok := livePodsByRun[rk]; !ok {
			livePodsByRun[rk] = p.Name
		}
	}
	openLeaseRuns := map[string]bool{}
	for i := range w.Leases {
		lease := &w.Leases[i]
		if lease.Status.Closed {
			continue
		}
		openLeaseRuns[keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)] = true
	}
	for rk, example := range livePodsByRun {
		if openLeaseRuns[rk] {
			continue
		}
		run := w.Runs[rk]
		if run == nil || isTerminalRun(run) {
			// A terminal or absent run's leftover pods are SettleLeases' job (it drops
			// them and closes their leases on positive evidence). Alarming here would
			// duplicate that and race it.
			continue
		}
		out = append(out, LedgerViolation{
			Kind:       ViolationPodNoLease,
			PodName:    example,
			RunKey:     rk,
			Repairable: false,
			Detail: fmt.Sprintf("run holds live jobtree pod %q (and possibly more) but no open lease; it is running for nobody — a mint-path bug or a forged pod",
				example),
		})
	}

	return out
}

// firstDeadNode returns the first slot's node that no longer exists, or "".
func firstDeadNode(lease *v1.GPULease, nodeNames map[string]bool) string {
	for _, slot := range lease.Spec.Slice.Nodes {
		node := nodeFromSlot(slot)
		if node == "" {
			continue
		}
		if !nodeNames[node] {
			return node
		}
	}
	return ""
}

// isTerminalRun reports whether a run's own phase says it is done. The auditor
// defers a terminal run's open leases to SettleLeases, which owns that rule.
func isTerminalRun(run *v1.Run) bool {
	return run.Status.Phase == RunPhaseFailed || run.Status.Phase == RunPhaseComplete
}
