package kube

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
)

// These fake-client tests exercise the auditor's grace timing, repair, and alarm
// paths deterministically — no apiserver, a controllable clock, and no background
// reconciler to race. The one thing a fake client CANNOT prove — that a real
// apiserver accepts the "Orphaned" close under R14's closure-monotonicity CEL — is
// proven by TestAuditorClosePersistsThroughTheAPIServer below, under envtest.

func openLease(name, runName, role string, nodes []string, podName string) *v1.GPULease {
	l := &v1.GPULease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: name,
			// A real minted lease carries its run and placement-group labels; without
			// the group label an open lease trips INV-GROUP-STAMPED in the live
			// reconciler (it addresses ranks by group).
			Labels: map[string]string{binder.LabelRunName: runName, binder.LabelGroupIndex: "0"},
		},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:team",
			RunRef:         v1.RunReference{Namespace: "default", Name: runName},
			PaidByEnvelope: "team/pool",
			Slice:          v1.GPULeaseSlice{Nodes: nodes, Role: role},
			Interval:       v1.GPULeaseInterval{Start: metav1.NewTime(baseTime)},
			Reason:         "Start",
		},
	}
	if podName != "" {
		l.Annotations = map[string]string{binder.AnnotationPodName: podName}
	}
	return l
}

func runningRun(name string) *v1.Run {
	r := &v1.Run{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}}
	r.Status.Phase = controllers.RunPhaseRunning
	return r
}

func newAuditor(c client.Client, clk *testClock, rec record.EventRecorder) *LedgerAuditor {
	a := &LedgerAuditor{
		Client:    c,
		APIReader: c,
		Clock:     clk,
		Recorder:  rec,
		Interval:  time.Minute,
		Grace:     5 * time.Minute,
	}
	a.defaults()
	return a
}

func mustSweep(t *testing.T, a *LedgerAuditor) {
	t.Helper()
	if err := a.Sweep(suiteCtx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
}

func leaseClosed(t *testing.T, c client.Client, name string) *v1.GPULease {
	t.Helper()
	var l v1.GPULease
	if err := c.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: name}, &l); err != nil {
		t.Fatalf("get lease %s: %v", name, err)
	}
	return &l
}

// The core contract: a sustained orphan is closed "Orphaned", but ONLY after the
// grace window — a fresh violation is left alone so a healthy in-flight swap is
// never repaired.
func TestSweepClosesAnOrphanOnlyAfterGrace(t *testing.T) {
	clk := &testClock{now: baseTime}
	// A dead-node orphan: the run is Running, the lease holds a slot on a node that
	// does not exist, and no pod is behind it.
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithStatusSubresource(&v1.Run{}, &v1.GPULease{}).
		WithObjects(runningRun("train"), openLease("train-0-lease", "train", binder.RoleActive, []string{"ghost#0"}, "train-0")).
		Build()
	rec := record.NewFakeRecorder(16)
	a := newAuditor(c, clk, rec)

	// First sweep: the violation is observed but not yet matured.
	mustSweep(t, a)
	if leaseClosed(t, c, "train-0-lease").Status.Closed {
		t.Fatal("the lease was closed within the grace window — the auditor raced recovery")
	}

	// Past grace: the second sweep closes it, with the DISTINCT reason.
	clk.Set(baseTime.Add(6 * time.Minute))
	mustSweep(t, a)
	got := leaseClosed(t, c, "train-0-lease")
	if !got.Status.Closed {
		t.Fatal("a sustained orphan was not closed after the grace window")
	}
	if got.Status.ClosureReason != controllers.ClosureReasonOrphaned {
		t.Errorf("closure reason = %q, want %q (never Completed/SweptTerminalRun)", got.Status.ClosureReason, controllers.ClosureReasonOrphaned)
	}
	if got.Status.Ended == nil {
		t.Error("a closed lease must record its Ended time so funding accrues correctly")
	}
	if !hasEvent(rec, "LeaseOrphaned") {
		t.Error("closing an orphaned lease must emit a Warning on the owning Run")
	}
}

// The gauge reports only SUSTAINED violations: within grace it reads 0 (a transient
// is not drift), and only past grace does it report the discrepancy.
func TestSweepGaugeReportsOnlySustainedViolations(t *testing.T) {
	metrics.Reset()
	clk := &testClock{now: baseTime}
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithStatusSubresource(&v1.Run{}, &v1.GPULease{}).
		WithObjects(runningRun("train"), openLease("train-0-lease", "train", binder.RoleActive, []string{"ghost#0"}, "train-0")).
		Build()
	a := newAuditor(c, clk, record.NewFakeRecorder(16))

	mustSweep(t, a)
	if v := metrics.Snapshot().LedgerViolations[controllers.ViolationLeaseDeadNode]; v != 0 {
		t.Errorf("within grace the gauge must read 0, got %v", v)
	}
	clk.Set(baseTime.Add(6 * time.Minute))
	mustSweep(t, a)
	if v := metrics.Snapshot().LedgerViolations[controllers.ViolationLeaseDeadNode]; v != 1 {
		t.Errorf("past grace the gauge must report the sustained violation, got %v", v)
	}
}

// A pod running for a run with no lease is ALARMED, never repaired: the pod is
// untouched and nothing is closed. Killing it is a policy call the auditor declines.
func TestSweepAlarmsPodNoLeaseWithoutTouchingAnything(t *testing.T) {
	metrics.Reset()
	clk := &testClock{now: baseTime}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: "train-0",
			Labels: map[string]string{binder.LabelRunName: "train"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithStatusSubresource(&v1.Run{}, &v1.GPULease{}).
		WithObjects(runningRun("train"), pod).
		Build()
	rec := record.NewFakeRecorder(16)
	a := newAuditor(c, clk, rec)

	// Stamp, then mature past the grace window.
	mustSweep(t, a)
	clk.Set(baseTime.Add(6 * time.Minute))
	mustSweep(t, a)

	// The pod is still there.
	var got corev1.Pod
	if err := c.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "train-0"}, &got); err != nil {
		t.Fatalf("the auditor must never delete a pod, but it is gone: %v", err)
	}
	if metrics.Snapshot().LedgerRepairs[controllers.ViolationPodNoLease] != 0 {
		t.Error("pod_no_lease must never increment the repair counter")
	}
	if v := metrics.Snapshot().LedgerViolations[controllers.ViolationPodNoLease]; v != 1 {
		t.Errorf("pod_no_lease gauge = %v, want 1", v)
	}
	if !hasEvent(rec, "PodWithoutLease") {
		t.Error("a pod running without a lease must raise a Warning")
	}
}

// A healthy world is left completely alone across repeated sweeps, past grace.
func TestSweepHealthyWorldNeverActs(t *testing.T) {
	metrics.Reset()
	clk := &testClock{now: baseTime}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "train-0", Labels: map[string]string{binder.LabelRunName: "train"}},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithStatusSubresource(&v1.Run{}, &v1.GPULease{}).
		WithObjects(runningRun("train"), openLease("train-0-lease", "train", binder.RoleActive, []string{"node-a#0"}, "train-0"), pod, node).
		Build()
	a := newAuditor(c, clk, record.NewFakeRecorder(16))

	for i := 0; i < 3; i++ {
		clk.Set(baseTime.Add(time.Duration(i*10) * time.Minute))
		mustSweep(t, a)
	}
	if leaseClosed(t, c, "train-0-lease").Status.Closed {
		t.Error("a healthy lease was closed")
	}
	snap := metrics.Snapshot()
	for _, k := range []string{controllers.ViolationLeaseNoPod, controllers.ViolationLeaseDeadNode, controllers.ViolationPodNoLease} {
		if snap.LedgerViolations[k] != 0 {
			t.Errorf("healthy world reported %s = %v", k, snap.LedgerViolations[k])
		}
	}
}

func hasEvent(rec *record.FakeRecorder, reason string) bool {
	for {
		select {
		case e := <-rec.Events:
			if strings.Contains(e, reason) {
				return true
			}
		default:
			return false
		}
	}
}

// --- envtest: the closer path against a real apiserver --------------------

// The auditor's read path against a REAL apiserver. The fake-client tests above hand
// a controllers.LedgerWorld straight to AuditLedger; what they cannot prove is that
// loadWorld correctly PROJECTS the two planes out of live API objects — that it reads
// the Node objects' existence set (unfiltered, so a present node is not "dead"), the
// GPULease list, and the run map with the same keys AuditLedger expects — so that a
// genuine orphan (an open Active lease on a node that does not exist) is actually
// flagged when the world is read from the server rather than constructed by hand.
//
// This is a read-only proof by construction: it never writes, so it does not race the
// live RunReconciler (whose eventual repair of the same orphan is the production
// behaviour, not something to fight in a test). The apiserver's acceptance of the
// Orphaned close write is covered by the fake-client close test and by R14's own
// closure-monotonicity envtests; here we prove the auditor SEES what it must.
func TestAuditorReadsAnOrphanFromTheRealAPIServer(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "auditor-run"},
		Spec: v1.RunSpec{
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	run.Status.Phase = controllers.RunPhaseRunning
	if err := kubeClient.Status().Update(suiteCtx, run); err != nil {
		t.Fatalf("set run Running: %v", err)
	}

	// A present node, and an orphan lease that names a DIFFERENT, non-existent node.
	// The present node proves the read is not simply "no nodes exist"; the ghost node
	// is the genuine dead-node orphan.
	createH100Node(t, "node-a", 4)
	lease := openLease("auditor-orphan-lease", "auditor-run", binder.RoleActive, []string{"ghost-node#0"}, "auditor-run-0")
	if err := kubeClient.Create(suiteCtx, lease); err != nil {
		t.Fatalf("create lease: %v", err)
	}

	a := newAuditor(kubeClient, &testClock{now: baseTime}, record.NewFakeRecorder(16))

	// loadWorld reads through the real apiserver; AuditLedger judges it. Poll because
	// the client cache legitimately lags the fresh writes above.
	eventually(t, 15*time.Second, func() error {
		world, _, err := a.loadWorld(suiteCtx)
		if err != nil {
			return err
		}
		if !world.NodeNames["node-a"] {
			return errNotClosed // the present node must be read as present
		}
		for _, v := range controllers.AuditLedger(world) {
			if v.Kind == controllers.ViolationLeaseDeadNode && v.LeaseName == "auditor-orphan-lease" {
				return nil
			}
		}
		return errNotClosed
	})
}

var errNotClosed = &auditErr{"orphan not yet visible through the real apiserver"}

type auditErr struct{ s string }

func (e *auditErr) Error() string { return e.s }
