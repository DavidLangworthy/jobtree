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
				}, Start: &testWindowStart, End: &testWindowEnd,
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

// MERGE-127.md pre-merge item 1: a namespace with no derived funding principal
// leaves its due reservation BLOCKED AND VISIBLE, not terminally failed.
//
// The history matters, because this test has now been wrong in both directions.
// A guard shipped 2026-07-24 refused the tick and returned nil; the 2026-07-25
// panel ran the code and found the reservation sitting Pending at tick 20 with
// the backlog gauge FROZEN at 1020, because metrics.ClearReservationBacklog was
// reached only from terminal paths — so terminating replaced it. MERGE-127.md
// then established that terminating was itself the wrong answer: the transition
// is FOREIGN-INDUCIBLE (a Budget in any namespace naming this namespace's owner
// flips OwnerOf to "" and destroys reservations in a namespace whose own contents
// never changed), and it contradicts quota-semantics.md:27 and :128, which
// outrank R7-tenancy-amendment.md:126-127 under AGENTS.md:176.
//
// What the panel actually caught was INVISIBILITY, not waiting. So the state that
// is correct here is honest rather than terminal, and this test asserts all four
// halves of "honest": the state, a durable cause, a durable onset, and — the one
// that mattered — the gauge ABSENT rather than frozen.
func TestActivateReservationBlocksWhenNamespaceHasNoOwner(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := conflictedNamespaceState(now)

	// Seed the gauge so its clearing is OBSERVABLE. Without this the assertion
	// below is vacuous — see its comment.
	metrics.SetReservationBacklog("default/res", "H100-80GB", 1020)

	// Blocking is a steady state, not a per-tick failure. Returning an error here
	// would put "namespace has no funding principal" into the controller's error
	// log on every single pass for as long as the binding stays broken.
	if err := NewRunController(state, runClock{now: now}).ActivateReservations(now); err != nil {
		t.Fatalf("blocking is an expected steady state, not an error to report every tick: %v", err)
	}

	res := state.Reservations["default/res"]
	if res.Status.State != "BlockedFunding" {
		t.Fatalf("reservation must be held in BlockedFunding, got state %q", res.Status.State)
	}
	// DURABLE CAUSE. Pending-with-no-reason was the invisibility; a state whose
	// reason does not name the binding is the same defect one rename later.
	if !strings.Contains(res.Status.Reason, "no funding principal") {
		t.Errorf("the blocked reservation must carry the real cause, got %q", res.Status.Reason)
	}
	if strings.Contains(res.Status.Reason, "owner and flavor must be set") {
		t.Errorf("the operator must not be told about fields the Run does not have, got: %q", res.Status.Reason)
	}
	// DURABLE ONSET. Without it, "how long has this been blocked" is unanswerable
	// and the state is as opaque as the Pending it replaced.
	if res.Status.BlockedSince == nil {
		t.Fatalf("a blocked reservation with no onset cannot be aged; BlockedSince must be stamped")
	}
	if !res.Status.BlockedSince.Time.Equal(now) {
		t.Errorf("BlockedSince = %v, want the tick that blocked it (%v)", res.Status.BlockedSince.Time, now)
	}
	if res.Status.CountdownSeconds != nil {
		t.Errorf("no activation is forecast while the namespace has no principal, so a countdown "+
			"would be the old lie in a new state; got %v", *res.Status.CountdownSeconds)
	}
	// THE GAUGE, ABSENT. This is the half of the 2026-07-24 defect that hid the
	// other half — the frozen {H100-80GB 1020} series — and the assertion was
	// VACUOUS until the fix-diff panel mutated it: the fixture's EarliestStart is
	// in the past, so refreshReservationBacklog never runs and the gauge was map[]
	// both before and after. Deleting ClearReservationBacklog entirely left
	// ./controllers green. It is seeded above now, so "cleared" is a real
	// transition instead of a coincidence.
	if backlog := metrics.Snapshot().ReservationBacklog; len(backlog) != 0 {
		t.Errorf("no activation forecast exists while the namespace has no principal, so the backlog "+
			"gauge must be CLEARED, not frozen; still reporting %v", backlog)
	}

	run := state.Runs["default/train"]
	if !strings.Contains(run.Status.Message, "no funding principal") {
		t.Errorf("the run must say why it stopped, got %q", run.Status.Message)
	}
	if len(state.Leases) != 0 {
		t.Errorf("nothing may be minted for a namespace that cannot pay, got %d leases", len(state.Leases))
	}
}

// The onset must survive re-blocking. The activation gate re-considers
// BlockedFunding every pass — that is what makes recovery automatic — so
// blockReservationOnFunding runs again on every tick the binding stays broken. If
// it restamped, blocked-age would read zero forever and the durable onset would
// be the frozen gauge wearing a timestamp: a field that always says "just now"
// tells an operator exactly as much as a field that says nothing.
func TestBlockedReservationOnsetSurvivesReBlocking(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := conflictedNamespaceState(now)

	if err := NewRunController(state, runClock{now: now}).ActivateReservations(now); err != nil {
		t.Fatalf("unexpected error on the first block: %v", err)
	}
	first := state.Reservations["default/res"].Status.BlockedSince
	if first == nil {
		t.Fatalf("fixture is wrong: expected the first pass to stamp an onset")
	}
	onset := first.Time

	// Twelve more passes, the binding still broken. Each one re-enters
	// activateReservation and calls blockReservationOnFunding again.
	for h := 1; h <= 12; h++ {
		at := now.Add(time.Duration(h) * time.Hour)
		if err := NewRunController(state, runClock{now: at}).ActivateReservations(at); err != nil {
			t.Fatalf("tick %d: unexpected error while re-blocking: %v", h, err)
		}
		res := state.Reservations["default/res"]
		if res.Status.State != "BlockedFunding" {
			t.Fatalf("tick %d: reservation left BlockedFunding without the binding being repaired: %q",
				h, res.Status.State)
		}
		if res.Status.BlockedSince == nil || !res.Status.BlockedSince.Time.Equal(onset) {
			t.Fatalf("tick %d: onset was RESTAMPED to %v, want the original %v — blocked-age would "+
				"read zero forever", h, res.Status.BlockedSince, onset)
		}
	}

	// Twelve hours of blockage is now readable off the object. That is the whole
	// point of the field.
	age := now.Add(12 * time.Hour).Sub(state.Reservations["default/res"].Status.BlockedSince.Time)
	if age != 12*time.Hour {
		t.Errorf("blocked age = %v, want 12h", age)
	}
}

// A blocked reservation must not outlive the run's need for it. Every caller of
// releasePendingReservations has just established the run no longer needs its
// reservation — it adopted a full-width gang, or it reached a terminal phase —
// and BlockedFunding is now the one state that can still be sitting there, since
// the activation gate re-considers it every pass.
//
// Reproduced before the filter was widened: block, fail the run, tick five times,
// and the reservation stayed BlockedFunding with releasedAt nil and a stale "no
// funding principal" reason next to a Failed run, forever. Under the terminal
// path substitution 1 replaces it would have been Failed — so making the
// reservation non-terminal without widening this filter trades one immortal
// reservation for another, which is the exact defect class the substitution
// exists to remove.
func TestBlockedReservationIsReleasedWhenItsRunNoLongerNeedsIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		advance func(t *testing.T, state *ClusterState, now time.Time)
	}{{
		name: "terminal run",
		advance: func(t *testing.T, state *ClusterState, now time.Time) {
			run := state.Runs["default/train"]
			NewRunController(state, runClock{now: now}).failRun(run, v1.RunStateEndedByResolver, "killed by the resolver")
		},
	}, {
		name: "run bound its gang independently",
		advance: func(t *testing.T, state *ClusterState, now time.Time) {
			// The admin repairs the binding and the plugin binds the gang before
			// the next activation pass — a normal race, not an exotic one.
			state.Budgets = []v1.Budget{h100Budget("team", "org:team", 16)}
			seedRunning(t, state, "default/train", now)
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			metrics.Reset()
			t.Cleanup(metrics.Reset)
			now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			state := conflictedNamespaceState(now)

			if err := NewRunController(state, runClock{now: now}).ActivateReservations(now); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := state.Reservations["default/res"].Status.State; got != "BlockedFunding" {
				t.Fatalf("fixture is wrong: expected a blocked reservation, got %q", got)
			}

			tc.advance(t, state, now)

			for h := 1; h <= 5; h++ {
				at := now.Add(time.Duration(h) * time.Hour)
				if err := NewRunController(state, runClock{now: at}).ActivateReservations(at); err != nil {
					t.Fatalf("tick %d: unexpected error: %v", h, err)
				}
			}

			res := state.Reservations["default/res"]
			if res.Status.State != "Released" {
				t.Fatalf("a blocked reservation whose run no longer needs it must be released, got %q "+
					"— it is immortal otherwise, which is what the blocked state was introduced to fix",
					res.Status.State)
			}
			if res.Status.ReleasedAt == nil {
				t.Errorf("a released reservation must carry the instant it was released")
			}
			if res.Status.CountdownSeconds != nil {
				t.Errorf("a released reservation must not keep counting down, got %v", *res.Status.CountdownSeconds)
			}
			assertInvariantNoPendingReservationForRunningRun(t, state)
		})
	}
}

// MERGE-127.md pre-merge item 3. The message on the no-funding-principal path
// used to end "...and resubmit", instructing a human to do the one thing
// quota-semantics.md:38-39 promises they never have to ("Recovery is automatic
// ... Nothing to resubmit, nothing to approve") — in a TENANT-FACING string,
// while the comment thirty lines above the function claimed recovery was
// autonomous. It is a ratified-text violation, not a wording preference.
//
// It is checked on both surfaces a human actually reads: the Run's status message
// and the Reservation's reason, which now carry the same text.
func TestNoFundingPrincipalMessageNeverTellsAnyoneToResubmit(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := conflictedNamespaceState(now)

	if err := NewRunController(state, runClock{now: now}).ActivateReservations(now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, surface := range []struct{ what, msg string }{
		{"the run's status message", state.Runs["default/train"].Status.Message},
		{"the reservation's reason", state.Reservations["default/res"].Status.Reason},
	} {
		if surface.msg == "" {
			t.Fatalf("fixture is wrong: %s is empty, so the assertion below proves nothing", surface.what)
		}
		// Sanity: the string really is the no-funding-principal one, so a green
		// result cannot come from having reached some other path entirely.
		if !strings.Contains(surface.msg, "no funding principal") {
			t.Fatalf("%s is not the no-funding-principal message: %q", surface.what, surface.msg)
		}
		if strings.Contains(strings.ToLower(surface.msg), "resubmit") {
			t.Errorf("%s tells a human to resubmit, which quota-semantics.md:38-39 promises they never "+
				"have to: %q", surface.what, surface.msg)
		}
	}
}

// Recovery is automatic (quota-semantics.md:38-39) — the claim that makes holding
// the reservation legitimate instead of merely inert. If it were false, blocking
// would be a slower way of destroying the work and the terminal path would have
// been the honest one.
func TestRunRecoversFromBlockedFundingWhenBindingIsRepaired(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := conflictedNamespaceState(now)

	if err := NewRunController(state, runClock{now: now}).ActivateReservations(now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := state.Reservations["default/res"].Status.State; got != "BlockedFunding" {
		t.Fatalf("fixture is wrong: expected a blocked reservation, got %q", got)
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

	// AND THE RESERVATION DOES NOT SIT BLOCKED FOREVER. This is the assertion the
	// blocked state creates the need for: unlike the terminal path it replaces,
	// BlockedFunding is a state a reservation could plausibly never leave, and a
	// run that recovers while its reservation stays blocked is a half-recovery
	// nothing would have noticed. The gauge must be gone with it — a stale backlog
	// series for a reservation that is no longer blocked is the frozen-{H100-80GB
	// 1020} defect all over again, pointing the other way.
	res := state.Reservations["default/res"]
	if res.Status.State == "BlockedFunding" {
		t.Fatalf("the run recovered but its reservation is still BlockedFunding since %v: "+
			"recovery has to reach the reservation too, or the block is permanent for it",
			res.Status.BlockedSince)
	}
	if backlog := metrics.Snapshot().ReservationBacklog; len(backlog) != 0 {
		t.Errorf("a recovered reservation must leave no backlog series behind, still reporting %v", backlog)
	}
	t.Logf("after repair: phase=%s pods=%d reservation=%s msg=%q",
		run.Status.Phase, activeIntentPods(state, "default", "train"), res.Status.State, run.Status.Message)
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
				}, Start: &testWindowStart, End: &testWindowEnd,
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

	// Seed the gauge so its clearing is OBSERVABLE. Without this the assertion
	// below is vacuous — see its comment.
	metrics.SetReservationBacklog("default/res", "H100-80GB", 1020)

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
	// Seeded above, for the same reason: without a non-empty starting gauge this
	// assertion passes even if ClearReservationBacklog is deleted outright.
	if backlog := metrics.Snapshot().ReservationBacklog; len(backlog) != 0 {
		t.Errorf("failReservationNoEnvelope must clear the backlog gauge, still reporting %v", backlog)
	}
}
