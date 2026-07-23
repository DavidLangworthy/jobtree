package v1

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// R11 — the status-condition vocabulary.
//
// Status used to be a free-form phase string plus a free-form message, with the
// canonical phase values defined in the *controllers* package and unenumerated in
// the CRD schema. Nothing could wait on a jobtree object: `kubectl wait
// --for=condition=...` had nothing to key on, and every consumer string-matched a
// phase whose spelling lived somewhere else.
//
// Two things are load-bearing here, and both are about making disagreement
// impossible rather than merely unlikely:
//
//  1. **A call site names a STATE, not a phase.** A RunState is a value in this
//     package carrying the reason, the conditions it turns on, and the phase they
//     imply. There is no lookup that can miss and no string that can be misspelled
//     — an unknown state is not representable.
//
//  2. **Phase is computed from the conditions, never written beside them.**
//     SetRunState applies the conditions and then sets Phase = DeriveRunPhase(...).
//     So "the phase and the conditions disagree" is not a bug that can be
//     introduced at a call site; it could only be a bug in the table, which
//     TestEveryRunStateDerivesItsOwnPhase checks exhaustively.

// Run phases. These live in the API package because the conditions below derive
// them and the CRD enumerates them; the controllers package re-exports the names
// it has always used.
const (
	RunPhasePending  = "Pending"
	RunPhaseRunning  = "Running"
	RunPhaseFailed   = "Failed"
	RunPhaseComplete = "Completed"
	// RunPhaseWaiting means the run is blocked on its follow dependencies and
	// has not entered admission (distinct from Pending, which is admitted but
	// short on capacity).
	RunPhaseWaiting = "Waiting"
)

// Run condition types. Every one of these is managed on every status write, so a
// reader never has to distinguish "False" from "nobody set it".
const (
	// RunConditionAdmitted — the engine has accepted the run and is working on
	// it (it has a plan, a reservation, or emitted pods). False means the run is
	// not yet the engine's problem: blocked upstream, unschedulable, or unfunded.
	RunConditionAdmitted = "Admitted"
	// RunConditionScheduled — the full gang holds leases on real nodes.
	RunConditionScheduled = "Scheduled"
	// RunConditionRunning — the workload is running at (at least) its minimum
	// runnable width.
	RunConditionRunning = "Running"
	// RunConditionCompleted — terminal success.
	RunConditionCompleted = "Completed"
	// RunConditionFailed — terminal failure.
	RunConditionFailed = "Failed"
	// RunConditionBlocked — progress is gated on something outside this run: an
	// upstream in the follow forest, or a checkpoint grace window. Blocked BEFORE
	// admission is what makes a run Waiting; blocked after it is a Pending run
	// holding capacity while a deadline runs down.
	RunConditionBlocked = "Blocked"
)

// runConditionTypes is every type SetRunState manages, in the order it writes
// them. Keeping the list here (rather than deriving it from the states) is what
// lets a state say only what is TRUE and still leave the rest explicitly False.
var runConditionTypes = []string{
	RunConditionAdmitted,
	RunConditionScheduled,
	RunConditionRunning,
	RunConditionCompleted,
	RunConditionFailed,
	RunConditionBlocked,
}

// RunReasonNotApplicable is the Reason stamped on a condition this state turns
// off and has nothing to say about. metav1.Condition requires a non-empty reason
// even for False, and inventing a per-condition negative vocabulary would double
// the table for no reader benefit. A state that DOES have something to say about
// a False condition names it in whenFalse instead — see RunStateUnfunded.
//
// Exported so a reader can tell "False, and here is why" from "False, and this
// state has no opinion" without matching a magic string of its own.
const RunReasonNotApplicable = "NotApplicable"

// RunState is one entry in the closed vocabulary of run states: the reason a run
// is where it is, the conditions that are True there, and the phase that follows.
//
// It is a VALUE, not a key into a map, on purpose. A call site cannot name a state
// that does not exist, so there is no "unknown reason" branch to get wrong.
type RunState struct {
	// Reason is the condition Reason stamped on every condition this state turns
	// on. It is the stable, machine-readable half of "why".
	Reason string
	// Phase is what DeriveRunPhase must return for this state's conditions. It is
	// declared, rather than only computed, so the table can be checked against the
	// derivation instead of defining it.
	Phase string
	// whenTrue lists the condition types this state turns on; every other managed
	// type is set False. Unexported on purpose: it is vocabulary internals, and
	// every state that uses it is declared in this file.
	whenTrue []string
	// whenFalse lists condition types this state turns OFF but still stamps its
	// reason on. A run that is Pending because it is Unfunded has NOTHING true —
	// and "why is it not admitted" is precisely the question worth answering, so
	// the reason has to ride on a False condition or it is unreachable. This is
	// standard: `Available=False, Reason=MinimumReplicasUnavailable` is the same
	// shape.
	whenFalse []string
}

// The states. Grouped by the phase they imply.
var (
	// --- Waiting: blocked before admission -----------------------------------

	// RunStateFollowWait — parked on an unfinished (or failed, inside its grace)
	// upstream in the follow forest.
	RunStateFollowWait = RunState{Reason: "FollowWait", Phase: RunPhaseWaiting, whenTrue: []string{RunConditionBlocked}}

	// --- Pending: admitted or admissible, but not running --------------------

	// RunStateUnschedulable — no placement exists for the request right now
	// (topology, flavor, or an invalid request the packer rejects).
	RunStateUnschedulable = RunState{Reason: "Unschedulable", Phase: RunPhasePending, whenFalse: []string{RunConditionAdmitted}}
	// RunStateUnfunded — quota, not capacity: the cover step could not fund the
	// width. This is the answer to "why is my run not starting" that a researcher
	// most often needs, and it is deliberately distinct from Unschedulable.
	RunStateUnfunded = RunState{Reason: "Unfunded", Phase: RunPhasePending, whenFalse: []string{RunConditionAdmitted}}
	// RunStateReserved — parked behind a Reservation with a forecast start.
	RunStateReserved = RunState{Reason: "Reserved", Phase: RunPhasePending, whenTrue: []string{RunConditionAdmitted}}
	// RunStateGangForming — pods are out and some leases are held, but not the
	// whole width. "Start together or not at all": a partial gang is NOT Running.
	RunStateGangForming = RunState{Reason: "GangForming", Phase: RunPhasePending, whenTrue: []string{RunConditionAdmitted}}
	// RunStateScheduling — the full intent gang is emitted and awaiting the
	// jobtree scheduler; no lease has been minted yet.
	RunStateScheduling = RunState{Reason: "Scheduling", Phase: RunPhasePending, whenTrue: []string{RunConditionAdmitted}}
	// RunStatePromised — an opportunistic (R3 Promise) start: pods are out,
	// pre-authorized, and expected to stay unfunded until quota returns.
	RunStatePromised = RunState{Reason: "Promised", Phase: RunPhasePending, whenTrue: []string{RunConditionAdmitted}}
	// RunStateCheckpointGrace — capacity was lost and the run is inside its
	// checkpoint grace window. Admitted AND Blocked: it still holds capacity, and
	// a deadline is running against it.
	RunStateCheckpointGrace = RunState{Reason: "CheckpointGrace", Phase: RunPhasePending, whenTrue: []string{RunConditionAdmitted, RunConditionBlocked}}
	// RunStateReclaimed — a funded admission took this run's opportunistic
	// capacity back; it will re-admit when quota allows.
	RunStateReclaimed = RunState{Reason: "Reclaimed", Phase: RunPhasePending, whenTrue: []string{RunConditionAdmitted}}
	// RunStateCapacityLost — a rank was lost (eviction or node failure) and the
	// run dropped below its minimum runnable width; it is re-assembling.
	RunStateCapacityLost = RunState{Reason: "CapacityLost", Phase: RunPhasePending, whenTrue: []string{RunConditionAdmitted}}

	// --- Running -------------------------------------------------------------

	// RunStateGangBound — the full active width holds open leases.
	RunStateGangBound = RunState{Reason: "GangBound", Phase: RunPhaseRunning, whenTrue: []string{RunConditionAdmitted, RunConditionScheduled, RunConditionRunning}}
	// RunStateShrunk — running at a legal, reduced width after a resolver cut or
	// a voluntary shrink. Still Running, still Scheduled: a malleable run at a
	// width inside [Min,Max] is healthy, not degraded.
	RunStateShrunk = RunState{Reason: "Shrunk", Phase: RunPhaseRunning, whenTrue: []string{RunConditionAdmitted, RunConditionScheduled, RunConditionRunning}}

	// --- Terminal ------------------------------------------------------------

	// RunStateAllSucceeded — every active pod succeeded.
	RunStateAllSucceeded = RunState{Reason: "AllSucceeded", Phase: RunPhaseComplete, whenTrue: []string{RunConditionAdmitted, RunConditionCompleted}}
	// RunStateWorkloadFailed — a workload container failed terminally under a
	// Fail (or exhausted Retry) policy (R8 / R9 9A-3).
	RunStateWorkloadFailed = RunState{Reason: "WorkloadFailed", Phase: RunPhaseFailed, whenTrue: []string{RunConditionFailed}}
	// RunStateNodeFailureNoSpare — a fenced node took capacity the run could not
	// replace, and its grace expired.
	RunStateNodeFailureNoSpare = RunState{Reason: "NodeFailureNoSpare", Phase: RunPhaseFailed, whenTrue: []string{RunConditionFailed}}
	// RunStateUpstreamFailed — an upstream in the follow forest failed and this
	// run's policy (or its expired grace) fails it too.
	RunStateUpstreamFailed = RunState{Reason: "UpstreamFailed", Phase: RunPhaseFailed, whenTrue: []string{RunConditionFailed}}
	// RunStateFollowCycle — the follow edges form a cycle. A spec error, not an
	// upstream failure, and worth its own reason: the fix is different.
	RunStateFollowCycle = RunState{Reason: "FollowCycle", Phase: RunPhaseFailed, whenTrue: []string{RunConditionFailed}}
	// RunStateCheckpointExpired — the checkpoint grace window ran out before the
	// lost capacity came back.
	RunStateCheckpointExpired = RunState{Reason: "CheckpointExpired", Phase: RunPhaseFailed, whenTrue: []string{RunConditionFailed}}
	// RunStateEndedByResolver — the oversubscription resolver ended the run.
	RunStateEndedByResolver = RunState{Reason: "EndedByResolver", Phase: RunPhaseFailed, whenTrue: []string{RunConditionFailed}}
)

// AllRunStates is every declared state. Its only consumer is the table test that
// checks each one derives the phase it claims — which is the whole guarantee this
// file exists to provide, so it is worth the export.
var AllRunStates = []RunState{
	RunStateFollowWait,
	RunStateUnschedulable,
	RunStateUnfunded,
	RunStateReserved,
	RunStateGangForming,
	RunStateScheduling,
	RunStatePromised,
	RunStateCheckpointGrace,
	RunStateReclaimed,
	RunStateCapacityLost,
	RunStateGangBound,
	RunStateShrunk,
	RunStateAllSucceeded,
	RunStateWorkloadFailed,
	RunStateNodeFailureNoSpare,
	RunStateUpstreamFailed,
	RunStateFollowCycle,
	RunStateCheckpointExpired,
	RunStateEndedByResolver,
}

// SetRunState applies a state to a RunStatus: every managed condition is written
// (True for the ones the state names, False for the rest), and Phase is then
// DERIVED from what was written. Message goes on the conditions the state turned
// on, and on Status.Message for the existing printer columns and CLI.
//
// observedGeneration is the Run's metadata.generation, so a reader can tell a
// condition about the current spec from one left over from the previous edit.
func SetRunState(status *RunStatus, observedGeneration int64, state RunState, message string) {
	on := make(map[string]bool, len(state.whenTrue))
	for _, t := range state.whenTrue {
		on[t] = true
	}
	explains := make(map[string]bool, len(state.whenFalse))
	for _, t := range state.whenFalse {
		explains[t] = true
	}
	for _, condType := range runConditionTypes {
		cond := metav1.Condition{
			Type:               condType,
			Status:             metav1.ConditionFalse,
			Reason:             RunReasonNotApplicable,
			ObservedGeneration: observedGeneration,
		}
		if on[condType] || explains[condType] {
			cond.Reason = state.Reason
			cond.Message = message
		}
		if on[condType] {
			cond.Status = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&status.Conditions, cond)
	}
	status.Phase = DeriveRunPhase(status.Conditions)
	status.Message = message
}

// SetRunMessage refreshes the human-readable message without changing the state:
// it updates Status.Message and the Message of every condition currently True,
// leaving statuses, reasons and lastTransitionTime alone.
//
// This exists for the running commentary the elastic loop produces ("grew to 8
// GPUs", "unable to shrink: ...") — a Running run that grows is still exactly as
// GangBound as it was, so naming a state there would be a lie about a transition.
// The alternative, letting those sites write Status.Message directly, is what
// makes the message and the conditions drift.
func SetRunMessage(status *RunStatus, message string) {
	for i := range status.Conditions {
		if status.Conditions[i].Status == metav1.ConditionTrue {
			status.Conditions[i].Message = message
		}
	}
	status.Message = message
}

// DeriveRunPhase computes the phase from the conditions. This is the only place a
// run's phase is decided.
//
// The Blocked rule is the one worth explaining: blocking BEFORE admission is what
// Waiting means (an unadmitted run parked on its follow edges), while blocking
// AFTER it is a Pending run that still holds capacity with a deadline running
// down — a checkpoint grace. Collapsing them would report a run holding GPUs as
// though it were merely queued.
func DeriveRunPhase(conditions []metav1.Condition) string {
	isTrue := func(t string) bool { return meta.IsStatusConditionTrue(conditions, t) }
	switch {
	case isTrue(RunConditionFailed):
		return RunPhaseFailed
	case isTrue(RunConditionCompleted):
		return RunPhaseComplete
	case isTrue(RunConditionRunning):
		return RunPhaseRunning
	case isTrue(RunConditionBlocked) && !isTrue(RunConditionAdmitted):
		return RunPhaseWaiting
	default:
		return RunPhasePending
	}
}

// --- Lease -------------------------------------------------------------------

const (
	// LeaseConditionActive — the lease is open: it charges a budget and holds
	// GPUs. This is the single most consequential bit in the system (an open
	// lease nobody closes bills forever), so it is worth being able to
	// `kubectl wait` and select on.
	LeaseConditionActive = "Active"
	// LeaseConditionClosed — the lease is closed; Reason is the ClosureReason.
	LeaseConditionClosed = "Closed"
)

// LeaseReasonMinted is the Active reason at mint time. A closed lease's reason is
// its ClosureReason verbatim, which is already a closed vocabulary owned by
// controllers.CloseLease (the sole closer).
const LeaseReasonMinted = "Minted"

// SetLeaseConditions mirrors a lease's open/closed fact into conditions. It reads
// Status.Closed/ClosureReason and writes only Conditions, so it cannot become a
// second writer of the closure fact — hack/antifake enforces that CloseLease is
// the only one, and this function must never make that a lie.
func SetLeaseConditions(status *LeaseStatus, observedGeneration int64) {
	active, closed := metav1.ConditionTrue, metav1.ConditionFalse
	reason := LeaseReasonMinted
	message := "open: charging its payer envelope and holding its slice"
	if status.Closed {
		active, closed = metav1.ConditionFalse, metav1.ConditionTrue
		reason = status.ClosureReason
		if reason == "" {
			reason = "Closed"
		}
		message = "closed: no longer charging"
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type: LeaseConditionActive, Status: active, Reason: reason,
		Message: message, ObservedGeneration: observedGeneration,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type: LeaseConditionClosed, Status: closed, Reason: reason,
		Message: message, ObservedGeneration: observedGeneration,
	})
}

// --- Reservation -------------------------------------------------------------

const (
	// ReservationConditionForecast — a start time is known.
	ReservationConditionForecast = "Forecast"
	// ReservationConditionActivated — the reservation has fired.
	ReservationConditionActivated = "Activated"
)

const (
	ReservationReasonEarliestStartKnown = "EarliestStartKnown"
	ReservationReasonUnforecastable     = "Unforecastable"
	ReservationReasonActivated          = "Activated"
	ReservationReasonPending            = "Pending"
	ReservationReasonReleased           = "Released"
)

// SetReservationConditions derives the two reservation conditions from the state
// the reconciler already writes, so there is no second source of truth to drift.
func SetReservationConditions(status *ReservationStatus, observedGeneration int64) {
	forecast, freason := metav1.ConditionFalse, ReservationReasonUnforecastable
	fmsg := "no earliest start could be forecast"
	if status.Forecast != nil {
		forecast, freason = metav1.ConditionTrue, ReservationReasonEarliestStartKnown
		fmsg = "an earliest start is forecast"
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type: ReservationConditionForecast, Status: forecast, Reason: freason,
		Message: fmsg, ObservedGeneration: observedGeneration,
	})

	activated, areason := metav1.ConditionFalse, ReservationReasonPending
	amsg := "waiting for its activation window"
	switch {
	case status.ReleasedAt != nil:
		areason, amsg = ReservationReasonReleased, "released"
	case status.ActivatedAt != nil:
		activated, areason, amsg = metav1.ConditionTrue, ReservationReasonActivated, "activated"
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type: ReservationConditionActivated, Status: activated, Reason: areason,
		Message: amsg, ObservedGeneration: observedGeneration,
	})
}

// --- Budget ------------------------------------------------------------------

// BudgetConditionHealthy is False when the budget is overcommitted: some envelope
// has negative headroom, i.e. more width is charged against it than it can fund.
// Quota and capacity vary independently and reconcile only at scheduling instants,
// so this is a normal, reportable state — not a crash.
const BudgetConditionHealthy = "Healthy"

const (
	BudgetReasonHealthy       = "Healthy"
	BudgetReasonOvercommitted = "Overcommitted"
)

// SetBudgetConditions derives the health condition from the headroom the budget
// reconciler has just written.
func SetBudgetConditions(status *BudgetStatus, observedGeneration int64) {
	var over []string
	for _, h := range status.Headroom {
		if h.Concurrency < 0 {
			over = append(over, h.Name)
		}
	}
	cond := metav1.Condition{
		Type: BudgetConditionHealthy, Status: metav1.ConditionTrue,
		Reason: BudgetReasonHealthy, Message: "every envelope has non-negative headroom",
		ObservedGeneration: observedGeneration,
	}
	if len(over) > 0 {
		cond.Status = metav1.ConditionFalse
		cond.Reason = BudgetReasonOvercommitted
		cond.Message = "overcommitted envelopes: " + strings.Join(over, ", ")
	}
	meta.SetStatusCondition(&status.Conditions, cond)
}
