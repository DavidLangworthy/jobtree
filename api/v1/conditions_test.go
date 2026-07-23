package v1

import (
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The whole point of R11 is that Phase and Conditions cannot disagree. A call site
// cannot break that — it names a RunState value, and SetRunState derives the phase
// from what it just wrote. The one place it COULD break is the table itself: a
// state whose declared Phase is not what its True set actually derives to. So the
// table checks itself, over every declared state, and a new state added without a
// matching derivation rule fails here rather than in production.
func TestEveryRunStateDerivesItsOwnPhase(t *testing.T) {
	if len(AllRunStates) == 0 {
		t.Fatal("AllRunStates is empty; this test would pass vacuously")
	}
	for _, state := range AllRunStates {
		var status RunStatus
		SetRunState(&status, 7, state, "because")
		if status.Phase != state.Phase {
			t.Errorf("state %s declares phase %q but its conditions derive %q",
				state.Reason, state.Phase, status.Phase)
		}
		if got := DeriveRunPhase(status.Conditions); got != status.Phase {
			t.Errorf("state %s: SetRunState wrote phase %q but DeriveRunPhase says %q",
				state.Reason, status.Phase, got)
		}
	}
}

// Every managed condition is present after any write, so a reader never has to
// distinguish "False" from "nobody has set this yet" — and the reason is on the
// conditions the state turned ON, which is what `kubectl get -o json` readers and
// `kubectl wait --for=condition=X` key on.
func TestSetRunStateManagesEveryConditionAndStampsTheReason(t *testing.T) {
	var status RunStatus
	SetRunState(&status, 3, RunStateGangBound, "adopted 4 GPUs")

	if len(status.Conditions) != len(runConditionTypes) {
		t.Fatalf("want all %d managed conditions written, got %d: %+v",
			len(runConditionTypes), len(status.Conditions), status.Conditions)
	}
	for _, condType := range runConditionTypes {
		cond := meta.FindStatusCondition(status.Conditions, condType)
		if cond == nil {
			t.Fatalf("condition %s missing", condType)
		}
		if cond.Reason == "" {
			t.Errorf("condition %s has an empty reason; the apiserver rejects that", condType)
		}
		if cond.ObservedGeneration != 3 {
			t.Errorf("condition %s observedGeneration = %d, want 3", condType, cond.ObservedGeneration)
		}
	}
	for _, want := range []string{RunConditionAdmitted, RunConditionScheduled, RunConditionRunning} {
		cond := meta.FindStatusCondition(status.Conditions, want)
		if cond.Status != metav1.ConditionTrue || cond.Reason != RunStateGangBound.Reason {
			t.Errorf("condition %s = %s/%s, want True/%s", want, cond.Status, cond.Reason, RunStateGangBound.Reason)
		}
		if cond.Message != "adopted 4 GPUs" {
			t.Errorf("condition %s message = %q, want the state's message", want, cond.Message)
		}
	}
	if cond := meta.FindStatusCondition(status.Conditions, RunConditionFailed); cond.Status != metav1.ConditionFalse {
		t.Errorf("Failed must be explicitly False on a running run, got %s", cond.Status)
	}
}

// The Unfunded/Unschedulable shape: nothing is True, so the answer to "why is my
// run not starting" can only live on a False condition. If it does not, the
// vocabulary is unreachable exactly where a researcher needs it.
func TestAStateWithNothingTrueStillCarriesItsReason(t *testing.T) {
	for _, state := range []RunState{RunStateUnfunded, RunStateUnschedulable} {
		var status RunStatus
		SetRunState(&status, 1, state, "no quota")
		cond := meta.FindStatusCondition(status.Conditions, RunConditionAdmitted)
		if cond.Status != metav1.ConditionFalse {
			t.Errorf("%s: Admitted = %s, want False", state.Reason, cond.Status)
		}
		if cond.Reason != state.Reason {
			t.Errorf("%s: Admitted reason = %q, want %q — the reason is unreachable otherwise",
				state.Reason, cond.Reason, state.Reason)
		}
		if other := meta.FindStatusCondition(status.Conditions, RunConditionScheduled); other.Reason != RunReasonNotApplicable {
			t.Errorf("%s: a condition this state has no opinion about must read %s, got %q",
				state.Reason, RunReasonNotApplicable, other.Reason)
		}
	}
}

// Blocked means two different things either side of admission, and collapsing them
// would report a run that still holds GPUs as though it were merely queued.
func TestBlockedBeforeAdmissionIsWaitingButAfterItIsPending(t *testing.T) {
	var waiting RunStatus
	SetRunState(&waiting, 1, RunStateFollowWait, "waiting on upstream")
	if waiting.Phase != RunPhaseWaiting {
		t.Errorf("blocked and NOT admitted must be Waiting, got %q", waiting.Phase)
	}

	var grace RunStatus
	SetRunState(&grace, 1, RunStateCheckpointGrace, "node failed; holding to the checkpoint deadline")
	if grace.Phase != RunPhasePending {
		t.Errorf("blocked WHILE admitted (checkpoint grace) must stay Pending — it still holds capacity — got %q", grace.Phase)
	}
	if !meta.IsStatusConditionTrue(grace.Conditions, RunConditionBlocked) {
		t.Error("checkpoint grace must still report Blocked; that is what says a deadline is running")
	}
}

// A transition flips the conditions rather than accumulating them, and
// lastTransitionTime only moves when the status actually changed (meta's
// contract — pinned here because the CLI and `kubectl wait` depend on it).
func TestRunStateTransitionsFlipConditionsRatherThanAccumulate(t *testing.T) {
	var status RunStatus
	SetRunState(&status, 1, RunStateGangBound, "running")
	firstRunning := meta.FindStatusCondition(status.Conditions, RunConditionRunning).LastTransitionTime

	SetRunState(&status, 1, RunStateGangBound, "still running")
	if got := meta.FindStatusCondition(status.Conditions, RunConditionRunning).LastTransitionTime; !got.Equal(&firstRunning) {
		t.Error("re-applying the same state must not move lastTransitionTime")
	}

	SetRunState(&status, 1, RunStateAllSucceeded, "done")
	if status.Phase != RunPhaseComplete {
		t.Fatalf("phase = %q, want Completed", status.Phase)
	}
	if meta.IsStatusConditionTrue(status.Conditions, RunConditionRunning) {
		t.Error("Running must go False when the run completes; a stale True is exactly the disagreement R11 removes")
	}
	if !meta.IsStatusConditionTrue(status.Conditions, RunConditionCompleted) {
		t.Error("Completed must be True")
	}
}

// Terminal failure wins over everything, so a run that fails while it held
// Running/Completed conditions still reports Failed.
func TestFailedWinsOverEveryOtherCondition(t *testing.T) {
	var status RunStatus
	SetRunState(&status, 1, RunStateGangBound, "running")
	SetRunState(&status, 1, RunStateWorkloadFailed, "rank 2 exited 1")
	if status.Phase != RunPhaseFailed {
		t.Errorf("phase = %q, want Failed", status.Phase)
	}
}

func TestLeaseConditionsMirrorTheClosureFact(t *testing.T) {
	open := GPULeaseStatus{}
	SetLeaseConditions(&open, 1)
	if !meta.IsStatusConditionTrue(open.Conditions, LeaseConditionActive) {
		t.Error("an unclosed lease must report Active=True; it is charging a budget and holding GPUs")
	}
	if meta.IsStatusConditionTrue(open.Conditions, LeaseConditionClosed) {
		t.Error("an unclosed lease must report Closed=False")
	}

	closed := GPULeaseStatus{Closed: true, ClosureReason: "RunDeleted"}
	SetLeaseConditions(&closed, 1)
	if meta.IsStatusConditionTrue(closed.Conditions, LeaseConditionActive) {
		t.Error("a closed lease must report Active=False")
	}
	cond := meta.FindStatusCondition(closed.Conditions, LeaseConditionClosed)
	if cond.Status != metav1.ConditionTrue || cond.Reason != "RunDeleted" {
		t.Errorf("Closed = %s/%s, want True/RunDeleted — the reason must be the ClosureReason verbatim", cond.Status, cond.Reason)
	}
}

func TestReservationConditionsDeriveFromForecastAndActivation(t *testing.T) {
	var unforecast ReservationStatus
	SetReservationConditions(&unforecast, 1)
	if meta.IsStatusConditionTrue(unforecast.Conditions, ReservationConditionForecast) {
		t.Error("no forecast means Forecast=False")
	}

	now := metav1.Now()
	activated := ReservationStatus{Forecast: &ReservationForecast{DeficitGPUs: 4}, ActivatedAt: &now}
	SetReservationConditions(&activated, 1)
	if !meta.IsStatusConditionTrue(activated.Conditions, ReservationConditionForecast) {
		t.Error("a forecast present means Forecast=True")
	}
	if !meta.IsStatusConditionTrue(activated.Conditions, ReservationConditionActivated) {
		t.Error("ActivatedAt set means Activated=True")
	}

	released := ReservationStatus{ActivatedAt: &now, ReleasedAt: &now}
	SetReservationConditions(&released, 1)
	cond := meta.FindStatusCondition(released.Conditions, ReservationConditionActivated)
	if cond.Status != metav1.ConditionFalse || cond.Reason != ReservationReasonReleased {
		t.Errorf("a released reservation = %s/%s, want False/Released", cond.Status, cond.Reason)
	}
}

func TestBudgetIsUnhealthyExactlyWhenAnEnvelopeIsOvercommitted(t *testing.T) {
	healthy := BudgetStatus{Headroom: []EnvelopeHeadroom{{Name: "west", Concurrency: 4}}}
	SetBudgetConditions(&healthy, 1)
	if !meta.IsStatusConditionTrue(healthy.Conditions, BudgetConditionHealthy) {
		t.Error("non-negative headroom everywhere must be Healthy=True")
	}

	over := BudgetStatus{Headroom: []EnvelopeHeadroom{
		{Name: "west", Concurrency: 4},
		{Name: "east", Concurrency: -2},
	}}
	SetBudgetConditions(&over, 1)
	cond := meta.FindStatusCondition(over.Conditions, BudgetConditionHealthy)
	if cond.Status != metav1.ConditionFalse || cond.Reason != BudgetReasonOvercommitted {
		t.Fatalf("Healthy = %s/%s, want False/Overcommitted", cond.Status, cond.Reason)
	}
	if want := "overcommitted envelopes: east"; cond.Message != want {
		t.Errorf("message = %q, want %q — naming the envelope is the point", cond.Message, want)
	}
}

// ClosureReason LOOKED like a closed vocabulary and is not: the oversubscription
// resolver stamps the attested lottery seed into it. The apiserver rejects a
// reason that does not match ^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$ — and it
// rejects the WHOLE status update, so from a controller that is an infinite
// retry loop, not a cosmetic defect. It wedged the swap path's lease writes (and
// with them the run's own status) until this sanitiser landed.
func TestASeededClosureReasonIsSanitisedButNotLost(t *testing.T) {
	closed := GPULeaseStatus{Closed: true, ClosureReason: "ReclaimUnfunded(0x2ab536d36c965726)"}
	SetLeaseConditions(&closed, 1)

	cond := meta.FindStatusCondition(closed.Conditions, LeaseConditionClosed)
	if cond.Reason != "ReclaimUnfunded" {
		t.Errorf("reason = %q, want the identifier prefix %q", cond.Reason, "ReclaimUnfunded")
	}
	if !apiserverReasonRE.MatchString(cond.Reason) {
		t.Errorf("reason %q would be REJECTED by the apiserver, wedging every status write", cond.Reason)
	}
	if !strings.Contains(cond.Message, "0x2ab536d36c965726") {
		t.Errorf("the verbatim closure reason must survive in the message (the seed is the audit trail), got %q", cond.Message)
	}
}

// Every reason this package can emit has to satisfy the apiserver, including the
// ones nobody thought about. A unit test is the only cheap place to check the
// whole vocabulary at once.
func TestEveryEmittableReasonSatisfiesTheAPIServer(t *testing.T) {
	seen := map[string]bool{}
	for _, state := range AllRunStates {
		var status RunStatus
		SetRunState(&status, 1, state, "m")
		for _, cond := range status.Conditions {
			seen[cond.Reason] = true
		}
	}
	for _, raw := range []string{
		"", "Completed", "RunDeleted", "NodeFailure", "SwapDeclined", "WorkloadFailed",
		"ReclaimUnfunded(0x2ab536d36c965726)", "Orphaned", "-leading-dash", "()",
	} {
		st := GPULeaseStatus{Closed: true, ClosureReason: raw}
		SetLeaseConditions(&st, 1)
		for _, cond := range st.Conditions {
			seen[cond.Reason] = true
		}
	}
	var res ReservationStatus
	SetReservationConditions(&res, 1)
	for _, cond := range res.Conditions {
		seen[cond.Reason] = true
	}
	var b BudgetStatus
	SetBudgetConditions(&b, 1)
	for _, cond := range b.Conditions {
		seen[cond.Reason] = true
	}

	if len(seen) < 5 {
		t.Fatalf("only %d reasons collected; this test would be near-vacuous", len(seen))
	}
	for reason := range seen {
		if !apiserverReasonRE.MatchString(reason) {
			t.Errorf("reason %q does not match the apiserver's pattern; the whole status update would be rejected", reason)
		}
	}
}

// The apiserver's own pattern for metav1.Condition.Reason, copied verbatim from
// the generated CRD schema. If upstream ever loosens it, this test is where the
// copy is corrected — deliberately, rather than by discovering a wedge in prod.
var apiserverReasonRE = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)
