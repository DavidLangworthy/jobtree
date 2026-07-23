package controllers

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
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

// R7 (sharpened by quota-semantics.md): a reservation blocked only by budget
// headroom (physical capacity is plentiful) must not trigger any preemption
// at activation. Budget shortfall now manifests as opportunistic
// classification, not a lottery over funded runs: the promise-made run
// starts opportunistically (unfunded) on the free capacity and is re-funded
// by arithmetic when quota returns. No funded work is cut.
func TestActivateReservationBudgetOnlyShortfallAdmitsOpportunistically(t *testing.T) {
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
	// SETUP: the scheduler plugin has already scheduled and funded hog and
	// bystander — Reconcile no longer mints on the admission path, so seed the
	// bound/Running state the old in-controller commit used to reach.
	seedRunning(t, state, "default/hog", now)
	seedRunning(t, state, "default/bystander", now)
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

	// No funded work was cut (R7 upheld): nothing closed, no resolver actions.
	for _, lease := range state.Leases {
		if lease.Status.Closed {
			t.Errorf("lease %s was closed (%s): budget shortfall must not preempt", lease.Name, lease.Status.ClosureReason)
		}
	}
	if actions := metrics.Snapshot().ResolverActions; len(actions) != 0 {
		t.Errorf("expected no resolver actions, got %v", actions)
	}

	// R3: the controller is no longer the committer, so it mints NOTHING on the
	// promise path. It pre-authorizes the start by emitting a promised intent
	// gang (lease-reason=Promise) carrying the payer provenance the activation
	// attributed the exhausted demand to; the jobtree scheduler binds and mints
	// it (those pods skip its funding gate). So the open-lease count is
	// unchanged, and blocked stays promised (Pending) until its leases land.
	if got := openLeaseCount(state); got != leasesBefore {
		t.Errorf("controller must mint no leases on the promise path: had %d open, now %d", leasesBefore, got)
	}
	blocked := state.Runs["default/blocked"]
	if blocked.Status.Phase == RunPhaseRunning {
		t.Errorf("expected blocked promised (not yet Running): the plugin binds its pods, got %s", blocked.Status.Phase)
	}
	if promise := promisePodsWithPayer(state, "default", "blocked", "org:team", "team", "west"); promise != 4 {
		t.Errorf("expected 4 promise pods for blocked with payer org:team/team/west, got %d", promise)
	}
	if all := intentPodsByRole(state, "default", "blocked", binder.RoleActive); all != 4 {
		t.Errorf("expected 4 Active intent pods for blocked, got %d", all)
	}
	// The reservation activated (Released), not rescheduled.
	for _, res := range state.Reservations {
		if res.Status.State == "Pending" {
			t.Errorf("expected reservation activated, found still-Pending %s", res.Name)
		}
	}

	// The promise is idempotent: reconciling blocked while its pods await the
	// scheduler must NOT plan a second reservation — its cover is expected to
	// fail until quota returns, so the Promise guard short-circuits admission.
	if err := controller.Reconcile("default", "blocked"); err != nil {
		t.Fatalf("blocked promise-wait reconcile failed: %v", err)
	}
	if len(state.Reservations) != 1 {
		t.Fatalf("promised blocked must not re-reserve: expected 1 reservation, got %d", len(state.Reservations))
	}
	if blocked.Status.Phase == RunPhaseRunning {
		t.Errorf("blocked must stay promised until its leases land, got %s", blocked.Status.Phase)
	}
	if got := promisePodsWithPayer(state, "default", "blocked", "org:team", "team", "west"); got != 4 {
		t.Errorf("promise gang must be idempotent, got %d pods after re-reconcile", got)
	}

	// The jobtree scheduler binds and mints the promised gang. Now the adoption
	// path flips blocked Running, exactly as for any other activation.
	if minted := seedPromiseLeases(t, state, "blocked", activation); minted != 4 {
		t.Fatalf("expected the plugin to mint 4 promise leases, got %d", minted)
	}
	if err := controller.Reconcile("default", "blocked"); err != nil {
		t.Fatalf("blocked adoption reconcile failed: %v", err)
	}
	if blocked.Status.Phase != RunPhaseRunning {
		t.Errorf("expected blocked adopted Running once its leases landed, got %s", blocked.Status.Phase)
	}
	if got := openLeaseCount(state); got != leasesBefore+4 {
		t.Errorf("expected 4 promise leases open (one per pod), had %d, now %d", leasesBefore, got)
	}
	// Its width is unfunded: org:team's envelope is exhausted by hog, so the run
	// coasts until quota returns.
	if blocked.Status.Funding == nil || blocked.Status.Funding.UnfundedGPUs != 4 || blocked.Status.Funding.OwnedGPUs != 0 {
		t.Errorf("expected blocked classed 4 unfunded / 0 owned, got %+v", blocked.Status.Funding)
	}
	// The reservation activated, not rescheduled: no Pending reservation for a
	// now-Running run (invariant 8).
	assertInvariantNoPendingReservationForRunningRun(t, state)

	// R14 demote-not-kill, the other half of the promise: when quota returns
	// (hog completes, freeing org:team's envelope), blocked is re-funded by
	// arithmetic — no new mint, the same open leases just re-class Owned.
	later := activation.Add(time.Hour)
	closed := v1.NewTime(later)
	for i := range state.Leases {
		l := &state.Leases[i]
		if l.Labels[binder.LabelRunName] == "hog" && !l.Status.Closed {
			l.Status.Closed = true
			l.Status.Ended = &closed
			l.Status.ClosureReason = "Completed"
		}
	}
	controller.Clock = runClock{now: later}
	// Measured after hog freed the envelope (its lease closed) but before blocked
	// reconciles: re-funding must move nothing, only re-class blocked's existing
	// open leases Unfunded → Owned.
	openBeforeRefund := openLeaseCount(state)
	if err := controller.Reconcile("default", "blocked"); err != nil {
		t.Fatalf("blocked re-funding reconcile failed: %v", err)
	}
	if got := openLeaseCount(state); got != openBeforeRefund {
		t.Errorf("re-funding must mint no new leases (arithmetic within the recorded envelope): had %d open, now %d", openBeforeRefund, got)
	}
	if blocked.Status.Funding == nil || blocked.Status.Funding.OwnedGPUs != 4 || blocked.Status.Funding.UnfundedGPUs != 0 {
		t.Errorf("expected blocked re-funded 4 owned / 0 unfunded once hog freed the envelope, got %+v", blocked.Status.Funding)
	}
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
	// SETUP: victim is already bound/Running (holding the whole node) via the
	// plugin's mint — Reconcile no longer binds on admission. Seed it so the
	// capacity-deficit reservation and preemption below have work to reclaim.
	seedRunning(t, state, "default/victim", now)

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
	// Funded activation preempts the victim then emits the waiter's intent pods
	// (Pending); the plugin mints — stood in for by seedRunning — to reach Running.
	seedRunning(t, state, "default/waiter", activation)

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
	// The runnable reservation activates (funded) despite the orphan's failure,
	// emitting intent pods; the plugin mints — stood in for by seedRunning.
	seedRunning(t, state, "default/runnable", now)

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
	// SETUP: hog is already bound/Running holding the whole node via the plugin's
	// mint — Reconcile no longer binds on admission.
	seedRunning(t, state, "default/hog", now)

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

	// The scheduler plugin now schedules waiter's freed-capacity slot and mints
	// its leases; flipping the run Running is the controller's adoption job, and
	// the bridge's apply is not atomic (R28), so model the pre-adoption state:
	// waiter's leases exist but the run is still Pending.
	bindTime := now.Add(20 * time.Minute)
	controller.Clock = runClock{now: bindTime}
	seedRunning(t, state, "default/waiter", bindTime)
	waiter.Status.Phase = RunPhasePending

	// The next reconcile adopts those leases: it flips waiter Running and, as
	// part of adoption, releases the now-superseded pending reservation.
	if err := controller.Reconcile("default", "waiter"); err != nil {
		t.Fatalf("waiter adoption reconcile failed: %v", err)
	}
	if waiter.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected waiter running after adoption, got %s", waiter.Status.Phase)
	}
	if waiter.Status.PendingReservation != nil || waiter.Status.EarliestStart != nil {
		t.Errorf("expected reservation pointers cleared on adoption, got %v / %v",
			waiter.Status.PendingReservation, waiter.Status.EarliestStart)
	}
	for key, res := range state.Reservations {
		if res.Status.State == "Pending" {
			t.Errorf("expected no pending reservations after adoption, %s is %s", key, res.Status.State)
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
	// SETUP: runner is already bound/Running via the plugin's mint — Reconcile no
	// longer binds on admission. Seed the bound state so the activation guard
	// below has a Running run whose stale reservation it must release.
	seedRunning(t, state, "default/runner", now)
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

// Review finding (PR #14 item 1): a promoted spare must keep its funding
// provenance — a sponsor-funded spare that swaps in must stay on the
// sponsor's books. Since R15 the class is derived from the payer facts
// (Owner + PaidBy*), so the swap lease carries those forward; the role is
// just Active (roles are Active|Spare only).
func TestSwapLeaseKeepsFundingProvenance(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := h100Run("train", "org:team", 2)
	run.Status.Phase = RunPhaseRunning
	active := v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "train-active", Labels: map[string]string{
			binder.LabelRunName:    "train",
			binder.LabelGroupIndex: "0",
			binder.LabelRunRole:    binder.RoleActive,
		}},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:team",
			RunRef:         v1.RunReference{Namespace: "default", Name: "train"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-a#0", "node-a#1"}, Role: binder.RoleActive},
			Interval:       v1.GPULeaseInterval{Start: v1.NewTime(now.Add(-time.Hour))},
			PaidByBudget:   "team",
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
	spare := v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "train-spare", Labels: map[string]string{
			binder.LabelRunName:    "train",
			binder.LabelGroupIndex: "0",
			binder.LabelRunRole:    binder.RoleSpare,
		}},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:sponsor",
			RunRef:         v1.RunReference{Namespace: "default", Name: "train"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-b#0", "node-b#1"}, Role: binder.RoleSpare},
			Interval:       v1.GPULeaseInterval{Start: v1.NewTime(now.Add(-time.Hour))},
			PaidByBudget:   "sponsor",
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
	state := &ClusterState{
		Nodes:  []topology.SourceNode{h100Node("node-a", 2), h100Node("node-b", 2)},
		Runs:   map[string]*v1.Run{"default/train": run},
		Leases: []v1.GPULease{active, spare},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("node failure handling failed: %v", err)
	}
	// The controller emits a swap pod carrying the spare's funding provenance;
	// the plugin mints the Swap lease from it — stood in for by seedSwapLease.
	// The provenance is what must survive, so this is exactly what the test checks.
	seedSwapLease(t, state, "train", now)

	var swap *v1.GPULease
	for i := range state.Leases {
		if state.Leases[i].Spec.Reason == "Swap" && !state.Leases[i].Status.Closed {
			swap = &state.Leases[i]
		}
	}
	if swap == nil {
		t.Fatalf("expected a swap lease")
	}
	if swap.Spec.Owner != "org:sponsor" {
		t.Errorf("swap lease lost its payer: owner %q, want org:sponsor", swap.Spec.Owner)
	}
	// The payer facts are the provenance now: the derivation reads them to
	// keep classing this width against the sponsor (borrowed), so
	// MaxBorrowGPUs still counts it. The role is Active.
	if swap.Spec.Slice.Role != binder.RoleActive {
		t.Errorf("swap lease role %q, want Active (class is derived from the payer, not the role)", swap.Spec.Slice.Role)
	}
	if swap.Spec.PaidByBudget != "sponsor" || swap.Spec.PaidByEnvelope != "west" {
		t.Errorf("swap lease lost funding attribution: %s/%s", swap.Spec.PaidByBudget, swap.Spec.PaidByEnvelope)
	}
}
