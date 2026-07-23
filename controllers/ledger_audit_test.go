package controllers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// --- fixtures -------------------------------------------------------------

func auditRun(name, phase string) *v1.Run {
	r := &v1.Run{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}}
	r.Status.Phase = phase
	return r
}

func auditLease(name, runName, role string, nodes []string, podName string) v1.GPULease {
	l := v1.GPULease{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name},
		Spec: v1.GPULeaseSpec{
			RunRef:         v1.RunReference{Namespace: "default", Name: runName},
			PaidByEnvelope: "team/pool",
			Slice:          v1.GPULeaseSlice{Nodes: nodes, Role: role},
		},
	}
	if podName != "" {
		l.Annotations = map[string]string{binder.AnnotationPodName: podName}
	}
	return l
}

func auditPod(name, runName, phase string) LedgerPod {
	return LedgerPod{Namespace: "default", Name: name, RunName: runName, Phase: phase}
}

func worldWith(runs []*v1.Run, leases []v1.GPULease, pods []LedgerPod, nodes ...string) LedgerWorld {
	rm := map[string]*v1.Run{}
	for _, r := range runs {
		rm[keys.NamespacedKey(r.Namespace, r.Name)] = r
	}
	nn := map[string]bool{}
	for _, n := range nodes {
		nn[n] = true
	}
	return LedgerWorld{Runs: rm, Leases: leases, Pods: pods, NodeNames: nn}
}

func kindsOf(vs []LedgerViolation) map[string]int {
	m := map[string]int{}
	for _, v := range vs {
		m[v.Kind]++
	}
	return m
}

// --- invariant 1: lease -> reality ---------------------------------------

// The canonical leak: a Running run holds an open Active lease whose pod is gone.
// This is R8's failed-pod zombie / the evicted-rank-never-reemitted case, and the
// auditor must see it as a real, repairable orphan.
func TestAuditFlagsAnActiveLeaseWhosePodIsGone(t *testing.T) {
	w := worldWith(
		[]*v1.Run{auditRun("train", RunPhaseRunning)},
		[]v1.GPULease{auditLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "train-0")},
		nil, // no pods
		"node-a",
	)
	vs := AuditLedger(w)
	if kindsOf(vs)[ViolationLeaseNoPod] != 1 {
		t.Fatalf("expected one lease_no_pod, got %+v", vs)
	}
	if !vs[0].Repairable {
		t.Error("a lease_no_pod must be repairable (the auditor may close it)")
	}
}

// A live pod behind the lease is the healthy state — no violation. A Pending pod
// counts as live: it is the rank being (re)provisioned, exactly what
// recoverEvictedRanks produces, and closing its lease would race the recovery.
func TestAuditAcceptsALeaseWithAPendingPod(t *testing.T) {
	for _, phase := range []string{string(corev1.PodRunning), string(corev1.PodPending)} {
		w := worldWith(
			[]*v1.Run{auditRun("train", RunPhaseRunning)},
			[]v1.GPULease{auditLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "train-0")},
			[]LedgerPod{auditPod("train-0", "train", phase)},
			"node-a",
		)
		if vs := AuditLedger(w); len(vs) != 0 {
			t.Errorf("phase %s: expected no violation with a live pod, got %+v", phase, vs)
		}
	}
}

// A dead node is the more specific, root-cause verdict: the lease is reported
// lease_dead_node, not lease_no_pod, because the absent pod is a consequence of
// the absent node — and the operator wants the cause.
func TestAuditFlagsALeaseOnADeletedNodeAsDeadNode(t *testing.T) {
	w := worldWith(
		[]*v1.Run{auditRun("train", RunPhaseRunning)},
		[]v1.GPULease{auditLease("train-0-lease", "train", binder.RoleActive, []string{"ghost#0"}, "train-0")},
		nil,
		"node-a", // ghost is absent
	)
	vs := AuditLedger(w)
	if kindsOf(vs)[ViolationLeaseDeadNode] != 1 || len(vs) != 1 {
		t.Fatalf("expected exactly one lease_dead_node, got %+v", vs)
	}
}

// Fencing: a node that still EXISTS but is NotReady is not dead. The auditor keys
// off the Node object's existence, never its readiness — the same rule as the swap
// path (only a deleted node or the out-of-service taint is a real loss). The node
// being present in NodeNames is what a NotReady-but-present node looks like here.
func TestAuditDoesNotTreatAPresentNodeAsDead(t *testing.T) {
	w := worldWith(
		[]*v1.Run{auditRun("train", RunPhaseRunning)},
		[]v1.GPULease{auditLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "train-0")},
		[]LedgerPod{auditPod("train-0", "train", string(corev1.PodRunning))},
		"node-a", // present, whatever its readiness
	)
	if vs := AuditLedger(w); len(vs) != 0 {
		t.Errorf("a present (even NotReady) node must not be treated as dead: %+v", vs)
	}
}

// A terminal run's open lease is SettleLeases' job, on positive evidence (the run's
// own phase). The auditor must defer, or the two race to close the same lease with
// two different reasons.
func TestAuditDefersTerminalRunLeasesToSettle(t *testing.T) {
	for _, phase := range []string{RunPhaseFailed, RunPhaseComplete} {
		w := worldWith(
			[]*v1.Run{auditRun("train", phase)},
			[]v1.GPULease{auditLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "train-0")},
			nil,
			"node-a",
		)
		if vs := AuditLedger(w); len(vs) != 0 {
			t.Errorf("phase %s: terminal-run leases belong to SettleLeases, got %+v", phase, vs)
		}
	}
}

// An absent Run is not the auditor's business — the finalizer holds a deleting Run
// until its leases close, and cleanupDeletedRun closes a genuine orphan on positive
// evidence. Guessing here would rebuild the R27c orphan-run reaper R12 deleted.
func TestAuditIgnoresLeasesOfAnAbsentRun(t *testing.T) {
	w := worldWith(
		nil, // run absent
		[]v1.GPULease{auditLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "train-0")},
		nil,
		"node-a",
	)
	if vs := AuditLedger(w); len(vs) != 0 {
		t.Errorf("an absent run's leases must be left to the finalizer/cleanup path, got %+v", vs)
	}
}

// A closed lease is a settled fact and must never be re-flagged.
func TestAuditIgnoresClosedLeases(t *testing.T) {
	lease := auditLease("train-0-lease", "train", binder.RoleActive, []string{"ghost#0"}, "train-0")
	lease.Status.Closed = true
	w := worldWith([]*v1.Run{auditRun("train", RunPhaseRunning)}, []v1.GPULease{lease}, nil, "node-a")
	if vs := AuditLedger(w); len(vs) != 0 {
		t.Errorf("a closed lease must not be flagged, got %+v", vs)
	}
}

// A Spare lease's worklessness is closeWorklessSpareLeases' job; the no-pod rule is
// Active-only. (Dead-node still applies to a spare, tested implicitly above.)
func TestAuditNoPodRuleIsActiveOnly(t *testing.T) {
	w := worldWith(
		[]*v1.Run{auditRun("train", RunPhaseRunning)},
		[]v1.GPULease{auditLease("train-spare-lease", "train", binder.RoleSpare, []string{"node-a#0"}, "train-spare")},
		nil,
		"node-a",
	)
	if vs := AuditLedger(w); len(vs) != 0 {
		t.Errorf("a spare's missing pod is not a lease_no_pod, got %+v", vs)
	}
}

// A legacy lease with no durable pod-name annotation cannot be soundly matched to a
// pod, so the no-pod rule declines rather than guess — the dead-node rule still
// covers it.
func TestAuditDeclinesNoPodVerdictWithoutDurableIdentity(t *testing.T) {
	w := worldWith(
		[]*v1.Run{auditRun("train", RunPhaseRunning)},
		[]v1.GPULease{auditLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "")}, // no annotation
		nil,
		"node-a",
	)
	if vs := AuditLedger(w); len(vs) != 0 {
		t.Errorf("without a pod-name annotation the no-pod verdict must be declined, got %+v", vs)
	}
}

// --- invariant 2: reality -> lease (report-only) --------------------------

// A live, non-terminal run with pods but ZERO open leases is charging nobody. It is
// reported, never repaired (killing a pod is a policy call the auditor does not make).
func TestAuditFlagsAPodRunningWithoutAnyLease(t *testing.T) {
	w := worldWith(
		[]*v1.Run{auditRun("train", RunPhaseRunning)},
		nil, // no leases at all
		[]LedgerPod{auditPod("train-0", "train", string(corev1.PodRunning))},
	)
	vs := AuditLedger(w)
	if kindsOf(vs)[ViolationPodNoLease] != 1 || len(vs) != 1 {
		t.Fatalf("expected one pod_no_lease, got %+v", vs)
	}
	if vs[0].Repairable {
		t.Error("pod_no_lease must be report-only (never repairable)")
	}
}

// The AwaitingMint transient must NOT trip invariant 2: a run mid-assembly has some
// open leases and a not-yet-minted pod. Checking at run granularity — "the run has
// at least one open lease" — makes that healthy window invisible, where a
// pod-granular check would cry wolf on every gang that is still forming.
func TestAuditDoesNotFlagAwaitingMint(t *testing.T) {
	w := worldWith(
		[]*v1.Run{auditRun("train", RunPhaseRunning)},
		[]v1.GPULease{auditLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "train-0")},
		[]LedgerPod{
			auditPod("train-0", "train", string(corev1.PodRunning)), // minted
			auditPod("train-1", "train", string(corev1.PodPending)), // awaiting its mint
		},
		"node-a",
	)
	if k := kindsOf(AuditLedger(w)); k[ViolationPodNoLease] != 0 {
		t.Errorf("a forming gang with one open lease must not be pod_no_lease, got %+v", k)
	}
}

// A healthy, fully-assembled world produces zero violations across the board.
func TestAuditHealthyWorldIsClean(t *testing.T) {
	w := worldWith(
		[]*v1.Run{auditRun("train", RunPhaseRunning)},
		[]v1.GPULease{
			auditLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "train-0"),
			auditLease("train-1-lease", "train", binder.RoleActive, []string{"node-a#1"}, "train-1"),
		},
		[]LedgerPod{
			auditPod("train-0", "train", string(corev1.PodRunning)),
			auditPod("train-1", "train", string(corev1.PodRunning)),
		},
		"node-a",
	)
	if vs := AuditLedger(w); len(vs) != 0 {
		t.Fatalf("a healthy world must be clean, got %+v", vs)
	}
}
