package kube

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func nodeWithReady(status corev1.ConditionStatus, transitioned time.Time) *corev1.Node {
	n := &corev1.Node{}
	cond := corev1.NodeCondition{Type: corev1.NodeReady, Status: status}
	if !transitioned.IsZero() {
		cond.LastTransitionTime = metav1.NewTime(transitioned)
	}
	n.Status.Conditions = []corev1.NodeCondition{cond}
	return n
}

func fence(node *corev1.Node) *corev1.Node {
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    taintOutOfService,
		Effect: corev1.TaintEffectNoExecute,
	})
	return node
}

// R21 — a `kubectl cordon` is not a node failure. Driving HandleNodeFailure off
// `!nodeUsable` swapped a healthy rank onto a spare while the original pod kept
// running: two live copies of the same distributed-training rank, which is silent
// data corruption rather than a crash.
func TestCordonIsNotANodeFailure(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	node := nodeWithReady(corev1.ConditionTrue, now.Add(-time.Hour))
	node.Spec.Unschedulable = true // kubectl cordon

	if nodeUsable(node) {
		t.Fatalf("setup: a cordoned node is not usable for NEW placement")
	}
	if nodeFailed(node) {
		t.Errorf("a cordoned but Ready node must never be treated as failed — that is the two-live-ranks bug")
	}
}

// The heart of R21, and the reason the grace window was removed.
//
// NotReady means the control plane cannot HEAR the kubelet. It does not mean the
// containers stopped — a partitioned kubelet keeps running them. Kubernetes itself
// waits ~50s to mark the node NotReady, then issues an ordinary GRACEFUL pod delete
// at tolerationSeconds (300s) that an unreachable kubelet never acts on. Swapping on
// a NotReady timer of any length starts a second copy of a live rank.
func TestNotReadyIsNeverAFailure(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name string
		node *corev1.Node
	}{
		{"just now", nodeWithReady(corev1.ConditionFalse, now)},
		{"ten seconds", nodeWithReady(corev1.ConditionFalse, now.Add(-10*time.Second))},
		{"two minutes", nodeWithReady(corev1.ConditionFalse, now.Add(-2*time.Minute))},
		{"an hour", nodeWithReady(corev1.ConditionFalse, now.Add(-time.Hour))},
		{"a week", nodeWithReady(corev1.ConditionFalse, now.Add(-7*24*time.Hour))},
		{"unreachable an hour", nodeWithReady(corev1.ConditionUnknown, now.Add(-time.Hour))},
		{"no transition time", nodeWithReady(corev1.ConditionFalse, time.Time{})},
	} {
		if nodeFailed(tc.node) {
			t.Errorf("%s: NotReady is not a fencing assertion; a swap here can duplicate a live rank", tc.name)
		}
	}
}

// The stale-event flake (task #36): a node recreated with no status yet has no
// Ready condition. It has not reported; it has not failed.
func TestNodeThatHasNotReportedYetIsNotFailed(t *testing.T) {
	fresh := &corev1.Node{} // created, kubelet has not posted a Ready condition

	if nodeUsable(fresh) {
		t.Fatalf("setup: a node with no Ready condition is not usable")
	}
	if nodeFailed(fresh) {
		t.Errorf("a node that has not reported yet must not be treated as failed")
	}
}

// `node.kubernetes.io/out-of-service` is Kubernetes' sanctioned assertion that the
// machine is really dead: Pod GC force-deletes its pods (grace 0). That, and node
// deletion, are the only signals that license moving a rank.
func TestOutOfServiceTaintIsAFailure(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	// The realistic shape: unreachable kubelet, then an operator fences it.
	fenced := fence(nodeWithReady(corev1.ConditionUnknown, now.Add(-10*time.Minute)))
	if !nodeFailed(fenced) {
		t.Errorf("an out-of-service node is fenced and must be treated as failed")
	}

	// Fencing is an assertion about the machine, so it wins even over a stale
	// Ready=True status — a node nobody can reach may still be reporting Ready
	// from the last heartbeat the API server saw.
	stillReady := fence(nodeWithReady(corev1.ConditionTrue, now.Add(-time.Hour)))
	if !nodeFailed(stillReady) {
		t.Errorf("the fencing taint is the assertion; a stale Ready condition must not veto it")
	}
}

// An unrelated taint is not a fence. `kubectl taint` is a routine operation.
func TestUnrelatedTaintIsNotAFailure(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	node := nodeWithReady(corev1.ConditionTrue, now.Add(-time.Hour))
	node.Spec.Taints = []corev1.Taint{
		{Key: "nvidia.com/gpu", Effect: corev1.TaintEffectNoSchedule},
		{Key: "node.kubernetes.io/unreachable", Effect: corev1.TaintEffectNoExecute},
	}

	if nodeFailed(node) {
		t.Errorf("only the out-of-service taint fences; `unreachable` is applied automatically and proves nothing")
	}
}

// A cordoned node stays out of the capacity pool. R21 decouples "failed" from
// "usable" — it must not accidentally make a cordoned node schedulable again.
func TestCordonStillRemovesTheNodeFromCapacity(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	node := nodeWithReady(corev1.ConditionTrue, now.Add(-time.Hour))
	node.Spec.Unschedulable = true

	if nodeUsable(node) {
		t.Errorf("bridge.load must keep excluding a cordoned node's GPUs from capacity")
	}
}

// A fenced node's GPUs do not exist, whatever its Ready condition still says.
//
// `nodeFailed` and `nodeUsable` answer different questions, but a fence answers
// both. Counting a fenced node's GPUs let the engine admit and CHARGE a run for
// capacity on a machine jobtree had just declared dead: the ledger says the GPUs
// are there, the NoExecute taint says nothing may run on them, and the next node
// event closes whatever was minted. A fencing taint is not transient — it outlives
// the failure it reports — so nothing corrects this on its own.
func TestFencedNodeIsNotCapacity(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	// The dangerous shape: the kubelet's last heartbeat still says Ready, and an
	// operator has fenced the machine.
	fenced := fence(nodeWithReady(corev1.ConditionTrue, now.Add(-time.Hour)))
	if !nodeFailed(fenced) {
		t.Fatalf("setup: an out-of-service node is fenced")
	}
	if nodeUsable(fenced) {
		t.Errorf("a fenced node's GPUs must leave the capacity pool; otherwise a run is charged for capacity that cannot run it")
	}

	// A NotReady node is NOT usable either — but for the ordinary reason, and
	// without being treated as failed.
	notReady := nodeWithReady(corev1.ConditionUnknown, now.Add(-time.Hour))
	if nodeUsable(notReady) {
		t.Errorf("a NotReady node is not schedulable")
	}
	if nodeFailed(notReady) {
		t.Errorf("...but it is still not failed: only a fencing assertion is")
	}
}
