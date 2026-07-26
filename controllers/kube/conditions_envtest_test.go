package kube

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// R11 verification, against a real apiserver.
//
// The unit tests in api/v1 prove the vocabulary is internally coherent. What they
// cannot prove is that the conditions SURVIVE the round trip: a `[]metav1.Condition`
// with no listType=map marker, no required fields, or a reason the apiserver's own
// validation rejects (empty, or not matching its regex) is dropped or refused at
// write time, and every unit test still passes. That failure mode is the whole
// reason this file exists.

// conditionOf returns a run's condition, or an error — never t.Fatalf, because it
// runs inside retry loops and the cached client legitimately lags a fresh Create.
// Fataling on the first miss made this a race, not an assertion.
func conditionOf(runName, condType string) (*metav1.Condition, error) {
	var run v1.Run
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: runName}, &run); err != nil {
		return nil, err
	}
	return meta.FindStatusCondition(run.Status.Conditions, condType), nil
}

// waitForRunCondition is the envtest stand-in for `kubectl wait
// --for=condition=<type> run/<name>`: kubectl's implementation is exactly this
// poll over status.conditions[type].status == "True", so if this works, so does
// the command a researcher would actually type (R11 verification item 2).
func waitForRunCondition(t *testing.T, runName, condType string) *metav1.Condition {
	t.Helper()
	var found *metav1.Condition
	eventually(t, 30*time.Second, func() error {
		var run v1.Run
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: runName}, &run); err != nil {
			return err
		}
		cond := meta.FindStatusCondition(run.Status.Conditions, condType)
		if cond == nil {
			return fmt.Errorf("condition %s not present (phase %q, conditions %+v)", condType, run.Status.Phase, run.Status.Conditions)
		}
		if cond.Status != metav1.ConditionTrue {
			return fmt.Errorf("condition %s = %s/%s, want True", condType, cond.Status, cond.Reason)
		}
		found = cond
		return nil
	})
	return found
}

// A run with no capacity anywhere: nothing is True, and the reason for THAT is
// the researcher's actual question. It has to ride on a False condition or the
// vocabulary is unreachable exactly where it matters most.
func TestAnUnadmittedRunPersistsAReadableReason(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "nowhere", Namespace: "default"},
		Spec: v1.RunSpec{
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	eventually(t, 30*time.Second, func() error {
		cond, err := conditionOf("nowhere", v1.RunConditionAdmitted)
		if err != nil {
			return err
		}
		if cond == nil {
			return fmt.Errorf("Admitted condition not written yet")
		}
		if cond.Status != metav1.ConditionFalse {
			return fmt.Errorf("Admitted = %s, want False on a run with no capacity", cond.Status)
		}
		if cond.Reason == v1.RunReasonNotApplicable || cond.Reason == "" {
			return fmt.Errorf("Admitted reason = %q; an unadmitted run must say WHY", cond.Reason)
		}
		return nil
	})
	assertPhaseMatchesConditions(t, "nowhere")
}

// The lifecycle, condition by condition, on real objects: a run reaches
// Scheduled+Running when the plugin's leases land, and at EVERY observation the
// persisted phase is exactly what the persisted conditions derive.
func TestRunConditionsSurviveTheAPIServerAcrossTheLifecycle(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "conditioned", Namespace: "default"},
		Spec: v1.RunSpec{
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Pods out, no leases yet: Admitted=True/Scheduling, and NOT Running.
	waitForRunPods(t, "conditioned", 4)
	eventually(t, 15*time.Second, func() error {
		cond, err := conditionOf("conditioned", v1.RunConditionAdmitted)
		if err != nil {
			return err
		}
		if cond == nil || cond.Status != metav1.ConditionTrue {
			return fmt.Errorf("Admitted = %+v, want True once the intent gang is emitted", cond)
		}
		return nil
	})
	if meta.IsStatusConditionTrue(mustRun(t, "conditioned").Status.Conditions, v1.RunConditionRunning) {
		t.Error("a run whose gang holds no leases must not report Running=True")
	}
	assertPhaseMatchesConditions(t, "conditioned")

	seedPluginLeases(t, "conditioned")

	// This is exactly what `kubectl wait --for=condition=Running run/conditioned`
	// polls, so proving it here proves the command.
	running := waitForRunCondition(t, "conditioned", v1.RunConditionRunning)
	if running.Reason != v1.RunStateGangBound.Reason {
		t.Errorf("Running reason = %q, want %q", running.Reason, v1.RunStateGangBound.Reason)
	}
	if !meta.IsStatusConditionTrue(mustRun(t, "conditioned").Status.Conditions, v1.RunConditionScheduled) {
		t.Error("a run at full width must also report Scheduled=True")
	}
	assertPhaseMatchesConditions(t, "conditioned")

	// Completion is asserted where the terminal pod phase already has to be
	// hand-injected: TestRunCompletesWhenPodsSucceed, which owns the (shrink-only,
	// capped-at-two) antifake allowlist entry for it. Duplicating that injection
	// here to re-prove the same polling mechanism would have grown a ratchet the
	// repo deliberately keeps closed.
}

func mustRun(t *testing.T, name string) *v1.Run {
	t.Helper()
	var run v1.Run
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: name}, &run); err != nil {
		t.Fatalf("get run %s: %v", name, err)
	}
	return &run
}

// The invariant, checked on the PERSISTED object rather than in memory. The
// in-process oracle (INV-PHASE-DERIVED) already checks it on every engine return;
// this checks it survives serialization, which is a different claim.
func assertPhaseMatchesConditions(t *testing.T, name string) {
	t.Helper()
	run := mustRun(t, name)
	if len(run.Status.Conditions) == 0 {
		t.Fatalf("run %s persisted NO conditions — the CRD schema dropped them, and every unit test would still pass", name)
	}
	if got := v1.DeriveRunPhase(run.Status.Conditions); got != run.Status.Phase {
		t.Errorf("persisted phase %q but persisted conditions derive %q: %+v", run.Status.Phase, got, run.Status.Conditions)
	}
	for _, cond := range run.Status.Conditions {
		if cond.Reason == "" {
			t.Errorf("condition %s came back with an empty reason; the apiserver would have rejected the write", cond.Type)
		}
		if cond.LastTransitionTime.IsZero() {
			t.Errorf("condition %s came back with no lastTransitionTime", cond.Type)
		}
	}
}

// A Lease is the object that charges a budget and holds GPUs, so its Active/Closed
// conditions are the ones an operator most wants to select on.
//
// This test is also the one that caught the first draft being INERT. Conditions
// were stamped in admission.PodLeaseWithRole, at the mint — and Status is a
// subresource, so the plugin's Create drops it and an open lease persisted no
// conditions at all. Every unit test passed. The derivation moved to the
// controller's observation point (Bridge.apply), which is a write that already
// happens; the sole committer pays nothing.
func TestLeaseConditionsSurviveTheAPIServer(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "leased", Namespace: "default"},
		Spec: v1.RunSpec{
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitForRunPods(t, "leased", 4)
	seedPluginLeases(t, "leased")
	waitForRunPhase(t, "leased", "Running")
	leases := waitForRunLeases(t, "leased", 1)

	eventually(t, 15*time.Second, func() error {
		var lease v1.GPULease
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: leases[0].Name}, &lease); err != nil {
			return err
		}
		if !meta.IsStatusConditionTrue(lease.Status.Conditions, v1.LeaseConditionActive) {
			return fmt.Errorf("an open lease must persist Active=True, got %+v", lease.Status.Conditions)
		}
		return nil
	})

	// Delete the run: the R12 finalizer closes the lease, and the closure has to
	// carry through into the conditions on the SAME write.
	if err := kubeClient.Delete(suiteCtx, mustRun(t, "leased")); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	eventually(t, 30*time.Second, func() error {
		var lease v1.GPULease
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: leases[0].Name}, &lease); err != nil {
			return err
		}
		if !lease.Status.Closed {
			return fmt.Errorf("lease not closed yet")
		}
		cond := meta.FindStatusCondition(lease.Status.Conditions, v1.LeaseConditionClosed)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			return fmt.Errorf("a closed lease must persist Closed=True, got %+v", lease.Status.Conditions)
		}
		if cond.Reason != lease.Status.ClosureReason {
			return fmt.Errorf("Closed reason = %q but closureReason = %q; the two must not drift", cond.Reason, lease.Status.ClosureReason)
		}
		if meta.IsStatusConditionTrue(lease.Status.Conditions, v1.LeaseConditionActive) {
			return fmt.Errorf("a closed lease must not still report Active=True")
		}
		return nil
	})
}

// Budget health is the operator-facing half: an overcommitted envelope is a legal,
// reportable state (quota and capacity vary independently), and it must be visible
// without recomputing the funding derivation client-side.
func TestBudgetHealthyConditionSurvivesTheAPIServer(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)

	eventually(t, 30*time.Second, func() error {
		var budget v1.Budget
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "team"}, &budget); err != nil {
			return err
		}
		cond := meta.FindStatusCondition(budget.Status.Conditions, v1.BudgetConditionHealthy)
		if cond == nil {
			return fmt.Errorf("no Healthy condition yet: %+v", budget.Status)
		}
		if cond.Status != metav1.ConditionTrue || cond.Reason != v1.BudgetReasonHealthy {
			return fmt.Errorf("Healthy = %s/%s, want True/%s", cond.Status, cond.Reason, v1.BudgetReasonHealthy)
		}
		if budget.Status.ObservedGeneration != budget.Generation {
			return fmt.Errorf("observedGeneration = %d, want the budget's generation %d",
				budget.Status.ObservedGeneration, budget.Generation)
		}
		return nil
	})
}
