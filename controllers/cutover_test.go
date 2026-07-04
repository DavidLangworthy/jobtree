package controllers

import (
	"strconv"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// seedRunning simulates the scheduler plugin having already scheduled and
// funded a run: it mints the exact leases admission.Plan produces (the old
// in-controller commit) plus their pods and marks the run Running. Lifecycle
// tests (completion, elasticity, node-failure, reservation activation) start
// from a bound run this way, now that Reconcile itself emits unscheduled intent
// pods instead of minting (borrow-vs-build.md §9). It is the test-side stand-in
// for the plugin's PreBind mint + the controller's adoption flip.
func seedRunning(t *testing.T, state *ClusterState, runKey string, now time.Time) {
	t.Helper()
	run := state.Runs[runKey]
	if run == nil {
		t.Fatalf("seedRunning: run %s not found", runKey)
	}
	res, err := admission.Plan(admission.Input{
		Run:     run,
		Budgets: state.Budgets,
		Runs:    state.Runs,
		Leases:  state.Leases,
		Nodes:   state.Nodes,
		Now:     now,
	})
	if err != nil {
		t.Fatalf("seedRunning %s: %v", runKey, err)
	}
	state.Leases = append(state.Leases, res.Leases...)
	state.Pods = append(state.Pods, res.Pods...)
	run.Status.Phase = RunPhaseRunning
}

// seedGrowLeases mints the leases the scheduler plugin would create for an
// elastic-grow cohort of deltaGPUs more GPUs (reason Grow), on top of the run's
// existing leases — the test stand-in for the plugin funding + binding a grow
// cohort now that growRun emits intent pods instead of minting.
func seedGrowLeases(t *testing.T, state *ClusterState, runKey string, deltaGPUs int32, now time.Time) {
	t.Helper()
	run := state.Runs[runKey]
	if run == nil {
		t.Fatalf("seedGrowLeases: run %s not found", runKey)
	}
	res, err := admission.Plan(admission.Input{
		Run:      run,
		Budgets:  state.Budgets,
		Runs:     state.Runs,
		Leases:   state.Leases,
		Nodes:    state.Nodes,
		Now:      now,
		Quantity: deltaGPUs,
		Reason:   "Grow",
	})
	if err != nil {
		t.Fatalf("seedGrowLeases %s +%d: %v", runKey, deltaGPUs, err)
	}
	state.Leases = append(state.Leases, res.Leases...)
	state.Pods = append(state.Pods, res.Pods...)
}

// seedSwapLease mints the Swap lease the scheduler plugin would create for a
// node-failure swap pod — from the provenance the controller stamped on that pod
// (the spare's payer), on the swap node — the test stand-in for the plugin's
// provenance-preserving PreBind now that HandleNodeFailure emits a swap pod
// instead of minting. Returns the minted lease.
func seedSwapLease(t *testing.T, state *ClusterState, runName string, now time.Time) v1.Lease {
	t.Helper()
	var pod *binder.PodManifest
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Labels[binder.LabelRunName] == runName && p.Annotations[binder.AnnotationLeaseReason] == "Swap" {
			pod = p // last swap pod wins
		}
	}
	if pod == nil {
		t.Fatalf("seedSwapLease: no swap pod found for run %s", runName)
	}
	node := pod.Annotations[binder.AnnotationSwapNode]
	slots := make([]string, pod.GPUs)
	for i := range slots {
		slots[i] = node + "#" + strconv.Itoa(i)
	}
	lease := v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: pod.Namespace,
			Name:      pod.Name + "-lease",
			Labels:    map[string]string{binder.LabelRunName: pod.Labels[binder.LabelRunName], binder.LabelRunRole: binder.RoleActive},
		},
		Spec: v1.LeaseSpec{
			Owner:          pod.Annotations[binder.AnnotationPayerOwner],
			RunRef:         v1.RunReference{Name: pod.Labels[binder.LabelRunName], Namespace: pod.Namespace},
			Slice:          v1.LeaseSlice{Nodes: slots, Role: binder.RoleActive},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(now)},
			PaidByBudget:   pod.Annotations[binder.AnnotationPayerBudget],
			PaidByEnvelope: pod.Annotations[binder.AnnotationPayerEnvelope],
			Reason:         "Swap",
		},
	}
	state.Leases = append(state.Leases, lease)
	return lease
}

// activeIntentPods counts the unscheduled Active workload pods Reconcile emitted
// for a run (the width it is requesting from the scheduler).
func activeIntentPods(state *ClusterState, namespace, runName string) int {
	return intentPodsByRole(state, namespace, runName, binder.RoleActive)
}

func spareIntentPods(state *ClusterState, namespace, runName string) int {
	return intentPodsByRole(state, namespace, runName, binder.RoleSpare)
}

func intentPodsByRole(state *ClusterState, namespace, runName, role string) int {
	n := 0
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Namespace == namespace && p.Labels[binder.LabelRunName] == runName && p.Labels[binder.LabelRunRole] == role {
			n++
		}
	}
	return n
}
