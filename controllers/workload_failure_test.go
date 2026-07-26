package controllers

import (
	"fmt"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// roledFailureWorld builds a Running roled run of width=len(phases) with the given
// FailurePolicy, one Active lease + pod per member (named as its ordinal so the
// top-up re-emit recognises the survivors), and phase[i] on member i.
func roledFailureWorld(policy string, retries int32, phases []string) (*ClusterState, *v1.Run, string) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "job", Namespace: keys.DefaultNamespace},
		Spec: v1.RunSpec{
			Resources: v1.RunResources{GPUType: "H100", TotalGPUs: int32(len(phases))},
			Roles:     []v1.RunRole{{Name: "w", Width: int32(len(phases)), GPUsPerPod: 1, FailurePolicy: policy}},
		},
		Status: v1.RunStatus{Phase: RunPhaseRunning},
	}
	if policy == v1.FailurePolicyRetry {
		run.Spec.Roles[0].Retries = &retries
	}
	key := keys.NamespacedKey(run.Namespace, run.Name)
	state := &ClusterState{Runs: map[string]*v1.Run{key: run}}
	for i, phase := range phases {
		name := cohortPodName(run, "0", i)
		node := fmt.Sprintf("n%d", i)
		state.Leases = append(state.Leases, v1.GPULease{
			ObjectMeta: v1.ObjectMeta{Namespace: keys.DefaultNamespace, Name: name,
				Labels: map[string]string{binder.LabelRunName: "job", binder.LabelGroupIndex: "0", binder.LabelRunRole: binder.RoleActive}},
			Spec: v1.GPULeaseSpec{Owner: "team", RunRef: v1.RunReference{Name: "job", Namespace: keys.DefaultNamespace},
				Slice: v1.GPULeaseSlice{Nodes: []string{node + "#0"}, Role: binder.RoleActive}},
		})
		state.Pods = append(state.Pods, binder.PodManifest{
			Namespace: keys.DefaultNamespace, Name: name, NodeName: node, Phase: phase, GPUs: 1,
			Labels: map[string]string{binder.LabelRunName: "job", binder.LabelGroupIndex: "0", binder.LabelRunRole: binder.RoleActive},
		})
	}
	return state, run, key
}

// Ignore: a Failed active member is terminal, not a blocker — the run completes.
func TestIgnorePolicyCompletesDespiteAFailedPod(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	state, _, key := roledFailureWorld(v1.FailurePolicyIgnore, 0, []string{"Succeeded", "Failed"})
	c := NewRunController(state, runClock{now: now})
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := state.Runs[key].Status.Phase; got != RunPhaseComplete {
		t.Fatalf("Ignore: a run whose members have all finished (succeeded or failed) must complete, got %s", got)
	}
}

// Retry: a failed member is re-emitted (fresh pod), its stale lease closed, attempts
// counted, and the run stays Running until retries exhaust.
func TestRetryPolicyReEmitsTheFailedMember(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	state, _, key := roledFailureWorld(v1.FailurePolicyRetry, 2, []string{"Running", "Failed"})
	c := NewRunController(state, runClock{now: now})
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	run := state.Runs[key]
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("Retry with attempts remaining must keep the run Running, got %s", run.Status.Phase)
	}
	if run.Status.FailedAttempts != 1 {
		t.Errorf("FailedAttempts = %d, want 1", run.Status.FailedAttempts)
	}
	// The failed member's stale lease is closed (so its rank is not double-charged).
	if openLeaseCount(state) != 1 {
		t.Errorf("the failed member's lease must be closed on retry, open leases = %d, want 1", openLeaseCount(state))
	}
	// A fresh (non-Failed) pod for the member was re-emitted.
	failedStill := false
	for i := range state.Pods {
		if state.Pods[i].Phase == binder.PodPhaseFailed {
			failedStill = true
		}
	}
	if failedStill {
		t.Errorf("the failed pod must be dropped and re-emitted, but a Failed pod remains")
	}
	if len(state.Pods) != 2 {
		t.Errorf("the gang must be back to width 2 after re-emit, got %d pods", len(state.Pods))
	}
}

func TestRetryPolicyFailsWhenExhausted(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	state, run, key := roledFailureWorld(v1.FailurePolicyRetry, 2, []string{"Running", "Failed"})
	run.Status.FailedAttempts = 2 // already used both retries
	c := NewRunController(state, runClock{now: now})
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := state.Runs[key].Status.Phase; got != RunPhaseFailed {
		t.Fatalf("Retry exhausted must fail the run, got %s", got)
	}
	if openLeaseCount(state) != 0 {
		t.Errorf("a failed run's leases must all close, open = %d", openLeaseCount(state))
	}
}
