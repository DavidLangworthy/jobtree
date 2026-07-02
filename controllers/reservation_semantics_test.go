package controllers

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func h100Node(name string, gpus int) topology.SourceNode {
	return topology.SourceNode{
		Name: name,
		Labels: map[string]string{
			topology.LabelRegion:       "us-west",
			topology.LabelCluster:      "cluster-a",
			topology.LabelFabricDomain: "island-a",
			topology.LabelGPUFlavor:    "H100-80GB",
		},
		GPUs: gpus,
	}
}

func h100Budget(budgetName, owner string, concurrency int32) v1.Budget {
	return v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: budgetName},
		Spec: v1.BudgetSpec{
			Owner: owner,
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "west",
				Flavor:      "H100-80GB",
				Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
				Concurrency: concurrency,
			}},
		},
	}
}

func h100Run(name, owner string, gpus int32) *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     owner,
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: gpus},
		},
	}
}

// assertInvariantNoPendingReservationForRunningRun checks invariant 8 of the
// testing plan: no Pending reservation may exist for a Running run.
func assertInvariantNoPendingReservationForRunningRun(t *testing.T, state *ClusterState) {
	t.Helper()
	for key, res := range state.Reservations {
		if res.Status.State != "Pending" && res.Status.State != "" {
			continue
		}
		runKey := keys.NamespacedKey(res.Spec.RunRef.Namespace, res.Spec.RunRef.Name)
		run, ok := state.Runs[runKey]
		if !ok {
			continue
		}
		if run.Status.Phase == RunPhaseRunning {
			t.Errorf("invariant 8 violated: reservation %s is Pending but run %s is Running", key, runKey)
		}
	}
}

func openLeaseCount(state *ClusterState) int {
	count := 0
	for _, lease := range state.Leases {
		if !lease.Status.Closed {
			count++
		}
	}
	return count
}

// R7: a reservation blocked only by budget headroom (physical capacity is
// plentiful) must not trigger any preemption at activation — it re-forecasts
// and reschedules instead.
func TestActivateReservationBudgetOnlyShortfallDoesNotPreempt(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes: []topology.SourceNode{h100Node("node-a", 12)},
		Budgets: []v1.Budget{
			h100Budget("team", "org:team", 4),
			h100Budget("bystander", "org:bystander", 16),
		},
	}
	state.Runs = map[string]*v1.Run{
		"default/hog":       h100Run("hog", "org:team", 4),
		"default/bystander": h100Run("bystander", "org:bystander", 2),
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "hog"); err != nil {
		t.Fatalf("hog reconcile failed: %v", err)
	}
	if err := controller.Reconcile("default", "bystander"); err != nil {
		t.Fatalf("bystander reconcile failed: %v", err)
	}
	if state.Runs["default/hog"].Status.Phase != RunPhaseRunning || state.Runs["default/bystander"].Status.Phase != RunPhaseRunning {
		t.Fatalf("expected hog and bystander running, got %s and %s",
			state.Runs["default/hog"].Status.Phase, state.Runs["default/bystander"].Status.Phase)
	}

	// blocked needs 4 GPUs: 6 are free (capacity fine), but org:team's
	// envelope is exhausted by hog — a pure budget shortfall.
	state.Runs["default/blocked"] = h100Run("blocked", "org:team", 4)
	if err := controller.Reconcile("default", "blocked"); err != nil {
		t.Fatalf("blocked reconcile failed: %v", err)
	}
	if len(state.Reservations) != 1 {
		t.Fatalf("expected a reservation for blocked, got %d", len(state.Reservations))
	}
	for _, res := range state.Reservations {
		res.Spec.EarliestStart = v1.NewTime(now) // force it due
	}

	leasesBefore := openLeaseCount(state)
	activation := now.Add(30 * time.Minute)
	controller.Clock = runClock{now: activation}
	if err := controller.ActivateReservations(activation); err != nil {
		t.Fatalf("activation returned error: %v", err)
	}

	if got := openLeaseCount(state); got != leasesBefore {
		t.Errorf("expected no leases closed or created, had %d open, now %d", leasesBefore, got)
	}
	for _, lease := range state.Leases {
		if lease.Status.Closed {
			t.Errorf("lease %s was closed (%s): budget shortfall must not preempt", lease.Name, lease.Status.ClosureReason)
		}
	}
	if actions := metrics.Snapshot().ResolverActions; len(actions) != 0 {
		t.Errorf("expected no resolver actions, got %v", actions)
	}
	if phase := state.Runs["default/blocked"].Status.Phase; phase != RunPhasePending {
		t.Errorf("expected blocked to stay pending, got %s", phase)
	}
	// The reservation was rescheduled: a Pending reservation still exists.
	pending := 0
	for _, res := range state.Reservations {
		if res.Status.State == "Pending" {
			pending++
		}
	}
	if pending != 1 {
		t.Errorf("expected one rescheduled pending reservation, got %d", pending)
	}
	assertInvariantNoPendingReservationForRunningRun(t, state)
}

// R7 companion: a genuine capacity deficit still clears through the resolver
// (the budget-only guard must not disable legitimate preemption).
func TestActivateReservationCapacityDeficitStillResolves(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes: []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: []v1.Budget{
			h100Budget("team", "org:team", 16),
			h100Budget("victim", "org:victim", 16),
		},
	}
	state.Runs = map[string]*v1.Run{
		"default/victim": h100Run("victim", "org:victim", 8),
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "victim"); err != nil {
		t.Fatalf("victim reconcile failed: %v", err)
	}

	// waiter needs all 8 GPUs; the node is full — a pure capacity deficit.
	state.Runs["default/waiter"] = h100Run("waiter", "org:team", 8)
	if err := controller.Reconcile("default", "waiter"); err != nil {
		t.Fatalf("waiter reconcile failed: %v", err)
	}
	if len(state.Reservations) != 1 {
		t.Fatalf("expected a reservation for waiter, got %d", len(state.Reservations))
	}
	for _, res := range state.Reservations {
		res.Spec.EarliestStart = v1.NewTime(now)
	}

	activation := now.Add(30 * time.Minute)
	controller.Clock = runClock{now: activation}
	if err := controller.ActivateReservations(activation); err != nil {
		t.Fatalf("activation returned error: %v", err)
	}

	if phase := state.Runs["default/waiter"].Status.Phase; phase != RunPhaseRunning {
		t.Fatalf("expected waiter running after preemption, got %s", phase)
	}
	if phase := state.Runs["default/victim"].Status.Phase; phase != RunPhaseFailed {
		t.Fatalf("expected victim preempted, got %s", phase)
	}
	assertInvariantNoPendingReservationForRunningRun(t, state)
}

// R8: a reservation referencing a deleted run is marked Failed and does not
// prevent a later reservation from activating; the error surfaces in the
// aggregate.
func TestActivateReservationsIsolatesFailures(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: []v1.Budget{h100Budget("team", "org:team", 16)},
	}
	state.Runs = map[string]*v1.Run{
		"default/runnable": h100Run("runnable", "org:team", 4),
	}
	past := v1.NewTime(now.Add(-time.Hour))
	state.Reservations = map[string]*v1.Reservation{
		// "aaa-" sorts first, so the broken reservation is visited first.
		"default/aaa-orphan": {
			ObjectMeta: v1.ObjectMeta{Name: "aaa-orphan", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:        v1.RunReference{Name: "deleted-run", Namespace: "default"},
				EarliestStart: past,
			},
			Status: v1.ReservationStatus{State: "Pending"},
		},
		"default/bbb-runnable": {
			ObjectMeta: v1.ObjectMeta{Name: "bbb-runnable", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:        v1.RunReference{Name: "runnable", Namespace: "default"},
				EarliestStart: past,
			},
			Status: v1.ReservationStatus{State: "Pending"},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	err := controller.ActivateReservations(now)
	if err == nil {
		t.Fatalf("expected aggregate error mentioning the orphaned reservation")
	}
	if !strings.Contains(err.Error(), "aaa-orphan") {
		t.Errorf("expected error to identify the failing reservation, got: %v", err)
	}

	if phase := state.Runs["default/runnable"].Status.Phase; phase != RunPhaseRunning {
		t.Errorf("expected runnable to activate despite earlier failure, got %s", phase)
	}
	orphan := state.Reservations["default/aaa-orphan"]
	if orphan.Status.State != "Failed" {
		t.Errorf("expected orphaned reservation marked Failed, got %s", orphan.Status.State)
	}
	if runnable := state.Reservations["default/bbb-runnable"]; runnable.Status.State != "Released" {
		t.Errorf("expected activated reservation released, got %s", runnable.Status.State)
	}
	assertInvariantNoPendingReservationForRunningRun(t, state)
}

// R9 scenario: reserve → capacity frees → direct bind → activation tick.
// The direct bind must release the pending reservation, and the activation
// tick must not double-materialize.
func TestDirectBindReleasesPendingReservation(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes: []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: []v1.Budget{
			h100Budget("team", "org:team", 16),
			h100Budget("hog", "org:hog", 16),
		},
	}
	state.Runs = map[string]*v1.Run{
		"default/hog": h100Run("hog", "org:hog", 8),
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "hog"); err != nil {
		t.Fatalf("hog reconcile failed: %v", err)
	}

	// waiter can't place while hog holds the node: it reserves.
	state.Runs["default/waiter"] = h100Run("waiter", "org:team", 8)
	if err := controller.Reconcile("default", "waiter"); err != nil {
		t.Fatalf("waiter reconcile failed: %v", err)
	}
	waiter := state.Runs["default/waiter"]
	if waiter.Status.PendingReservation == nil {
		t.Fatalf("expected waiter to hold a pending reservation")
	}
	assertInvariantNoPendingReservationForRunningRun(t, state)

	// Capacity frees before the reservation's earliest start.
	closed := v1.NewTime(now.Add(10 * time.Minute))
	for i := range state.Leases {
		state.Leases[i].Status.Closed = true
		state.Leases[i].Status.Ended = &closed
		state.Leases[i].Status.ClosureReason = "Completed"
	}

	// The next reconcile binds directly.
	bindTime := now.Add(20 * time.Minute)
	controller.Clock = runClock{now: bindTime}
	if err := controller.Reconcile("default", "waiter"); err != nil {
		t.Fatalf("waiter direct-bind reconcile failed: %v", err)
	}
	if waiter.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected waiter running after direct bind, got %s", waiter.Status.Phase)
	}
	if waiter.Status.PendingReservation != nil || waiter.Status.EarliestStart != nil {
		t.Errorf("expected reservation pointers cleared on direct bind, got %v / %v",
			waiter.Status.PendingReservation, waiter.Status.EarliestStart)
	}
	for key, res := range state.Reservations {
		if res.Status.State == "Pending" {
			t.Errorf("expected no pending reservations after direct bind, %s is %s", key, res.Status.State)
		}
	}
	assertInvariantNoPendingReservationForRunningRun(t, state)

	// The activation tick must be a no-op for the already-running waiter.
	leasesBefore := len(state.Leases)
	podsBefore := len(state.Pods)
	tick := now.Add(2 * time.Hour)
	controller.Clock = runClock{now: tick}
	if err := controller.ActivateReservations(tick); err != nil {
		t.Fatalf("activation tick failed: %v", err)
	}
	if len(state.Leases) != leasesBefore || len(state.Pods) != podsBefore {
		t.Fatalf("activation tick double-materialized: leases %d→%d, pods %d→%d",
			leasesBefore, len(state.Leases), podsBefore, len(state.Pods))
	}
	assertInvariantNoPendingReservationForRunningRun(t, state)
}

// R9 phase guard: even if a Pending reservation survives for a Running run
// (state written by an older version, or a race), the activation tick
// releases it instead of materializing a second time.
func TestActivateReservationSkipsRunningRun(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: []v1.Budget{h100Budget("team", "org:team", 16)},
	}
	state.Runs = map[string]*v1.Run{
		"default/runner": h100Run("runner", "org:team", 4),
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "runner"); err != nil {
		t.Fatalf("runner reconcile failed: %v", err)
	}
	if state.Runs["default/runner"].Status.Phase != RunPhaseRunning {
		t.Fatalf("expected runner running")
	}

	// Inject a stale Pending reservation for the already-running run.
	past := v1.NewTime(now.Add(-time.Hour))
	state.Reservations["default/stale"] = &v1.Reservation{
		ObjectMeta: v1.ObjectMeta{Name: "stale", Namespace: "default"},
		Spec: v1.ReservationSpec{
			RunRef:        v1.RunReference{Name: "runner", Namespace: "default"},
			EarliestStart: past,
		},
		Status: v1.ReservationStatus{State: "Pending"},
	}

	leasesBefore := len(state.Leases)
	if err := controller.ActivateReservations(now); err != nil {
		t.Fatalf("activation failed: %v", err)
	}
	if len(state.Leases) != leasesBefore {
		t.Fatalf("stale reservation double-materialized: %d→%d leases", leasesBefore, len(state.Leases))
	}
	stale := state.Reservations["default/stale"]
	if stale.Status.State != "Released" || stale.Status.Reason != "Superseded" {
		t.Errorf("expected stale reservation Released/Superseded, got %s/%s", stale.Status.State, stale.Status.Reason)
	}
	assertInvariantNoPendingReservationForRunningRun(t, state)
}

// Ruling 2026-07-02 (PR #13 item 3): a stale Pending reservation must not
// resurrect a Failed run — release it and require an explicit resubmit.
func TestActivateReservationDoesNotResurrectFailedRun(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: []v1.Budget{h100Budget("team", "org:team", 16)},
	}
	failed := h100Run("victim", "org:team", 4)
	failed.Status.Phase = RunPhaseFailed
	failed.Status.Message = "ended by resolver"
	state.Runs = map[string]*v1.Run{"default/victim": failed}
	past := v1.NewTime(now.Add(-time.Hour))
	state.Reservations = map[string]*v1.Reservation{
		"default/stale": {
			ObjectMeta: v1.ObjectMeta{Name: "stale", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:        v1.RunReference{Name: "victim", Namespace: "default"},
				EarliestStart: past,
			},
			Status: v1.ReservationStatus{State: "Pending"},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.ActivateReservations(now); err != nil {
		t.Fatalf("activation failed: %v", err)
	}
	if len(state.Leases) != 0 || len(state.Pods) != 0 {
		t.Fatalf("stale reservation resurrected a Failed run: %d leases, %d pods", len(state.Leases), len(state.Pods))
	}
	if phase := state.Runs["default/victim"].Status.Phase; phase != RunPhaseFailed {
		t.Errorf("expected run to stay Failed, got %s", phase)
	}
	stale := state.Reservations["default/stale"]
	if stale.Status.State != "Released" || stale.Status.Reason != "Superseded" {
		t.Errorf("expected stale reservation Released/Superseded, got %s/%s", stale.Status.State, stale.Status.Reason)
	}
	assertInvariantNoPendingReservationForRunningRun(t, state)
}

// Review finding (PR #13 item 1): when the budget-shortfall re-forecast
// itself fails permanently (here: every envelope is gone), the reservation
// must be marked Failed instead of retrying as Pending forever.
func TestActivateReservationFailsTerminallyWhenReforecastFails(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: nil, // the budget was deleted after the reservation was made
	}
	state.Runs = map[string]*v1.Run{"default/blocked": h100Run("blocked", "org:team", 4)}
	past := v1.NewTime(now.Add(-time.Hour))
	state.Reservations = map[string]*v1.Reservation{
		"default/blocked-res": {
			ObjectMeta: v1.ObjectMeta{Name: "blocked-res", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:        v1.RunReference{Name: "blocked", Namespace: "default"},
				EarliestStart: past,
			},
			Status: v1.ReservationStatus{State: "Pending"},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	err := controller.ActivateReservations(now)
	if err == nil {
		t.Fatalf("expected an aggregate error naming the failed re-forecast")
	}
	res := state.Reservations["default/blocked-res"]
	if res.Status.State != "Failed" {
		t.Fatalf("expected reservation marked Failed, got %s (reason %s)", res.Status.State, res.Status.Reason)
	}
	for _, lease := range state.Leases {
		t.Errorf("unexpected lease %s created", lease.Name)
	}

	// The next tick must not retry the failed reservation.
	if err := controller.ActivateReservations(now.Add(time.Minute)); err != nil {
		t.Fatalf("failed reservation retried hot: %v", err)
	}
}
