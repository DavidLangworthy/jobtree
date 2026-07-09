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
	if failed, _ := nodeFailed(node, now); failed {
		t.Errorf("a cordoned but Ready node must never be treated as failed — that is the two-live-ranks bug")
	}
}

// The stale-event flake (task #36): a node recreated with no status yet has no
// Ready condition. It has not reported; it has not failed. Treating "not usable"
// as "failed" let a replayed event close a healthy node's leases.
func TestNodeThatHasNotReportedYetIsNotFailed(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	fresh := &corev1.Node{} // created, kubelet has not posted a Ready condition

	if nodeUsable(fresh) {
		t.Fatalf("setup: a node with no Ready condition is not usable")
	}
	failed, retry := nodeFailed(fresh, now)
	if failed {
		t.Errorf("a node that has not reported yet must not be treated as failed")
	}
	if retry <= 0 {
		t.Errorf("expected a re-check to be scheduled, got retryAfter=%v", retry)
	}
}

// A kubelet blip is not a node failure. NotReady must persist past the grace
// window before jobtree closes leases and re-places a rank.
func TestNotReadyWithinGraceIsNotYetFailed(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	node := nodeWithReady(corev1.ConditionFalse, now.Add(-10*time.Second))

	failed, retry := nodeFailed(node, now)
	if failed {
		t.Errorf("NotReady for 10s is a blip, not a failure (grace is %v)", nodeNotReadyGrace)
	}
	if want := nodeNotReadyGrace - 10*time.Second; retry != want {
		t.Errorf("retryAfter = %v, want the remaining grace %v", retry, want)
	}
}

// ...and past the grace, it really is failed. The swap must still work.
func TestNotReadyPastGraceIsFailed(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	node := nodeWithReady(corev1.ConditionFalse, now.Add(-nodeNotReadyGrace-time.Second))

	if failed, _ := nodeFailed(node, now); !failed {
		t.Errorf("a node NotReady past the grace window is failed")
	}
	// Unknown (kubelet unreachable) is the other real-failure status.
	unknown := nodeWithReady(corev1.ConditionUnknown, now.Add(-nodeNotReadyGrace-time.Second))
	if failed, _ := nodeFailed(unknown, now); !failed {
		t.Errorf("Ready=Unknown past the grace window is failed")
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
