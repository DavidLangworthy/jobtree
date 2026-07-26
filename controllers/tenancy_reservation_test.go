package controllers

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// conflictedNamespaceState builds a cluster where namespace "default" carries two
// Budgets with two different owners — ConflictMultipleOwners, so OwnerOf derives
// the empty fail-safe owner — plus a Run and a due Reservation in it.
//
// Both Budgets carry a real envelope: api/v1 rejects an envelope-less Budget, and
// a fixture the API server would refuse is not evidence about a legal system.
func conflictedNamespaceState(now time.Time) *ClusterState {
	rival := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "rival", Namespace: "default"},
		Spec: v1.BudgetSpec{
			Owner: "org:rival",
			Envelopes: []v1.BudgetEnvelope{{
				Name: "east", Flavor: "H100-80GB", Concurrency: 16,
				Selector: map[string]string{
					topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
					topology.LabelFabricDomain: "island-a",
				},
			}},
		},
	}
	state := &ClusterState{
		Nodes:   []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: []v1.Budget{h100Budget("team", "org:team", 16), rival},
	}
	state.Runs = map[string]*v1.Run{"default/train": h100Run("train", "org:team", 4)}
	state.Reservations = map[string]*v1.Reservation{
		"default/res": {
			ObjectMeta: v1.ObjectMeta{Name: "res", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:        v1.RunReference{Name: "train", Namespace: "default"},
				EarliestStart: v1.NewTime(now.Add(-time.Hour)),
			},
			Status: v1.ReservationStatus{State: "Pending", CountdownSeconds: ptrInt64(1020)},
		},
	}
	return state
}

// R7 §4: a namespace with no derived funding principal can never fund this
// reservation, so activation FAILS IT TERMINALLY — matching what `main` already
// did and what R7-tenancy-amendment.md:126-127 and :590 say this path does.
//
// This test exists because the first attempt got it backwards. A guard shipped on
// 2026-07-24 refused the tick and returned nil, arguing that terminating "would
// destroy a legitimate reservation over an admin typo" — the reaper shape. The
// 2026-07-25 adversarial panel disproved it unanimously by running the code on
// both branches: `main` terminated at tick 1; the branch left the reservation
// Pending at tick 20 with the backlog gauge frozen at 1020, because
// metrics.ClearReservationBacklog is reached only from terminal paths. The
// consequence seat ruled fixIsReaper=false and demonstrated recovery.
//
// So: terminal, gauge cleared, and an error naming the real cause — not
// cover.Plan's "owner and flavor must be set", which is about fields an R7 Run
// does not even have.
func TestActivateReservationFailsTerminallyWhenNamespaceHasNoOwner(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := conflictedNamespaceState(now)

	err := NewRunController(state, runClock{now: now}).ActivateReservations(now)
	if err == nil {
		t.Fatalf("a namespace with no funding principal can never activate; the failure must be reported")
	}
	if !strings.Contains(err.Error(), "no funding principal") {
		t.Errorf("the error must name the real cause (the namespace binding), got: %v", err)
	}
	if strings.Contains(err.Error(), "owner and flavor must be set") {
		t.Errorf("the operator must not be told about fields the Run does not have, got: %v", err)
	}

	res := state.Reservations["default/res"]
	if res.Status.State != "Failed" {
		t.Fatalf("reservation must fail TERMINALLY, got state %q — a Pending reservation that nothing "+
			"can ever activate is immortal", res.Status.State)
	}
	if res.Status.CountdownSeconds != nil {
		t.Errorf("a failed reservation must not keep counting down, got %v", *res.Status.CountdownSeconds)
	}
	// The gauge is the half of this defect that hides the other half.
	if backlog := metrics.Snapshot().ReservationBacklog; len(backlog) != 0 {
		t.Errorf("the backlog gauge must be cleared on the terminal path, still reporting %v", backlog)
	}

	run := state.Runs["default/train"]
	if !strings.Contains(run.Status.Message, "no funding principal") {
		t.Errorf("the run must say why it stopped, got %q", run.Status.Message)
	}
	if len(state.Leases) != 0 {
		t.Errorf("nothing may be minted for a namespace that cannot pay, got %d leases", len(state.Leases))
	}
}

// The other half of the panel's ruling: terminating is NOT a reaper, because the
// run recovers on its own once an admin repairs the binding. If that were false,
// failing the reservation really would destroy legal work and the 2026-07-24
// reasoning would have been right.
func TestRunRecoversAfterTerminalFailureWhenBindingIsRepaired(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := conflictedNamespaceState(now)

	if err := NewRunController(state, runClock{now: now}).ActivateReservations(now); err == nil {
		t.Fatalf("fixture is wrong: expected the conflicted namespace to fail its reservation")
	}
	if state.Reservations["default/res"].Status.State != "Failed" {
		t.Fatalf("fixture is wrong: expected a terminally failed reservation")
	}

	// The admin removes the second Budget. The namespace binds again.
	state.Budgets = []v1.Budget{h100Budget("team", "org:team", 16)}
	for h := 1; h <= 5; h++ {
		at := now.Add(time.Duration(h) * time.Hour)
		c := NewRunController(state, runClock{now: at})
		if err := c.Reconcile("default", "train"); err != nil {
			t.Fatalf("tick %d: Reconcile failed after the binding was repaired: %v", h, err)
		}
		if err := c.ActivateReservations(at); err != nil {
			t.Fatalf("tick %d: ActivateReservations failed after repair: %v", h, err)
		}
	}

	run := state.Runs["default/train"]
	if run.Status.Phase == RunPhaseFailed {
		t.Fatalf("REAPER: the run is Failed after the admin repaired the binding: %q", run.Status.Message)
	}

	// THE LOAD-BEARING ASSERTION, and the reason this test was rewritten.
	//
	// It used to assert only that the phase was not Failed and that the message no
	// longer mentioned the conflict. The 2026-07-25 fix-diff panel mutated the
	// repair out and the test still PASSED: an unrepaired run also sits Pending,
	// just with a different wrong message ("owner and flavor must be set"). So the
	// test proved nothing about recovery — a decorative test, the exact class the
	// playbook warns about, written to answer a panel.
	//
	// Recovery has an observable that permanent limbo does not: the run actually
	// emits its intent pods again. Assert that.
	if got := activeIntentPods(state, "default", "train"); got != 4 {
		t.Fatalf("recovery means the run emits its gang again: activeIntentPods = %d, want 4 "+
			"(an unrepaired run sits Pending forever and emits none)", got)
	}
	if strings.Contains(run.Status.Message, "no funding principal") ||
		strings.Contains(run.Status.Message, "owner and flavor must be set") {
		t.Fatalf("the run is stuck on the old conflict after repair: %q", run.Status.Message)
	}
	t.Logf("after repair: phase=%s pods=%d msg=%q",
		run.Status.Phase, activeIntentPods(state, "default", "train"), run.Status.Message)
}

// failReservationNoEnvelope had NO TEST anywhere in the repo, on either branch.
// That is precisely why the immortal-reservation defect could exist: the guard
// added in front of it made it unreachable, and nothing went red. The panel said
// so by name. This is that test.
//
// Stimulus: the namespace IS bound (one Budget, one owner), so the derived-owner
// guard does not fire — but the Budget owns no envelope of the run's flavor, so
// cover.Plan cannot fund it and opportunisticCoverPlan has nothing to attribute
// the promise to. That is the exact condition the function documents.
func TestFailReservationNoEnvelopeTerminatesAndClearsTheGauge(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Legal Budget, correct owner, wrong flavor: A100 envelope only, H100 run.
	wrongFlavor := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "team", Namespace: "default"},
		Spec: v1.BudgetSpec{
			Owner: "org:team",
			Envelopes: []v1.BudgetEnvelope{{
				Name: "west-a100", Flavor: "A100-40GB", Concurrency: 16,
				Selector: map[string]string{
					topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
					topology.LabelFabricDomain: "island-a",
				},
			}},
		},
	}
	state := &ClusterState{
		Nodes:   []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: []v1.Budget{wrongFlavor},
	}
	state.Runs = map[string]*v1.Run{"default/train": h100Run("train", "org:team", 4)}
	state.Reservations = map[string]*v1.Reservation{
		"default/res": {
			ObjectMeta: v1.ObjectMeta{Name: "res", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:        v1.RunReference{Name: "train", Namespace: "default"},
				EarliestStart: v1.NewTime(now.Add(-time.Hour)),
			},
			Status: v1.ReservationStatus{State: "Pending", CountdownSeconds: ptrInt64(1020)},
		},
	}

	// Sanity: the namespace really is bound, so this exercises the no-envelope
	// path and not the derived-owner guard sitting in front of it.
	if got := NewRunController(state, runClock{now: now}).evaluate(now).OwnerOf("default"); got != "org:team" {
		t.Fatalf("fixture is wrong: OwnerOf(default) = %q, want org:team — this test must reach "+
			"failReservationNoEnvelope, not the empty-owner guard", got)
	}

	err := NewRunController(state, runClock{now: now}).ActivateReservations(now)
	if err == nil {
		t.Fatalf("a reservation whose owner has no envelope of the run's flavor cannot be kept; "+
			"expected a terminal failure, got nil (leases=%d)", len(state.Leases))
	}
	if !strings.Contains(err.Error(), "no envelope to fund reservation") {
		t.Errorf("expected failReservationNoEnvelope's reason, got: %v", err)
	}

	res := state.Reservations["default/res"]
	if res.Status.State != "Failed" {
		t.Fatalf("failReservationNoEnvelope must mark the reservation Failed, got %q", res.Status.State)
	}
	if res.Status.CountdownSeconds != nil {
		t.Errorf("a failed reservation must not keep counting down, got %v", *res.Status.CountdownSeconds)
	}
	if backlog := metrics.Snapshot().ReservationBacklog; len(backlog) != 0 {
		t.Errorf("failReservationNoEnvelope must clear the backlog gauge, still reporting %v", backlog)
	}
}
