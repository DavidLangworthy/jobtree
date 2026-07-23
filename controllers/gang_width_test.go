package controllers

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// R2 part 2 — adopt at the gang's full active width.
//
// "Start together or not at all" (docs/index.md). Before this, the controller
// flipped a Run to Running on ANY open lease, so an N-wide run holding N−1
// slices reported healthy Running while N−1 containers charged budget forever,
// and every consumer that keys off Phase — runGangComplete, the elastic loop,
// the CLI — read it as whole.

// gangWidthState builds a 4-GPU, 1-GPU-per-pod run on a single 4-GPU node.
func gangWidthState() *ClusterState {
	return &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:team",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 16,
				}},
			},
		}},
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 4,
		}},
		Runs: map[string]*v1.Run{
			"default/gang": {
				ObjectMeta: v1.ObjectMeta{Name: "gang", Namespace: "default"},
				Spec: v1.RunSpec{
					Owner:     "org:ai:team",
					Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
				},
				Status: v1.RunStatus{Phase: RunPhasePending},
			},
		},
	}
}

// memberLease is one pod-lease of the gang: a single GPU slot on node-a.
func memberLease(slot int, role, reason string, now time.Time) v1.GPULease {
	return v1.GPULease{
		ObjectMeta: v1.ObjectMeta{
			Name:      "gang-lease-" + string(rune('a'+slot)),
			Namespace: "default",
			Labels:    map[string]string{binder.LabelRunName: "gang", binder.LabelGroupIndex: "0", binder.LabelRunRole: role},
		},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:ai:team",
			RunRef:         v1.RunReference{Name: "gang", Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-a#" + string(rune('0'+slot))}, Role: role},
			Interval:       v1.GPULeaseInterval{Start: v1.NewTime(now.Add(-time.Minute))},
			PaidByBudget:   "team",
			PaidByEnvelope: "west",
			Reason:         reason,
		},
	}
}

// memberPod is the intent pod the controller emitted for gang member i.
func memberPod(i int, reason string, extra map[string]string) binder.PodManifest {
	ann := map[string]string{
		binder.AnnotationExpectedWidth: "4",
		binder.AnnotationLeaseReason:   reason,
	}
	for k, v := range extra {
		ann[k] = v
	}
	return binder.PodManifest{
		Namespace: "default",
		Name:      "gang-active-" + string(rune('0'+i)),
		GPUs:      1,
		Labels: map[string]string{
			binder.LabelRunName:    "gang",
			binder.LabelRunRole:    binder.RoleActive,
			binder.LabelGroupIndex: "0",
		},
		Annotations: ann,
	}
}

func activePodNames(state *ClusterState) []string {
	var names []string
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Labels[binder.LabelRunRole] == binder.RoleActive {
			names = append(names, p.Name)
		}
	}
	return names
}

// A gang holding 3 of its 4 slices has not started. It must not report Running,
// and it must converge: the missing member is re-emitted, and the run adopts the
// instant the last lease lands.
func TestReconcileDoesNotAdoptPartialGang(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := gangWidthState()
	for i := 0; i < 3; i++ {
		state.Leases = append(state.Leases, memberLease(i, binder.RoleActive, "Start", now))
		state.Pods = append(state.Pods, memberPod(i, "Start", nil))
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	run := state.Runs["default/gang"]
	if run.Status.Phase == RunPhaseRunning {
		t.Fatalf("a 3-of-4 gang must not report Running: %s", run.Status.Message)
	}
	if run.Status.Phase != RunPhasePending {
		t.Errorf("phase = %s, want Pending while assembling", run.Status.Phase)
	}
	if !strings.Contains(run.Status.Message, "assembling gang: 3/4") {
		t.Errorf("message must name the deficit, got %q", run.Status.Message)
	}
	// Pending is honest only because the held capacity and its cost are reported.
	if run.Status.Width == nil || run.Status.Width.Allocated != 3 {
		t.Errorf("width must report the 3 GPUs actually held, got %+v", run.Status.Width)
	}
	if run.Status.Width != nil && run.Status.Width.Pending != "Assemble to 4" {
		t.Errorf("width.pending = %q, want %q", run.Status.Width.Pending, "Assemble to 4")
	}
	if run.Status.Funding == nil {
		t.Errorf("funding must report what the held slices charge")
	}
	if len(state.Leases) != 3 {
		t.Fatalf("adoption must never mint a lease, got %d", len(state.Leases))
	}
	// The missing member's pod is re-emitted so the plugin can bind it; the
	// three surviving pods are untouched.
	if got := activePodNames(state); len(got) != 4 {
		t.Fatalf("expected the gang topped back up to 4 active pods, got %v", got)
	}

	// The last slice lands: now it is a real, whole gang.
	state.Leases = append(state.Leases, memberLease(3, binder.RoleActive, "Start", now))
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("a whole gang must adopt to Running, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
	if !strings.Contains(run.Status.Message, "adopted 4 GPUs") {
		t.Errorf("unexpected adoption message: %q", run.Status.Message)
	}
	if len(state.Leases) != 4 {
		t.Errorf("adoption must never mint a lease, got %d", len(state.Leases))
	}
}

// Spare leases are held standby capacity that does no work. A run whose only
// open lease is a Spare has nothing running, so it must not be adopted — the
// old open>0 gate counted spares and flipped such a run to Running.
func TestReconcileDoesNotAdoptSpareOnlyLeases(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := gangWidthState()
	state.Leases = append(state.Leases, memberLease(0, binder.RoleSpare, "Start", now))

	mirrorPods(state)
	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	run := state.Runs["default/gang"]
	if run.Status.Phase == RunPhaseRunning {
		t.Fatalf("a spare-only run holds no active width and must not report Running: %s", run.Status.Message)
	}
	if strings.Contains(run.Status.Message, "adopted") {
		t.Errorf("spare leases must not be adopted, got %q", run.Status.Message)
	}
}

// Elastic-grow leases are width added ON TOP OF the base gang, so they must not
// stand in for missing base-gang width. A malleable run whose base nodes all
// failed keeps its grow leases open; counting those would let it adopt to
// Running at "full width" while holding zero base-gang GPUs — and, worse, clear
// the checkpoint grace that is supposed to bound its recovery.
//
// A Lease records no cohort, so Spec.Reason is the only durable thing separating
// a grow lease from a base one.
func TestReconcileDoesNotAdoptOnGrowLeasesAlone(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := gangWidthState()
	// Base gang gone (its nodes failed); 4 GPUs of grow width survive.
	for i := 0; i < 4; i++ {
		state.Leases = append(state.Leases, memberLease(i, binder.RoleActive, binder.LeaseReasonGrow, now))
	}
	deadline := v1.NewTime(now.Add(time.Hour))
	run := state.Runs["default/gang"]
	run.Status.CheckpointDeadline = &deadline

	mirrorPods(state)
	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if run.Status.Phase == RunPhaseRunning {
		t.Fatalf("grow width must not stand in for a missing base gang: %s", run.Status.Message)
	}
	if run.Status.CheckpointDeadline == nil {
		t.Errorf("the node-failure grace must survive: nothing recovered the base gang")
	}
}

// Swap and Promise leases each stand in for a real base-gang member, so unlike
// grow leases they DO count toward the adopted width.
func TestSwapLeasesCountTowardGangWidth(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := gangWidthState()
	for i := 0; i < 3; i++ {
		state.Leases = append(state.Leases, memberLease(i, binder.RoleActive, "Start", now))
	}
	// The fourth member was swapped onto a spare after its node failed.
	state.Leases = append(state.Leases, memberLease(3, binder.RoleActive, "Swap", now))

	mirrorPods(state)
	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	run := state.Runs["default/gang"]
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("a gang made whole by a swap must adopt to Running, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
}

// A MALLEABLE run may legitimately run anywhere in [Min, Max] — quota-semantics'
// demote-not-kill. A malleable run that lost a group to node failure is parked
// Pending with a checkpoint deadline while it still holds a valid width; holding
// it to TotalGPUs would leave it "assembling" until the grace expired and then
// TERMINALLY FAIL a run that was happily continuing at reduced width.
func TestMalleableRunAdoptsAtMinWidth(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := gangWidthState()
	run := state.Runs["default/gang"]
	run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 2, MaxTotalGPUs: 4, StepGPUs: 1}
	// Node failure took two of the four slices; two remain, at the minimum width.
	for i := 0; i < 2; i++ {
		state.Leases = append(state.Leases, memberLease(i, binder.RoleActive, "Start", now))
	}
	deadline := v1.NewTime(now.Add(time.Hour))
	run.Status.CheckpointDeadline = &deadline

	mirrorPods(state)
	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("a malleable run at its minimum width is running, not broken: %s (%s)", run.Status.Phase, run.Status.Message)
	}
	if run.Status.CheckpointDeadline != nil {
		t.Errorf("the run recovered to a runnable width; the node-failure grace must not still be counting down")
	}
}

// ...but below its minimum a malleable run is a broken gang like any other.
func TestMalleableRunBelowMinDoesNotAdopt(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := gangWidthState()
	run := state.Runs["default/gang"]
	run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 3, MaxTotalGPUs: 4, StepGPUs: 1}
	state.Leases = append(state.Leases, memberLease(0, binder.RoleActive, "Start", now))
	state.Leases = append(state.Leases, memberLease(1, binder.RoleActive, "Start", now))

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if run.Status.Phase == RunPhaseRunning {
		t.Fatalf("2 GPUs is below the run's 3-GPU minimum; it must not report Running: %s", run.Status.Message)
	}
	if !strings.Contains(run.Status.Message, "assembling gang: 2/3") {
		t.Errorf("the deficit must be named against the minimum runnable width, got %q", run.Status.Message)
	}
}

// The top-up keys presence by pod NAME. A member lost from the MIDDLE of the
// cohort must come back as itself; a count-based scan would instead rebuild the
// last index, duplicating one pod while the missing one never returns.
func TestTopUpRecreatesTheMissingMember(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := gangWidthState()
	for _, i := range []int{0, 1, 3} {
		state.Leases = append(state.Leases, memberLease(i, binder.RoleActive, "Start", now))
		state.Pods = append(state.Pods, memberPod(i, "Start", nil))
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	seen := map[string]int{}
	for _, n := range activePodNames(state) {
		seen[n]++
	}
	if seen["gang-active-2"] != 1 {
		t.Errorf("the missing middle member must be re-emitted exactly once, got %d", seen["gang-active-2"])
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("pod %s emitted %d times; the top-up must never duplicate a member", name, n)
		}
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 distinct active pods, got %v", seen)
	}
}

// A Promise gang (R3) is pre-authorized and skips the plugin's funding gate,
// which is expected to refuse it until quota returns. Re-emitting one of its
// members as a plain "Start" pod would send it into that gate and wedge the run,
// so the top-up must recover the gang's provenance — here from the open leases,
// the durable record, because every pod is gone.
func TestTopUpPreservesPromiseProvenance(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := gangWidthState()
	for i := 0; i < 3; i++ {
		state.Leases = append(state.Leases, memberLease(i, binder.RoleActive, binder.LeaseReasonPromise, now))
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "gang"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	pods := 0
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Labels[binder.LabelRunRole] != binder.RoleActive {
			continue
		}
		pods++
		if got := p.Annotations[binder.AnnotationLeaseReason]; got != binder.LeaseReasonPromise {
			t.Errorf("pod %s lease-reason = %q, want Promise (a Start pod would hit the funding gate the promise exists to bypass)", p.Name, got)
		}
		if p.Annotations[binder.AnnotationPayerBudget] != "team" || p.Annotations[binder.AnnotationPayerEnvelope] != "west" {
			t.Errorf("pod %s lost the payer provenance the plugin mints from: %v", p.Name, p.Annotations)
		}
		if p.Annotations[binder.AnnotationPayerOwner] != "org:ai:team" {
			t.Errorf("pod %s payer-owner = %q", p.Name, p.Annotations[binder.AnnotationPayerOwner])
		}
	}
	if pods != 4 {
		t.Fatalf("expected the promise gang topped back up to 4 pods, got %d", pods)
	}
}

// A half-applied ACTIVATION must keep its reservation held while the gang is
// still short: releasing it would hand the reserved capacity to another run
// while this one is still assembling onto it.
func TestActivateReservationHoldsReservationOnPartialGang(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	earliest := v1.NewTime(now.Add(-time.Minute))
	state := gangWidthState()
	for i := 0; i < 3; i++ {
		state.Leases = append(state.Leases, memberLease(i, binder.RoleActive, "Start", now))
		state.Pods = append(state.Pods, memberPod(i, "Start", nil))
	}
	state.Runs["default/gang"].Status.PendingReservation = ptrString("gang-res-1")
	state.Reservations = map[string]*v1.Reservation{
		"default/gang-res-1": {
			ObjectMeta: v1.ObjectMeta{Name: "gang-res-1", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:         v1.RunReference{Name: "gang", Namespace: "default"},
				IntendedSlice:  v1.IntendedSlice{Domain: map[string]string{topology.LabelRegion: "us-west"}},
				PayingEnvelope: "west",
				EarliestStart:  earliest,
			},
			Status: v1.ReservationStatus{State: "Pending"},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.ActivateReservations(now); err != nil {
		t.Fatalf("activate failed: %v", err)
	}

	run := state.Runs["default/gang"]
	if run.Status.Phase == RunPhaseRunning {
		t.Fatalf("a 3-of-4 gang must not be activated to Running: %s", run.Status.Message)
	}
	if run.Status.PendingReservation == nil {
		t.Errorf("the reservation must stay attached while the gang is short")
	}
	res := state.Reservations["default/gang-res-1"]
	if res.Status.State != "Pending" {
		t.Errorf("reservation = %s, want it still Pending (releasing it hands the capacity away mid-assembly)", res.Status.State)
	}
	if len(state.Leases) != 3 {
		t.Errorf("activation must never mint a lease, got %d", len(state.Leases))
	}

	// The last slice lands: the activation completes and the reservation releases.
	state.Leases = append(state.Leases, memberLease(3, binder.RoleActive, "Start", now))
	if err := controller.ActivateReservations(now); err != nil {
		t.Fatalf("second activate failed: %v", err)
	}
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("a whole gang must activate to Running, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
	if run.Status.PendingReservation != nil {
		t.Errorf("pendingReservation should clear on adoption")
	}
	if res.Status.State != "Released" || res.Status.Reason != "Activated" {
		t.Errorf("reservation = %s/%s, want Released/Activated", res.Status.State, res.Status.Reason)
	}
}
