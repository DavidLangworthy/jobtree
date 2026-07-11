package controllers

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/forecast"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/pack"
	"github.com/davidlangworthy/jobtree/pkg/resolver"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// Run phases used for status reporting.
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

// defaultUpstreamFailureGrace bounds how long a follower waits on a failed
// upstream (under the default "wait" policy) before failing itself.
const defaultUpstreamFailureGrace = 30 * time.Minute

// ClusterState stores the in-memory view of the cluster for the simplified controller.
type ClusterState struct {
	Runs         map[string]*v1.Run
	Budgets      []v1.Budget
	Nodes        []topology.SourceNode
	Leases       []v1.Lease
	Pods         []binder.PodManifest
	Reservations map[string]*v1.Reservation
}

// RunController drives immediate admissions using the local state.
type RunController struct {
	State *ClusterState
	Clock Clock
	// Period is the cluster accounting horizon (R14): admission lookahead
	// and the evaluation cadence both measure width × Period. Zero means
	// funding.DefaultPeriod.
	Period time.Duration
	// Recorder receives observability events as the engine discovers them
	// (admit, reserve, activate, resolver action, node-failure swap,
	// complete). Nil is safe — the CLI's local simulator leaves it unset;
	// the k8s bridge wires a real client-go EventRecorder.
	Recorder EventRecorder
}

// EventRecorder receives observability events keyed to the Run object they
// concern. The k8s bridge implements this against a real
// client-go/controller-runtime EventRecorder (surfacing real corev1.Events);
// the pure engine package has no such dependency itself.
type EventRecorder interface {
	Event(run *v1.Run, eventType, reason, message string)
}

// Event type strings mirroring corev1.EventTypeNormal/EventTypeWarning
// without giving the pure engine package a k8s.io/api dependency.
const (
	EventTypeNormal  = "Normal"
	EventTypeWarning = "Warning"
)

// emit is a nil-safe convenience wrapper around Recorder.Event.
func (c *RunController) emit(run *v1.Run, eventType, reason, message string) {
	if c.Recorder == nil || run == nil {
		return
	}
	c.Recorder.Event(run, eventType, reason, message)
}

// NewRunController constructs a controller with the given state store.
func NewRunController(state *ClusterState, clock Clock) *RunController {
	if clock == nil {
		clock = RealClock{}
	}
	if state.Runs == nil {
		state.Runs = make(map[string]*v1.Run)
	}
	if state.Reservations == nil {
		state.Reservations = make(map[string]*v1.Reservation)
	}
	return &RunController{State: state, Clock: clock}
}

// evaluate derives the funding classification from the current facts. This
// is the one derivation (quota-semantics.md Decision 3): cover, the
// resolver, run status, and budget status all consume it — nothing reads a
// classification back from status.
func (c *RunController) evaluate(now time.Time) *funding.Evaluation {
	return funding.Evaluate(funding.Input{
		Budgets: c.State.Budgets,
		Leases:  c.State.Leases,
		Runs:    c.State.Runs,
		Now:     now,
		Period:  c.Period,
	})
}

// Reconcile admits the run identified by namespace/name when feasible.
func (c *RunController) Reconcile(namespace, name string) error {
	key := keys.NamespacedKey(namespace, name)
	run, ok := c.State.Runs[key]
	if !ok {
		return fmt.Errorf("run %s/%s not found", namespace, name)
	}

	// The oracle. Deferred so it runs on EVERY return, including the error
	// returns: Bridge.WithWorld applies the state diff even when the engine
	// fails, so a half-applied admission is still a state someone must live in.
	before := c.snapshotWorld()
	defer c.checkInvariants("RunController.Reconcile", before)

	flavor := run.Spec.Resources.GPUType
	start := time.Now()
	result := "noop"
	defer func() {
		metrics.ObserveAdmission(flavor, result, time.Since(start))
	}()
	if run.Status.Phase == RunPhaseComplete {
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		return nil
	}

	now := c.Clock.Now()

	// The failure edge (R9 9A-3), BEFORE completion: a terminally Failed active pod
	// means the gang cannot just "complete", and under the default Fail policy the
	// run must fail and stop charging its budget rather than hang Running forever.
	if run.Status.Phase == RunPhaseRunning {
		if handled, res := c.handleWorkloadFailure(run, now); handled {
			run.Status.Width = summarizeRunWidth(run, c.State.Leases)
			result = res
			return nil
		}
	}

	// A gang whose active pods have all reached a terminal phase (Succeeded — or,
	// under the Ignore policy, Failed) is complete: finalize it and close its leases
	// so it stops holding GPUs and charging its budget.
	if run.Status.Phase == RunPhaseRunning && c.runGangComplete(run) {
		c.completeRun(run, now)
		result = "completed"
		return nil
	}

	// Follow gate: a run with unmet dependencies waits (or fails) before any
	// admission — placed before topology/adoption so a Waiting run never packs,
	// reserves, or is flipped Running by a stray lease. Once Running/terminal,
	// follow no longer applies.
	if isPreAdmission(run.Status.Phase) && !c.evaluateFollow(run, now) {
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		result = "waiting"
		return nil
	}

	// Checkpoint grace: a run parked Pending by HandleNodeFailure (no spare,
	// but spec.runtime.checkpoint > 0) gets to keep trying to re-admit
	// (below, and via reservation) only until this deadline; past it, the
	// checkpoint is presumed stale and the run fails rather than retrying
	// forever.
	if run.Status.Phase == RunPhasePending && run.Status.CheckpointDeadline != nil && !now.Before(run.Status.CheckpointDeadline.Time) {
		c.failRun(run, "checkpoint grace expired without recovering capacity")
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		result = "failed"
		return nil
	}

	c.mirrorETA(run, now)

	usage := computeUsage(c.State.Leases, now)
	snapshot, err := topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
	if err != nil {
		run.Status.Phase = RunPhasePending
		run.Status.Message = err.Error()
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		return nil
	}

	ev := c.evaluate(now)
	inventory := cover.NewInventory(ev)

	run.Status.Width = summarizeRunWidth(run, c.State.Leases)
	run.Status.Funding = summarizeRunFunding(run, ev)

	if run.Status.Phase == RunPhaseRunning {
		if run.Spec.Malleable != nil {
			if err := c.reconcileElasticRun(run, snapshot, inventory, now); err != nil {
				result = "error"
				return err
			}
			run.Status.Width = summarizeRunWidth(run, c.State.Leases)
			run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))
		}
		return nil
	}

	// Adopt the plugin's leases — but only at the run's full active width (R2).
	// Same half-applied-admission adoption as in activateReservation, reachable
	// from any watch event (lease creates included), so the wedge heals without
	// waiting for an activation tick. Terminal runs are excluded: adoption must
	// not resurrect them (ruling 2026-07-02).
	if run.Status.Phase != RunPhaseFailed && run.Status.Phase != RunPhaseComplete {
		allocated := baseGangGPUsForRun(key, c.State.Leases)
		expected := minRunnableGPUs(run)
		switch {
		case allocated > 0 && allocated >= expected:
			run.Status.Phase = RunPhaseRunning
			run.Status.Message = fmt.Sprintf("adopted %d GPUs of open leases from an earlier admission", allocated)
			c.releasePendingReservations(run, now)
			run.Status.PendingReservation = nil
			run.Status.EarliestStart = nil
			// Whole again: the node-failure checkpoint grace no longer applies
			// (activateReservation's adoption already clears it).
			run.Status.CheckpointDeadline = nil
			run.Status.Width = summarizeRunWidth(run, c.State.Leases)
			run.Status.Funding = summarizeRunFunding(run, ev)
			result = "adopted"
			return nil
		case allocated > 0:
			// A partial gang has NOT started. "Start together or not at all"
			// (docs/index.md): reporting Running here would hide N−1 containers
			// charging budget behind a healthy phase, and every consumer that
			// keys off Phase — runGangComplete, the elastic loop, the CLI —
			// would read it as whole. Stay pre-Running, re-emit whatever pods
			// went missing, and wait; this block re-runs on the next lease
			// create and adopts the moment the last slice lands.
			//
			// Returning early is load-bearing: falling through to admission
			// would re-plan the run against a snapshot its own leases already
			// occupy, reporting them as a deficit and evicting other runs to
			// cover capacity it is holding.
			created := c.topUpActiveGang(run)
			run.Status.Phase = RunPhasePending
			run.Status.Message = fmt.Sprintf("assembling gang: %d/%d GPUs held (awaiting the jobtree scheduler)", allocated, expected)
			run.Status.Width = summarizeRunWidth(run, c.State.Leases)
			if run.Status.Width != nil {
				run.Status.Width.Pending = fmt.Sprintf("Assemble to %d", expected)
			}
			run.Status.Funding = summarizeRunFunding(run, ev)
			if created > 0 {
				c.emit(run, EventTypeWarning, "GangIncomplete", run.Status.Message)
			}
			result = "assembling"
			return nil
		}
		// allocated == 0: nothing active is running. A run whose only open lease
		// is a leftover Spare falls through here rather than flipping Running on
		// held standby capacity that does no work.
	}

	// A promised activation's pods are already out, pre-authorized (R3): its
	// cover is EXPECTED to fail until quota returns (that is why the promise
	// fired), so re-entering admission here would just plan a spurious second
	// reservation every tick. Wait instead — the plugin binds the Promise pods
	// (they skip its funding gate) and the adoption path above flips Running.
	if runHasPromisePods(c.State.Pods, run) {
		run.Status.Phase = RunPhasePending
		run.Status.Message = fmt.Sprintf("promised start: scheduling %d GPUs (awaiting the jobtree scheduler)", run.Spec.Resources.TotalGPUs)
		result = "scheduling"
		return nil
	}

	reclaimed := false
	for {
		packPlan, err := planPlacement(run, snapshot)
		if err != nil {
			planErr, ok := err.(*pack.PlanError)
			if !ok || planErr.Reason == pack.FailureReasonInvalidRequest {
				run.Status.Phase = RunPhasePending
				run.Status.Message = err.Error()
				result = "waiting"
				return nil
			}
			// R14: a funded admission reclaims opportunistic capacity
			// before falling back to a reservation. One attempt per pass;
			// fragmentation (deficit 0 with free GPUs) still reserves.
			if !reclaimed && c.reclaimForAdmission(run, ev, inventory, snapshot, now) {
				reclaimed = true
				usage = computeUsage(c.State.Leases, now)
				snapshot, err = topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
				if err != nil {
					result = "error"
					return err
				}
				ev = c.evaluate(now)
				inventory = cover.NewInventory(ev)
				continue
			}
			request := cover.Request{Owner: run.Spec.Owner, Flavor: run.Spec.Resources.GPUType, Quantity: run.Spec.Resources.TotalGPUs, Now: now, Admitted: run.CreationTimestamp.Time, RunKey: key, AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow}
			if run.Spec.Funding != nil {
				request.Sponsors = append(request.Sponsors, run.Spec.Funding.Sponsors...)
			}
			if err := c.planReservation(run, snapshot, nil, planErr, nil, ev, request, now); err != nil {
				result = "error"
				return err
			}
			result = "reserved"
			return nil
		}

		location := deriveLocation(packPlan)
		spareTotal := expectedSpareTotal(run, &packPlan)
		quantity := run.Spec.Resources.TotalGPUs + spareTotal
		request := cover.Request{
			Owner:       run.Spec.Owner,
			Flavor:      run.Spec.Resources.GPUType,
			Quantity:    quantity,
			Location:    location,
			Now:         now,
			Admitted:    run.CreationTimestamp.Time,
			AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow,
		}
		if run.Spec.Funding != nil {
			request.Sponsors = append(request.Sponsors, run.Spec.Funding.Sponsors...)
			if run.Spec.Funding.MaxBorrowGPUs != nil {
				remaining := *run.Spec.Funding.MaxBorrowGPUs - borrowedGPUsForRun(ev, run)
				if remaining < 0 {
					remaining = 0
				}
				request.MaxBorrowGPUs = &remaining
			}
		}

		// Cover is now a fundability PREDICTION, not a commit: if the run's
		// width cannot be funded from its family/sponsors, reserve; the plugin
		// is never handed unfundable pods to gate. The authoritative funding
		// decision and the mint happen in the scheduler plugin's Permit/PreBind
		// (borrow-vs-build.md §9) — the controller mints nothing.
		if _, coverErr := inventory.Plan(request); coverErr != nil {
			if planErr, ok := coverErr.(*cover.PlanError); ok && planErr.Reason != cover.FailureReasonInvalidRequest {
				if err := c.planReservation(run, snapshot, &packPlan, nil, planErr, ev, request, now); err != nil {
					result = "error"
					return err
				}
				result = "reserved"
				return nil
			}
			run.Status.Phase = RunPhasePending
			run.Status.Message = coverErr.Error()
			result = "waiting"
			return nil
		}

		// Placeable and fundable: emit unscheduled intent pods for the plugin to
		// schedule and fund. The adoption path (above) flips the run Running once
		// the plugin's leases appear — the run stays Pending until then.
		created := c.emitIntentPods(run, packPlan)
		run.Status.Phase = RunPhasePending
		run.Status.Message = fmt.Sprintf("scheduling %d GPUs (awaiting the jobtree scheduler)", packPlan.TotalGPUs)
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		run.Status.Funding = summarizeRunFunding(run, ev)
		// Emit the observable request-for-width event once, when the intent pods
		// are first created — not on every reconcile of an already-pending run.
		if created > 0 {
			c.emit(run, EventTypeNormal, "Scheduling", run.Status.Message)
		}
		result = "scheduling"
		return nil
	}
}

// reclaimForAdmission clears an admission's physical deficit from unfunded
// work only: it fires only when the run's demand is fundable (the request
// must not itself be born opportunistic) and never touches funded leases —
// funded-work preemption stays reservation-activation-only (R7). The
// unfunded pool is judged with the prospective claim ranked in, so family
// work this admission recalls is reclaimable too. It reports whether
// anything was reclaimed.
func (c *RunController) reclaimForAdmission(run *v1.Run, ev *funding.Evaluation, inventory *cover.Inventory, snapshot *topology.Snapshot, now time.Time) bool {
	totalNeeded := int(run.Spec.Resources.TotalGPUs + expectedSpareTotal(run, nil))
	deficit := computeDeficit(snapshot, nil, totalNeeded)
	if deficit <= 0 {
		return false
	}
	request := cover.Request{
		Owner:       run.Spec.Owner,
		Flavor:      run.Spec.Resources.GPUType,
		Quantity:    run.Spec.Resources.TotalGPUs + expectedSpareTotal(run, nil),
		Now:         now,
		Admitted:    run.CreationTimestamp.Time,
		RunKey:      keys.NamespacedKey(run.Namespace, run.Name),
		AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow,
	}
	if run.Spec.Funding != nil {
		request.Sponsors = append(request.Sponsors, run.Spec.Funding.Sponsors...)
		if run.Spec.Funding.MaxBorrowGPUs != nil {
			remaining := *run.Spec.Funding.MaxBorrowGPUs - borrowedGPUsForRun(ev, run)
			if remaining < 0 {
				remaining = 0
			}
			request.MaxBorrowGPUs = &remaining
		}
	}
	plan, err := inventory.Plan(request)
	if err != nil {
		return false
	}
	resolution, err := resolver.Resolve(resolver.Input{
		Deficit:      deficit,
		Flavor:       run.Spec.Resources.GPUType,
		SeedSource:   keys.NamespacedKey(run.Namespace, run.Name),
		Now:          now,
		Nodes:        c.State.Nodes,
		Leases:       activeLeasePointers(c.State.Leases),
		Runs:         c.State.Runs,
		Evaluation:   c.hypotheticalEvaluation(run, plan, now),
		OnlyUnfunded: true,
	})
	if err != nil || len(resolution.Actions) == 0 {
		return false
	}
	c.applyResolution(resolution, now)
	return true
}

// hypotheticalEvaluation ranks a prospective claim into the derivation by
// evaluating the current facts plus synthetic open leases paying the plan's
// envelopes. This is the ranking function's "a new claim may displace the
// lowest-ranked funded claim — that is recall" made operational: family
// claims the prospective claim outranks evaluate unfunded here and join the
// first-cut reclaim pool. The synthetic leases exist only inside this
// evaluation; nothing is written to state.
func (c *RunController) hypotheticalEvaluation(run *v1.Run, plan cover.Plan, now time.Time) *funding.Evaluation {
	leases := make([]v1.Lease, 0, len(c.State.Leases)+len(plan.Segments))
	leases = append(leases, c.State.Leases...)
	for i, seg := range plan.Segments {
		if seg.Quantity <= 0 {
			continue
		}
		nodes := make([]string, int(seg.Quantity))
		for j := range nodes {
			nodes[j] = fmt.Sprintf("hypothetical#%d", j)
		}
		leases = append(leases, v1.Lease{
			ObjectMeta: v1.ObjectMeta{
				Namespace: run.Namespace,
				Name:      fmt.Sprintf("%s-hypothetical-%d", run.Name, i),
			},
			Spec: v1.LeaseSpec{
				Owner:                 seg.Owner,
				RunRef:                v1.RunReference{Name: run.Name, Namespace: run.Namespace},
				Slice:                 v1.LeaseSlice{Nodes: nodes, Role: binder.RoleActive},
				Interval:              v1.LeaseInterval{Start: v1.NewTime(now)},
				PaidByBudgetNamespace: seg.Namespace,
				PaidByBudget:          seg.BudgetName,
				PaidByEnvelope:        seg.EnvelopeName,
				Reason:                "Hypothetical",
			},
		})
	}
	return funding.Evaluate(funding.Input{
		Budgets: c.State.Budgets,
		Leases:  leases,
		Runs:    c.State.Runs,
		Now:     now,
		Period:  c.Period,
	})
}

// mirrorETA reflects a workload-reported ETA (the pod annotation) into the run
// status, taking the latest estimate across the gang. It manages only the
// "job" source: a CLI-set ("controller") ETA is left untouched, and a
// job-sourced ETA is cleared once no pod reports one. Observability only —
// nothing schedules on it.
func (c *RunController) mirrorETA(run *v1.Run, now time.Time) {
	var latest time.Time
	found := false
	for i := range c.State.Pods {
		pod := &c.State.Pods[i]
		if pod.Namespace != run.Namespace || pod.Labels[binder.LabelRunName] != run.Name {
			continue
		}
		raw := pod.Annotations[binder.EtaAnnotation]
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			continue
		}
		if !found || t.After(latest) {
			latest, found = t, true
		}
	}
	if found {
		run.Status.ETA = &v1.RunETA{
			EstimatedCompletion: v1.NewTime(latest),
			ReportedAt:          v1.NewTime(now),
			Source:              "job",
		}
		return
	}
	if run.Status.ETA != nil && run.Status.ETA.Source == "job" {
		run.Status.ETA = nil
	}
}

// runGangComplete reports whether every active (non-spare) pod of the run has
// reached the Succeeded phase. Spare pods are held capacity and do not gate
// completion; a run with no active pods (never bound) is not complete. A pod
// that Failed is simply not Succeeded, so it holds the run open rather than
// completing or failing it.
func (c *RunController) runGangComplete(run *v1.Run) bool {
	// Under the Ignore failure policy (R9 9A-3) a terminally Failed active pod is a
	// terminal member, not a blocker — an embarrassingly-parallel role completes when
	// every member has finished, succeeded or not.
	policy, _, _ := failurePolicyFor(run)
	ignoreFailed := policy == v1.FailurePolicyIgnore
	sawActive := false
	for i := range c.State.Pods {
		pod := &c.State.Pods[i]
		if pod.Namespace != run.Namespace || pod.Labels[binder.LabelRunName] != run.Name {
			continue
		}
		if pod.Labels[binder.LabelRunRole] == binder.RoleSpare {
			continue
		}
		sawActive = true
		if pod.Phase == binder.PodPhaseSucceeded {
			continue
		}
		if ignoreFailed && pod.Phase == binder.PodPhaseFailed {
			continue
		}
		return false
	}
	return sawActive
}

// completeRun finalizes a succeeded gang: it closes the run's open leases
// (reason Completed) so the funding derivation stops counting them, drops the
// run's pods, and records the terminal phase.
func (c *RunController) completeRun(run *v1.Run, now time.Time) {
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	// The success path has always retired both planes. It now says so with the same
	// function every failure path uses, rather than a second copy of the loop: two
	// implementations of one obligation is how the failure paths came to do half.
	c.releaseRun(run, "Completed", now)
	run.Status.Phase = RunPhaseComplete
	run.Status.Message = "run completed: all active pods succeeded"
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
	run.Status.Width = summarizeRunWidth(run, c.State.Leases)
	run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))
	metrics.ClearElasticWidth(runKey)
	c.emit(run, EventTypeNormal, "Completed", run.Status.Message)
}

// failurePolicyFor returns a run's role FailurePolicy (default Fail) with its Retries
// and Backoff (R9 9A-3). A legacy Roles-less run uses the default.
func failurePolicyFor(run *v1.Run) (policy string, retries int32, backoff time.Duration) {
	if len(run.Spec.Roles) == 0 {
		return v1.FailurePolicyFail, 0, 0
	}
	r := &run.Spec.Roles[0]
	policy = r.FailurePolicy
	if policy == "" {
		policy = v1.FailurePolicyFail
	}
	if r.Retries != nil {
		retries = *r.Retries
	}
	if r.Backoff != nil {
		backoff = r.Backoff.Duration
	}
	return policy, retries, backoff
}

// firstFailedActivePod returns the run's first terminally-Failed active (non-spare)
// pod, or nil.
func (c *RunController) firstFailedActivePod(run *v1.Run) *binder.PodManifest {
	for i := range c.State.Pods {
		p := &c.State.Pods[i]
		if p.Namespace != run.Namespace || p.Labels[binder.LabelRunName] != run.Name {
			continue
		}
		if p.Labels[binder.LabelRunRole] == binder.RoleSpare {
			continue
		}
		if p.Phase == binder.PodPhaseFailed {
			return p
		}
	}
	return nil
}

// handleWorkloadFailure applies the run's role FailurePolicy to a terminally failed
// active pod (R9 9A-3). It returns handled=true when it has decided the pass — the
// run failed, or a retry was issued or is backing off. Ignore returns handled=false
// so the completion gate (which counts a Failed pod as terminal under Ignore)
// finalizes the run; a run with no failed active pod also returns false.
func (c *RunController) handleWorkloadFailure(run *v1.Run, now time.Time) (handled bool, result string) {
	failed := c.firstFailedActivePod(run)
	if failed == nil {
		return false, ""
	}
	policy, retries, backoff := failurePolicyFor(run)
	switch policy {
	case v1.FailurePolicyIgnore:
		return false, "" // the completion gate treats a Failed member as terminal here

	case v1.FailurePolicyRetry:
		if run.Status.FailedAttempts < retries {
			// Backoff: park until the deadline so a crash-looping member does not spin.
			if backoff > 0 {
				if run.Status.RetryAfter == nil {
					d := v1.NewTime(now.Add(backoff))
					run.Status.RetryAfter = &d
					c.emit(run, EventTypeWarning, "WorkloadRetryScheduled",
						fmt.Sprintf("active pod %s failed; retrying after %s", failed.Name, backoff))
					return true, "retrying"
				}
				if now.Before(run.Status.RetryAfter.Time) {
					return true, "retrying" // still backing off; the reconcile requeues
				}
			}
			// Close the failed member's still-open lease (else it charges forever and
			// the re-emitted member's fresh lease double-counts the rank), drop the
			// pod, and re-emit the missing member — the plugin re-mints its lease.
			c.closeMemberLease(run, failed.NodeName, now)
			c.dropPod(failed.Namespace, failed.Name)
			run.Status.FailedAttempts++
			run.Status.RetryAfter = nil
			created := c.topUpActiveGang(run)
			c.emit(run, EventTypeWarning, "WorkloadRetry",
				fmt.Sprintf("re-emitting failed active pod %s (attempt %d/%d, %d pod(s))", failed.Name, run.Status.FailedAttempts, retries, created))
			return true, "retrying"
		}
		// Retries exhausted → fall through to Fail.
	}

	// Fail (default), or Retry exhausted.
	c.failWorkload(run, fmt.Sprintf("active pod %s terminally failed; failing the gang", failed.Name), now)
	return true, "failed"
}

// failWorkload drives the failure edge (R9 9A-3): terminal Failed, close the run's
// open leases (WorkloadFailed) and drop its pods, and clear parked-state fields so a
// follower's grace path sees an honest terminal upstream instead of hanging.
func (c *RunController) failWorkload(run *v1.Run, msg string, now time.Time) {
	run.Status.Phase = RunPhaseFailed
	run.Status.Message = msg
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
	run.Status.FollowDeadline = nil
	run.Status.CheckpointDeadline = nil
	run.Status.RetryAfter = nil
	if closed := c.releaseRun(run, "WorkloadFailed", now); closed > 0 {
		c.emit(run, EventTypeWarning, "LeasesReleased",
			fmt.Sprintf("released %d open lease(s) held by the failed run", closed))
	}
	c.emit(run, EventTypeWarning, "WorkloadFailed", msg)
}

// closeMemberLease closes the run's open active lease on node (a failed member's
// lease) so a Retry re-emit does not leave two open leases for one rank.
func (c *RunController) closeMemberLease(run *v1.Run, node string, now time.Time) {
	if node == "" {
		return
	}
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	for i := range c.State.Leases {
		l := &c.State.Leases[i]
		if l.Status.Closed || l.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		if keys.NamespacedKey(l.Spec.RunRef.Namespace, l.Spec.RunRef.Name) != runKey {
			continue
		}
		for _, slot := range l.Spec.Slice.Nodes {
			if nodeFromSlot(slot) == node {
				CloseLease(l, "WorkloadFailed", now)
				return
			}
		}
	}
}

// dropPod removes one pod manifest from state so Bridge.apply deletes it.
func (c *RunController) dropPod(namespace, name string) {
	kept := c.State.Pods[:0]
	for _, p := range c.State.Pods {
		if p.Namespace == namespace && p.Name == name {
			continue
		}
		kept = append(kept, p)
	}
	c.State.Pods = kept
}

// isPreAdmission reports whether a run has not yet started or terminated, so
// the follow gate still applies.
func isPreAdmission(phase string) bool {
	switch phase {
	case RunPhaseRunning, RunPhaseComplete, RunPhaseFailed:
		return false
	}
	return true
}

// evaluateFollow gates a pre-admission run on its follow dependencies. It
// returns true when the run may proceed to admission (no deps, or every
// upstream Completed); otherwise it sets Waiting or Failed and returns false.
// Existence and cycle detection live here because the webhook has no cluster
// view.
func (c *RunController) evaluateFollow(run *v1.Run, now time.Time) bool {
	if run.Spec.Follow == nil || len(run.Spec.Follow.After) == 0 {
		run.Status.FollowDeadline = nil
		return true
	}
	if path, cyclic := c.followCycle(run); cyclic {
		c.failRun(run, "follow cycle: "+path)
		return false
	}

	var pending, failed []string
	for _, name := range run.Spec.Follow.After {
		up, ok := c.State.Runs[keys.NamespacedKey(run.Namespace, name)]
		switch {
		case !ok:
			failed = append(failed, name+" (deleted)")
		case up.Status.Phase == RunPhaseComplete:
			// satisfied
		case up.Status.Phase == RunPhaseFailed:
			failed = append(failed, name)
		default:
			pending = append(pending, name)
		}
	}

	if len(failed) > 0 {
		if run.Spec.Follow.OnUpstreamFailure == v1.OnUpstreamFailureFail {
			c.failRun(run, "upstream failed: "+strings.Join(failed, ", "))
			return false
		}
		// "wait" (default): give the researcher a grace window to fix and
		// resubmit the failed stage, then fail so it is not a silent zombie.
		if run.Status.FollowDeadline == nil {
			deadline := v1.NewTime(now.Add(followGrace(run.Spec.Follow)))
			run.Status.FollowDeadline = &deadline
		}
		if !now.Before(run.Status.FollowDeadline.Time) {
			c.failRun(run, "upstream failed and grace expired: "+strings.Join(failed, ", "))
			return false
		}
		c.setWaiting(run, fmt.Sprintf("waiting: upstream failed (%s); fails at %s if unresolved",
			strings.Join(failed, ", "), run.Status.FollowDeadline.Time.UTC().Format(time.RFC3339)))
		return false
	}
	if len(pending) > 0 {
		run.Status.FollowDeadline = nil
		c.setWaiting(run, "waiting for: "+strings.Join(pending, ", "))
		return false
	}
	run.Status.FollowDeadline = nil
	return true
}

// followGrace is the run's configured wait window on a failed upstream, or the
// default.
func followGrace(f *v1.RunFollow) time.Duration {
	if f.UpstreamFailureGrace != nil {
		return f.UpstreamFailureGrace.Duration
	}
	return defaultUpstreamFailureGrace
}

// checkpointGrace returns the run's configured checkpoint window, or zero
// when unset — the real (only) reader of RunSpec.Runtime.Checkpoint. Zero
// means a node failure without a spare fails the run immediately, same as
// before this field was wired.
func checkpointGrace(run *v1.Run) time.Duration {
	if run == nil || run.Spec.Runtime == nil {
		return 0
	}
	return run.Spec.Runtime.Checkpoint.Duration
}

// followCycle reports whether the run's transitive follow closure contains a
// cycle (which would deadlock it), returning a readable path. Same-namespace.
func (c *RunController) followCycle(start *v1.Run) (string, bool) {
	inStack := make(map[string]bool)
	done := make(map[string]bool)
	var path []string
	var dfs func(r *v1.Run) (string, bool)
	dfs = func(r *v1.Run) (string, bool) {
		key := keys.NamespacedKey(r.Namespace, r.Name)
		inStack[key] = true
		path = append(path, r.Name)
		if r.Spec.Follow != nil {
			for _, name := range r.Spec.Follow.After {
				up, ok := c.State.Runs[keys.NamespacedKey(r.Namespace, name)]
				if !ok {
					continue
				}
				upKey := keys.NamespacedKey(up.Namespace, up.Name)
				if inStack[upKey] {
					return strings.Join(append(path, up.Name), " -> "), true
				}
				if done[upKey] {
					continue
				}
				if p, cyclic := dfs(up); cyclic {
					return p, true
				}
			}
		}
		inStack[key] = false
		done[key] = true
		path = path[:len(path)-1]
		return "", false
	}
	return dfs(start)
}

// setWaiting parks a run on its follow dependencies (not admitted, no
// reservation).
func (c *RunController) setWaiting(run *v1.Run, msg string) {
	run.Status.Phase = RunPhaseWaiting
	run.Status.Message = msg
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
}

// failRun terminally fails a pre-admission run (a follow cycle, an upstream
// failure past grace, or an expired checkpoint grace window).
//
// It used to *assert* that such a run holds no leases and close nothing. Nothing
// enforced that, and a run parked in checkpoint grace can still hold a spare on a
// healthy node — so the assertion was the R25 immortal-lease class written as a
// comment. Enforce it instead: a failed run holds no open leases, or its budget
// pays for GPUs nobody is using until someone deletes the Run object.
func (c *RunController) failRun(run *v1.Run, msg string) {
	run.Status.Phase = RunPhaseFailed
	run.Status.Message = msg
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
	run.Status.FollowDeadline = nil
	run.Status.CheckpointDeadline = nil
	if closed := c.releaseRun(run, "RunFailed", c.Clock.Now()); closed > 0 {
		c.emit(run, EventTypeWarning, "LeasesReleased", fmt.Sprintf(
			"released %d open lease(s) still held by the failed run", closed))
	}
	c.emit(run, EventTypeWarning, "Failed", msg)
}

// releasePendingReservations marks every Pending reservation for the run as
// superseded and clears the run's reservation pointers.
func (c *RunController) releasePendingReservations(run *v1.Run, now time.Time) {
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	for _, res := range c.State.Reservations {
		if keys.NamespacedKey(res.Spec.RunRef.Namespace, res.Spec.RunRef.Name) != runKey {
			continue
		}
		if res.Status.State != "Pending" && res.Status.State != "" {
			continue
		}
		released := v1.NewTime(now)
		res.Status.State = "Released"
		res.Status.Reason = "Superseded"
		res.Status.ReleasedAt = &released
		res.Status.CountdownSeconds = nil
		// The reservation is leaving Pending: stop reporting a backlog for
		// it so the gauge does not persist forever (audit finding #21).
		metrics.ClearReservationBacklog(keys.NamespacedKey(res.Namespace, res.Name))
	}
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
	// Every caller either just (re-)admitted the run (checkpoint grace
	// satisfied) or is a no-op on an already-terminal/Running run (where the
	// deadline is already nil); clearing here is safe either way.
	run.Status.CheckpointDeadline = nil
}

// ActivateReservations attempts to start any due reservations in sorted key
// order, invoking the resolver if capacity deficits remain. A reservation
// that fails to activate is recorded on its status and does not block later
// reservations; the collected errors are returned as an aggregate.
func (c *RunController) ActivateReservations(now time.Time) error {
	before := c.snapshotWorld()
	defer c.checkInvariants("RunController.ActivateReservations", before)

	dueKeys := make([]string, 0, len(c.State.Reservations))
	for key := range c.State.Reservations {
		dueKeys = append(dueKeys, key)
	}
	sort.Strings(dueKeys)

	var errs []error
	for _, key := range dueKeys {
		reservation, ok := c.State.Reservations[key]
		if !ok {
			// Superseded or rescheduled by an earlier activation this pass.
			continue
		}
		if reservation.Status.State != "Pending" && reservation.Status.State != "" {
			continue
		}
		if reservation.Spec.EarliestStart.Time.After(now) {
			// Not due yet: refresh the countdown gauge instead of leaving it
			// frozen at whatever value it had when the reservation was
			// created (audit finding #21).
			c.refreshReservationBacklog(key, reservation, now)
			continue
		}
		if err := c.activateReservation(key, reservation, now); err != nil {
			reservation.Status.Reason = fmt.Sprintf("activation failed: %v", err)
			errs = append(errs, fmt.Errorf("reservation %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// refreshReservationBacklog recomputes a still-pending reservation's backlog
// gauge from its EarliestStart against the current clock, so the value
// tracks the shrinking countdown instead of freezing at the value it had
// when the reservation was created (audit finding #21).
func (c *RunController) refreshReservationBacklog(key string, reservation *v1.Reservation, now time.Time) {
	runKey := keys.NamespacedKey(reservation.Spec.RunRef.Namespace, reservation.Spec.RunRef.Name)
	run := c.State.Runs[runKey]
	if run == nil {
		return
	}
	delta := reservation.Spec.EarliestStart.Time.Sub(now).Seconds()
	if delta < 0 {
		delta = 0
	}
	metrics.SetReservationBacklog(key, run.Spec.Resources.GPUType, delta)
}

func (c *RunController) activateReservation(key string, reservation *v1.Reservation, now time.Time) error {
	runKey := keys.NamespacedKey(reservation.Spec.RunRef.Namespace, reservation.Spec.RunRef.Name)
	run, ok := c.State.Runs[runKey]
	if !ok {
		// The run is gone, so the reservation can never activate; fail it
		// terminally rather than retrying every tick.
		reservation.Status.State = "Failed"
		reservation.Status.CountdownSeconds = nil
		metrics.ClearReservationBacklog(key)
		return fmt.Errorf("run %s referenced by reservation %s not found", runKey, key)
	}
	if run.Status.Phase == RunPhaseRunning || run.Status.Phase == RunPhaseComplete || run.Status.Phase == RunPhaseFailed {
		// Running/Completed: the run already bound (capacity freed before
		// the activation tick); materializing again would double-spend the
		// budget. Failed: a resolver-killed run must be resubmitted
		// explicitly — a stale reservation must not resurrect it (ruling
		// 2026-07-02).
		c.releasePendingReservations(run, now)
		return nil
	}

	if allocated := baseGangGPUsForRun(runKey, c.State.Leases); allocated > 0 {
		// Open leases for a run that never reached Running mean an earlier
		// evaluation materialized them but its run-status write was lost
		// (the bridge's apply is not atomic — R28). Finish that activation
		// instead of planning again against the run's own capacity, which
		// would report the run's own leases as a deficit forever.
		expected := minRunnableGPUs(run)
		if allocated < expected {
			// Half-applied and still short of the gang's width (R2): top the
			// missing members back up and HOLD the reservation. Releasing it now
			// would hand the reserved capacity to another run while this one is
			// still assembling onto it. The run's own adoption path releases the
			// reservation once the last lease lands.
			created := c.topUpActiveGang(run)
			run.Status.Phase = RunPhasePending
			run.Status.Message = fmt.Sprintf("assembling gang: %d/%d GPUs held (awaiting the jobtree scheduler)", allocated, expected)
			run.Status.Width = summarizeRunWidth(run, c.State.Leases)
			if run.Status.Width != nil {
				run.Status.Width.Pending = fmt.Sprintf("Assemble to %d", expected)
			}
			run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))
			if created > 0 {
				c.emit(run, EventTypeWarning, "GangIncomplete", run.Status.Message)
			}
			return nil
		}
		activated := v1.NewTime(now)
		reservation.Status.State = "Released"
		reservation.Status.Reason = "Activated"
		reservation.Status.ActivatedAt = &activated
		reservation.Status.ReleasedAt = &activated
		reservation.Status.CountdownSeconds = nil
		metrics.ClearReservationBacklog(key)
		run.Status.Phase = RunPhaseRunning
		run.Status.Message = fmt.Sprintf("adopted %d GPUs of open leases from an earlier activation", allocated)
		run.Status.PendingReservation = nil
		run.Status.EarliestStart = nil
		run.Status.CheckpointDeadline = nil
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))
		return nil
	}

	// Already activated on a prior tick (funded path): intent pods are out for
	// the plugin to place, but its leases have not been adopted yet. Do NOT
	// re-run the resolver — that would evict more capacity every tick while the
	// freed room waits for the plugin's async bind. Wait; the run's adoption
	// path releases the reservation once the leases appear.
	if runHasActivePods(c.State.Pods, run) {
		metrics.ClearReservationBacklog(key)
		return nil
	}

	usage := computeUsage(c.State.Leases, now)
	snapshot, err := topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
	if err != nil {
		return err
	}

	ev := c.evaluate(now)
	inventory := cover.NewInventory(ev)

	// opportunistic tracks whether funding fell back to the promised-but-
	// unfunded escape hatch (opportunisticCoverPlan). A funded activation emits
	// intent pods for the plugin to mint; an opportunistic one is the one narrow
	// mint the controller still performs, since the plugin's funding gate would
	// refuse an unfunded gang (cascade-plan.md §1).
	opportunistic := false

	location := reservation.Spec.IntendedSlice.Domain
	request := cover.Request{
		Owner:       run.Spec.Owner,
		Flavor:      run.Spec.Resources.GPUType,
		Quantity:    run.Spec.Resources.TotalGPUs,
		Location:    location,
		Now:         now,
		Admitted:    run.CreationTimestamp.Time,
		RunKey:      keys.NamespacedKey(run.Namespace, run.Name),
		AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow,
	}
	if run.Spec.Funding != nil {
		request.Sponsors = append(request.Sponsors, run.Spec.Funding.Sponsors...)
		if run.Spec.Funding.MaxBorrowGPUs != nil {
			remaining := *run.Spec.Funding.MaxBorrowGPUs - borrowedGPUsForRun(ev, run)
			if remaining < 0 {
				remaining = 0
			}
			request.MaxBorrowGPUs = &remaining
		}
	}

	// Mirror the classification used when the reservation was created:
	// every pack failure except an invalid request is a capacity-class
	// problem, and every cover failure except an invalid request is a
	// budget/policy-class problem.
	plan, err := planPlacement(run, snapshot)
	var packErr *pack.PlanError
	if err != nil {
		if pe, ok := err.(*pack.PlanError); ok {
			if pe.Reason == pack.FailureReasonInvalidRequest {
				return err
			}
			packErr = pe
		} else {
			return err
		}
	}

	spareTotal := expectedSpareTotal(run, &plan)
	request.Quantity = run.Spec.Resources.TotalGPUs + spareTotal
	coverPlan, err := inventory.Plan(request)
	var coverPlanErr *cover.PlanError
	if err != nil {
		if ce, ok := err.(*cover.PlanError); ok {
			if ce.Reason == cover.FailureReasonInvalidRequest {
				return err
			}
			coverPlanErr = ce
		} else {
			return err
		}
	}

	if packErr != nil || coverPlanErr != nil {
		scope := reservation.Spec.IntendedSlice.Domain
		if scope == nil {
			scope = deriveLocation(plan)
		}
		totalNeeded := int(run.Spec.Resources.TotalGPUs + spareTotal)
		deficit := 0
		if packErr != nil {
			deficit = computeDeficit(snapshot, scope, totalNeeded)
			if deficit <= 0 {
				// Free GPUs exist in scope yet placement still failed
				// (fragmentation): clear the full request width.
				deficit = totalNeeded
			}
		}
		if deficit > 0 {
			if coverPlanErr != nil {
				// Physical deficit AND unfundable demand: sharpened R7 —
				// reclaim (unfunded first cut included) is justified only by
				// funded demand, and the lottery over funded runs only by a
				// funded claim's capacity need. The promise-made admission
				// waits for space to free instead of cutting anyone.
				reservation.Status.Reason = "waiting for capacity: demand is currently unfunded and reclaims nothing"
				return nil
			}
			// Capacity shortfall for fundable demand: the resolver reclaims
			// in the consolidated order — unfunded first, then spares,
			// shrink, and the lottery. The prospective claim is ranked into
			// the evaluation so family work it recalls is in the unfunded
			// pool, not the funded lottery. Preemption stays capacity-only
			// (R7): the budget half of a shortfall is handled below by
			// opportunistic admission, never by cutting funded work.
			leases := activeLeasePointers(c.State.Leases)
			resInput := resolver.Input{
				Deficit:    deficit,
				Flavor:     run.Spec.Resources.GPUType,
				Scope:      scope,
				SeedSource: reservation.Name,
				Now:        now,
				Nodes:      c.State.Nodes,
				Leases:     leases,
				Runs:       c.State.Runs,
				Evaluation: c.hypotheticalEvaluation(run, coverPlan, now),
			}
			resolution, err := resolver.Resolve(resInput)
			if err != nil {
				return err
			}
			c.applyResolution(resolution, now)

			// rebuild the world after resolution
			usage = computeUsage(c.State.Leases, now)
			snapshot, err = topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
			if err != nil {
				return err
			}
			plan, err = planPlacement(run, snapshot)
			if err != nil {
				return err
			}
			ev = c.evaluate(now)
			inventory = cover.NewInventory(ev)
			spareTotal = expectedSpareTotal(run, &plan)
			request.Quantity = run.Spec.Resources.TotalGPUs + spareTotal
			coverPlan, err = inventory.Plan(request)
			if err != nil {
				if ce, ok := err.(*cover.PlanError); ok && ce.Reason != cover.FailureReasonInvalidRequest {
					plan, ok := c.opportunisticCoverPlan(run, reservation, ev, request.Quantity)
					if !ok {
						return c.failReservationNoEnvelope(reservation, runKey)
					}
					coverPlan = plan
					opportunistic = true
				} else {
					return err
				}
			}
		} else {
			// Pure budget shortfall at activation: the reservation is a
			// promise already made, so the run starts anyway and the
			// evaluation classes it — usually unfunded, re-funded by
			// arithmetic when quota returns (R14 demote-not-kill; this
			// dissolves the 2026-07-02 known edge where funded victims
			// could die for a run that never started).
			plan, ok := c.opportunisticCoverPlan(run, reservation, ev, request.Quantity)
			if !ok {
				// No envelope at all to attribute the work to and nothing to
				// re-fund from when quota returns (the budget was removed
				// after the reservation was made): unlike an exhausted-but-
				// present envelope, this is terminal (PR #13 finding).
				return c.failReservationNoEnvelope(reservation, runKey)
			}
			coverPlan = plan
			opportunistic = true
		}
	}

	activated := v1.NewTime(now)
	if opportunistic {
		// Promised-but-unfunded start: the plugin's Permit funding gate would
		// refuse this gang, so the controller pre-authorizes it instead of
		// minting — it emits intent pods marked lease-reason=Promise carrying
		// the payer provenance opportunisticCoverPlan attributed the demand to
		// (always exactly one segment). The plugin — still the sole committer —
		// skips the funding gate for Promise pods, exactly as for a swap, and
		// mints from that provenance at PreBind; the evaluation then classes
		// the leases — typically Unfunded, re-funded by arithmetic when quota
		// returns (R14 demote-not-kill). The run reaches Running when the
		// adoption path picks the minted leases up, like any other activation.
		c.emitPromisePods(run, plan, coverPlan.Segments[0])

		reservation.Status.State = "Released"
		reservation.Status.Reason = "Activated"
		reservation.Status.ActivatedAt = &activated
		reservation.Status.ReleasedAt = &activated
		reservation.Status.CountdownSeconds = nil
		metrics.ClearReservationBacklog(key)

		run.Status.PendingReservation = nil
		run.Status.EarliestStart = nil
		run.Status.Message = fmt.Sprintf("reservation %s activated (promised; awaiting the jobtree scheduler)", reservation.Name)
		run.Status.Funding = summarizeRunFunding(run, ev)
		c.emit(run, EventTypeNormal, "Activated", run.Status.Message)
		return nil
	}

	// Funded activation: capacity is freed and the demand funds, so emit the
	// run's intent pods for the scheduler plugin to place and mint — exactly as
	// initial admission does. We mint nothing. The reservation's promise has
	// fired (its capacity is freed and its pods are out), so it Releases now; the
	// run reaches Running when the plugin's leases are adopted. The once-per-
	// activation guard above stops the resolver re-firing while that bind lands.
	c.emitIntentPods(run, plan)
	reservation.Status.State = "Released"
	reservation.Status.Reason = "Activated"
	reservation.Status.ActivatedAt = &activated
	reservation.Status.ReleasedAt = &activated
	reservation.Status.CountdownSeconds = nil
	metrics.ClearReservationBacklog(key)

	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
	run.Status.Message = fmt.Sprintf("reservation %s activated; scheduling %d GPUs (awaiting the jobtree scheduler)", reservation.Name, run.Spec.Resources.TotalGPUs)
	run.Status.Funding = summarizeRunFunding(run, ev)
	c.emit(run, EventTypeNormal, "Activated", run.Status.Message)
	return nil
}

// runHasActivePods reports whether the run already has unscheduled Active intent
// pods emitted (funded reservation activation is idempotent per tick).
func runHasActivePods(pods []binder.PodManifest, run *v1.Run) bool {
	for i := range pods {
		p := &pods[i]
		if p.Namespace == run.Namespace && p.Labels[binder.LabelRunName] == run.Name && p.Labels[binder.LabelRunRole] == binder.RoleActive {
			return true
		}
	}
	return false
}

// runHasPromisePods reports whether the run's emitted intent pods are a
// promised (pre-authorized, lease-reason=Promise) activation awaiting the
// scheduler (R3). Such a run must not re-enter admission planning: its cover
// is expected to fail until quota returns, and the plugin binds its pods
// regardless (they skip the funding gate).
func runHasPromisePods(pods []binder.PodManifest, run *v1.Run) bool {
	for i := range pods {
		p := &pods[i]
		if p.Namespace == run.Namespace && p.Labels[binder.LabelRunName] == run.Name &&
			p.Annotations[binder.AnnotationLeaseReason] == binder.LeaseReasonPromise {
			return true
		}
	}
	return false
}

// opportunisticCoverPlan funds an activation the budget cannot cover: the
// whole demand is attributed to the reservation's intended envelope as a
// recorded fact, and the evaluation decides what that fact is worth —
// typically Unfunded now, re-funded by arithmetic when quota returns. The
// envelope is resolved among the owner's budgets. It returns ok=false when
// no such envelope exists (the budget was removed): there is nothing to
// attribute the work to and nothing to re-fund from, so the caller must not
// admit — the reservation fails terminally instead.
func (c *RunController) opportunisticCoverPlan(run *v1.Run, reservation *v1.Reservation, ev *funding.Evaluation, quantity int32) (cover.Plan, bool) {
	segment := cover.Segment{Owner: run.Spec.Owner, Quantity: quantity}
	found := false
	for _, acct := range ev.Envelopes() {
		if acct.Owner != run.Spec.Owner {
			continue
		}
		// Prefer the reservation's intended envelope; fall back to any
		// envelope the owner still has of the run's flavor so an exhausted
		// (but present) budget coasts rather than failing.
		if acct.Key.Envelope == reservation.Spec.PayingEnvelope {
			segment.Namespace = acct.Key.Namespace
			segment.BudgetName = acct.Key.Budget
			segment.EnvelopeName = acct.Key.Envelope
			found = true
			break
		}
		if !found && acct.Spec.Flavor == run.Spec.Resources.GPUType {
			segment.Namespace = acct.Key.Namespace
			segment.BudgetName = acct.Key.Budget
			segment.EnvelopeName = acct.Key.Envelope
			found = true
		}
	}
	if !found {
		return cover.Plan{}, false
	}
	return cover.Plan{Segments: []cover.Segment{segment}}, true
}

// failReservationNoEnvelope marks a reservation terminally failed because
// the run's owner has no envelope of the run's flavor: opportunistic
// admission needs a real payer to attribute unfunded hours to and to
// re-fund from, so with none the promise cannot be kept.
func (c *RunController) failReservationNoEnvelope(reservation *v1.Reservation, runKey string) error {
	reservation.Status.State = "Failed"
	reservation.Status.CountdownSeconds = nil
	metrics.ClearReservationBacklog(keys.NamespacedKey(reservation.Namespace, reservation.Name))
	return fmt.Errorf("run %s has no envelope to fund reservation %s (budget removed)", runKey, reservation.Name)
}

// SAFETY-CRITICAL SEMANTICS.
//
// This node-failure / spare-swap seam is modeled in:
//   - specs/NodeFailure.tla
//   - specs/NodeFailure.md
//
// If you change the semantics of HandleNodeFailure, failGroupWithoutSpare,
// runPhaseTracker, or closeRunLeases, update that spec and rerun:
//   - make node-failure-spec-check
//   - make node-failure-spec-counterexamples
//
// The path-scoped CI rail is .github/workflows/node-failure-spec.yaml.
//
// HandleNodeFailure performs a spare swap when a node fails.
// ErrNoLeaseOnNode reports that no lease of any role named the node, so its
// failure needs no response. Callers must test it with errors.Is — the reason it
// is a typed sentinel is that the node reconciler used to string-match the old
// message, and that swallow is what hid R25's leaked spare lease.
var ErrNoLeaseOnNode = errors.New("jobtree: no lease found on node")

// HandleNodeFailure closes every lease that names a failed node, and swaps each
// affected active slice onto a held spare where one exists.
//
// The CALLER decides what "failed" means, and that is not incidental: ClusterState
// carries topology.SourceNode{Name, Labels, GPUs}, and the bridge already drops
// unusable nodes when it loads (nodeUsable, bridge.go:178), so the engine cannot
// tell a cordoned node from a dead one. controllers/kube.nodeFailed makes that
// judgement against the real corev1.Node. A bare `kubectl cordon` is not a failure
// (R21) — acting on one starts a second copy of a rank that is still running.
func (c *RunController) HandleNodeFailure(nodeName string, now time.Time) error {
	if now.IsZero() {
		now = c.Clock.Now()
	}

	// Deferred, so the oracle sees the state only on RETURN. Mid-method this
	// function legitimately violates INV-TERMINAL-PRESENT: it marks a run Failed
	// inside the lease loop and sweeps that run's remaining leases only after the
	// loop, precisely so the outcome cannot depend on lease order.
	before := c.snapshotWorld()
	defer c.checkInvariants("RunController.HandleNodeFailure", before)

	handled := false
	phases := runPhaseTracker{}

	// Pass 1 — spare leases on the failed node (R25).
	//
	// A spare that sits ON the failed node is a casualty, not a swap target: its
	// slots died with the node, and emitSwapPod targets the spare's own node, so
	// swapping onto it would place the replacement on the corpse. Close it here,
	// before pass 2, so findSpareLease cannot hand one back.
	//
	// The old loop skipped every spare BEFORE testing the node, so a node holding
	// nothing but a spare matched nothing, returned "no active lease found", and
	// the caller swallowed that by string-match — leaving an open, budget-charging
	// lease pointed at a node that no longer exists.
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		if lease.Status.Closed || lease.Spec.Slice.Role != binder.RoleSpare {
			continue
		}
		if !leaseContainsNode(lease, nodeName) {
			continue
		}
		CloseLease(lease, "NodeFailure", now)
		handled = true
		// A run whose only stake on the node was a held spare loses its fault
		// tolerance here, silently, without any change to its phase. Say so.
		spareKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		if run := c.State.Runs[spareKey]; run != nil {
			c.emit(run, EventTypeWarning, "SpareLostToNodeFailure", fmt.Sprintf(
				"the spare held for group %s died with node %s; the group is no longer covered",
				leaseGroupIndex(lease), nodeName))
		}
	}

	// The funding derivation, taken ONCE, against the world as it was BEFORE this
	// failure was handled. That is a SEMANTIC CHOICE, not an optimisation, and the
	// comment that used to sit here implied otherwise.
	//
	// Pass 2 closes leases as it runs — the swap closes the spare and the failed
	// active, failGroupWithoutSpare closes a dead group's, reclaimSquatter closes a
	// squatter's — and closing a lease frees budget. So a squatter this evaluation
	// calls Unfunded can derive a FUNDED class in the world the failure itself
	// produces, once a co-tenant's lease dies with the node. An adversarial panel
	// reproduced exactly that (finding F6).
	//
	// We reclaim it anyway, deliberately:
	//
	//   - Re-evaluating per iteration would make the outcome depend on the order of
	//     c.State.Leases. That is playbook class 3, the defect this file has now
	//     shipped seven times. One evaluation per failure is what makes pass 2
	//     commutative.
	//   - The funded status such a squatter acquires is an artifact of a co-tenant's
	//     death, and it evaporates the moment that run re-admits. Sparing it means
	//     failing a genuinely funded victim to protect work that is about to be
	//     unfunded again — which is precisely what a judge's mutation of the
	//     "refresh ev" fix did.
	//   - The error is one-directional and safe. A stale ev can only make us reclaim
	//     work that is about to be funded; it can never make us evict a lease this
	//     evaluation calls funded, because that path declines the swap.
	//   - The reclaimed run is demoted, not killed, and re-admits when capacity
	//     returns (quota-semantics R14). The state is legal and self-correcting.
	//
	// Class is derived, never stored (quota-semantics Decision 3), so an evaluation
	// is the only way to know. Tracked as task #54. Do not "fix" this without a
	// ruling recorded in docs/project/quota-semantics.md.
	ev := c.evaluate(now)

	// Pass 2 — active leases on the failed node.
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		if lease.Status.Closed || lease.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		if !leaseContainsNode(lease, nodeName) {
			continue
		}
		handled = true
		runKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		run := c.State.Runs[runKey]
		groupIndex := leaseGroupIndex(lease)
		if run == nil {
			CloseLease(lease, "NodeFailure", now)
			continue
		}

		spareLease, spareIdx := findSpareLease(c.State.Leases, runKey, groupIndex)
		if spareLease == nil {
			c.failGroupWithoutSpare(run, runKey, lease, nil, nodeName, now, phases)
			continue
		}

		// R22 — reclaim at SLOT granularity, and never at another run's expense.
		//
		// The swap re-places onto the spare's own node#ordinal slots. Only a lease
		// occupying those exact slots is a genuine conflict; a run merely sharing
		// the node on different GPUs is not, and the old sweep closed it anyway
		// because leasesOverlap compared nodeFromSlot and discarded the ordinal. In
		// the correct common case this sweep now closes nothing.
		//
		// A surviving exact-slot conflict is either a bug or a deliberate
		// oversubscription. Either way, evicting another run's funded work is the
		// resolver's call — it knows the funding classes — not this function's. So
		// we decline the spare rather than steal the slots, and fall through to the
		// no-spare path. The run re-admits through the normal, funding-aware route.
		spareSlots := buildSlotSet(spareLease.Spec.Slice.Nodes)
		crossRunConflict := false
		for j := range c.State.Leases {
			if j == spareIdx || j == i {
				continue
			}
			other := &c.State.Leases[j]
			if other.Status.Closed || !leaseOccupiesSlots(other, spareSlots) {
				continue
			}
			otherKey := keys.NamespacedKey(other.Spec.RunRef.Namespace, other.Spec.RunRef.Name)
			if otherKey == runKey {
				// Our own stale lease on the spare's slots. Clearing it must move
				// BOTH planes: close the lease AND drop the pod holding the slot, or
				// the pod is stranded on the swap target — it keeps its real
				// nvidia.com/gpu claim, and the swap pod (hard-pinned to this node)
				// can never bind, while INV-LEASE-HAS-POD stays green because it is
				// coarse (the run still has other pods). That is the same half-plane
				// reclaimSquatter was built to avoid, on the same-run door.
				//
				// The pod plane addresses NODES, not slots (chunk-local ordinals mean
				// two same-run leases can share a slot STRING on physically distinct
				// GPUs — this is how the branch is even reachable). So dropping the
				// node's pods is only safe when no OTHER open same-run lease shares
				// the node; if one does, its container is a sibling's and not ours to
				// delete. Fail closed exactly as reclaimSquatter does: drop the pod
				// only when provably safe, else close the lease alone (no worse than
				// before, never evicting a sibling's live rank).
				nodes := nodesOfSlots(other.Spec.Slice.Nodes)
				podDropSafe := true
				for _, d := range c.openRunLeasesOnNodes(runKey, nodes) {
					if d != other && d != spareLease && d != lease {
						podDropSafe = false
						break
					}
				}
				CloseLease(other, "ReclaimedBySpare", now)
				if podDropSafe {
					c.removeRunPodsOnNodes(runKey, nodes)
				}
				continue
			}
			// Another run holds the exact slots the swap needs. Whether we may take
			// them is a FUNDING question, and the derivation answers it: unfunded
			// work is opportunistic and reclaimable by definition (quota-semantics:
			// it runs on capacity nobody paid for). Anything else is somebody's
			// funded capacity, and evicting it belongs to the resolver, which ranks
			// by class — not to this function. Decline the swap instead, and let the
			// run re-admit through the normal, funding-aware route.
			//
			// The old sweep closed the victim unconditionally, and compared NODES
			// rather than slots, so a swap for run A silently killed run B's funded
			// work merely for sharing a machine.
			class, classified := ev.Class(other)
			if classified && class == funding.ClassUnfunded {
				// phases, not a bare write: this victim may ALSO be a casualty of the
				// same node failure, in which case failGroupWithoutSpare has its own
				// verdict on its phase and the worst one must win, whatever order the
				// leases happen to sit in.
				//
				// It can also REFUSE. Deleting the squatter's container means deleting
				// every container it holds on that node — the pod plane cannot address
				// a slot — and if one of those backs a FUNDED lease of the same run we
				// would be evicting paid-for work. Decline, as for any funded conflict.
				if c.reclaimSquatter(other, otherKey, now, phases, ev) {
					continue
				}
				crossRunConflict = true
				c.emit(run, EventTypeWarning, "SwapSlotConflict", fmt.Sprintf(
					"spare slots for group %s are squatted by %s, whose funded work shares the node; "+
						"declining the swap rather than evicting it", groupIndex, otherKey))
				continue
			}
			crossRunConflict = true
			c.emit(run, EventTypeWarning, "SwapSlotConflict", fmt.Sprintf(
				"spare slots for group %s are held by funded run %s; declining the swap rather than evicting it",
				groupIndex, otherKey))
		}
		if crossRunConflict {
			// Pass the spare so it is RELEASED, not stranded. Declining the swap
			// while leaving the spare's lease open charged the run's budget for GPUs
			// it could never use, forever: nothing downstream closes a terminal
			// run's leases.
			c.failGroupWithoutSpare(run, runKey, lease, spareLease, nodeName, now, phases)
			continue
		}

		spareNodes := leaseNodeNames(spareLease)
		CloseLease(spareLease, "Swap", now)
		CloseLease(lease, "NodeFailure", now)
		// Free the held spare's pod on the reclaimed node so the bridge deletes it
		// and the swap pod (which hard-targets that node) can bind there.
		c.removeSparePodOnNodes(run, leaseGroupIndex(spareLease), spareNodes)
		// Re-emit the group's pod as a SWAP onto the reclaimed spare node,
		// stamped with the spare's funding provenance; the scheduler plugin binds
		// it there (required node affinity) and mints the Swap lease from that
		// provenance — the sole committer. We mint nothing here. It inherits the
		// failed member's rendezvous hostname (R9 9A-1), so nodeName (the failed
		// node) is passed to find the member being replaced.
		c.emitSwapPod(run, groupIndex, spareLease, nodeName, now)
		msg := fmt.Sprintf("group %s swapping to spare after node %s failure", groupIndex, nodeName)
		// Running is the mildest outcome: it must not overwrite a sibling group's
		// Failed or Pending, whichever order the leases happen to be in.
		phases.apply(run, runKey, RunPhaseRunning, msg)
		c.emit(run, EventTypeNormal, "NodeFailureSwap", msg)
	}

	// A run this call drove to Failed is dead as a gang: its surviving slices on
	// healthy nodes are no longer part of anything. Release them, or the ledger
	// charges its budget for GPUs nobody is using and keeps them marked occupied
	// until someone deletes the Run object. Swept after the loop so the outcome does
	// not depend on the order of c.State.Leases.
	for runKey, phase := range phases {
		if phase != RunPhaseFailed {
			continue
		}
		run := c.State.Runs[runKey]
		if run == nil {
			continue
		}
		if closed := c.releaseRun(run, "RunFailed", now); closed > 0 {
			c.emit(run, EventTypeWarning, "LeasesReleased", fmt.Sprintf(
				"released %d open lease(s) held by the failed run", closed))
		}
	}

	if !handled {
		return fmt.Errorf("%w: %s", ErrNoLeaseOnNode, nodeName)
	}
	return nil
}

// runPhaseSeverity orders the phases HandleNodeFailure can write, worst last. A run
// may hold several active leases on the failed node; each group reaches its own
// verdict, but they share one Status.Phase.
var runPhaseSeverity = map[string]int{
	RunPhaseRunning: 1,
	RunPhasePending: 2,
	RunPhaseFailed:  3,
}

// runPhaseTracker keeps the WORST phase written for each run across a single
// HandleNodeFailure call.
//
// Writing Status.Phase per group made the last group processed win, and the order
// is the order of c.State.Leases. A run with one group swapping to a spare and
// another group dead without coverage reported whichever came last — so a run with
// a dead, uncovered rank could report Running. The phase now does not depend on
// slice order.
type runPhaseTracker map[string]string

// apply writes the phase only if it is worse than what this call has written
// already, and reports whether it did.
func (t runPhaseTracker) apply(run *v1.Run, runKey, phase, msg string) bool {
	if seen, ok := t[runKey]; ok && runPhaseSeverity[seen] >= runPhaseSeverity[phase] {
		return false
	}
	t[runKey] = phase
	run.Status.Phase = phase
	run.Status.Message = msg
	return true
}

// failGroupWithoutSpare closes the failed slice and either parks the run inside
// its checkpoint grace or fails it. Shared by the "no spare exists" and the "the
// spare's slots belong to another run" paths, which want identical treatment: the
// group lost its capacity and nothing safe can replace it.
//
// spareLease is the spare this group holds and will NOT be using — nil on the
// no-spare path, non-nil when the swap was declined because another funded run
// holds the spare's exact slots.
func (c *RunController) failGroupWithoutSpare(run *v1.Run, runKey string, lease, spareLease *v1.Lease, nodeName string, now time.Time, phases runPhaseTracker) {
	CloseLease(lease, "NodeFailure", now)

	// The group is not runnable, so the spare it was holding can never cover it.
	// Leaving that lease open charges the run's budget for GPUs it will never use
	// and keeps the ledger marking them occupied, blocking re-admission — the
	// immortal-lease class R25 exists to kill, reached by a new door. Nothing else
	// closes it: failRun only edited status, completeRun runs on success, and
	// adoption skips terminal runs.
	if spareLease != nil {
		CloseLease(spareLease, "SwapDeclined", now)
		// Drop the spare's held pod too, exactly as the accepted-swap path does.
		// Closing the lease alone is a half-plane action: the ledger frees the
		// GPUs while the placeholder container keeps holding them. Left running,
		// it strands GPUs the ledger calls free, and if the run re-admits inside
		// its checkpoint grace the pod lingers forever, invisible to every
		// invariant (INV-LEASE-HAS-POD fires only at zero pods). Adversarial
		// review 2026-07-10, c74e0ef: reproduced holding 2 GPUs 20h post-recovery.
		c.removeSparePodOnNodes(run, leaseGroupIndex(spareLease), leaseNodeNames(spareLease))
		c.emit(run, EventTypeWarning, "SpareReleased", fmt.Sprintf(
			"released the spare held for group %s: the group lost node %s and cannot use it",
			leaseGroupIndex(lease), nodeName))
	}

	if grace := checkpointGrace(run); grace > 0 {
		// spec.runtime.checkpoint says the workload can be safely requeued: park
		// the run Pending (it re-enters the normal cover/pack/bind or reservation
		// path on the next reconcile) instead of failing immediately, but only for
		// up to the checkpoint grace window (enforced in Reconcile).
		deadline := v1.NewTime(now.Add(grace))
		msg := fmt.Sprintf("node %s failed without spare coverage; checkpoint grace until %s", nodeName, deadline.Time.UTC().Format(time.RFC3339))
		if phases.apply(run, runKey, RunPhasePending, msg) {
			run.Status.CheckpointDeadline = &deadline
		}
		c.emit(run, EventTypeWarning, "NodeFailureCheckpointGrace", msg)
		return
	}

	msg := fmt.Sprintf("node %s failed without spare coverage", nodeName)
	if phases.apply(run, runKey, RunPhaseFailed, msg) {
		// Terminal, so a grace deadline is meaningless. The run's remaining leases
		// are swept after the lease loop finishes, not here: closing them mid-loop
		// would let the outcome depend on the order of c.State.Leases, which is the
		// bug runPhaseTracker exists to remove.
		run.Status.CheckpointDeadline = nil
	}
	c.emit(run, EventTypeWarning, "NodeFailureNoSpare", msg)
}

// reclaimSquatter evicts an unfunded run from the slots a swap needs — in BOTH
// planes, because a lease and a pod are two different claims on one GPU.
//
// Closing the lease alone was a ledger-only eviction. The victim's container kept
// running on the exact node#ordinal the swap then targeted, so the ledger said
// the slot was free while the kubelet disagreed; and the victim itself went on
// reporting Running forever, holding nothing, a zombie no reconcile would ever
// visit. (Its phase was never touched, so the width invariant caught it.)
//
// Demote, do not kill: unfunded work is reclaimable by definition, and
// quota-semantics R14 says it requeues and re-admits when capacity returns. A run
// that keeps enough width to run stays Running.
//
// It takes the phases tracker, and that is not decoration. THE VICTIM MAY ALSO BE
// A VICTIM OF THE NODE FAILURE ITSELF: an unfunded run can hold a rank on the
// dead node AND squat on the spare's slots, an oversubscription the engine
// explicitly tolerates (see the comment above buildSlotSet). Then two writers
// reach for its phase in one pass — failGroupWithoutSpare writes Failed through
// the tracker, this writes Pending — and without the fold the winner is whichever
// lease `c.State.Leases` happened to store last. Failed is terminal, so in half
// the orderings a reclaimable run was permanently killed instead of requeued.
//
// The tracker's lattice (Running < Pending < Failed) makes the fold commutative:
// the worst verdict wins, whatever the order. This is the SEVENTH defect of this
// exact class on this path; see docs/project/history-run-phase-writers.md, which
// is also the argument for why this parameter is passed rather than remembered.
// It returns false when the eviction cannot be made two-plane-consistent without
// touching FUNDED work. The caller must then decline the swap, exactly as it does
// for a funded conflict: choosing between funded runs belongs to pkg/resolver,
// which ranks by class.
func (c *RunController) reclaimSquatter(lease *v1.Lease, victimKey string, now time.Time, phases runPhaseTracker, ev *funding.Evaluation) bool {
	// THE POD PLANE CANNOT ADDRESS A SLOT.
	//
	// The conflict is detected at node#ordinal granularity (leaseOccupiesSlots), but
	// binder.PodManifest carries NodeName and GPUs, not ordinals, and every pod the
	// controller emits is labelled group "0" (emitCohortPods hardcodes it), while
	// every lease the plugin mints has no group at all. So "delete the pods of this
	// lease" is not expressible. See R28b.
	//
	// The previous shape asked removePodsForGroups for group "0" and got THE WHOLE
	// RUN's pods, while closing exactly ONE lease. The victim was left holding open
	// leases with no containers behind them — billing forever, invisible to the
	// width invariant, which counts leases and not pods. Reproduced: an unfunded run
	// Running with an open lease and zero pods, still open after 20 simulated hours.
	//
	// So evict at the finest granularity the pod plane CAN express — the node — and
	// close exactly the leases whose containers are being deleted. Both planes drop
	// together, or neither does.
	nodes := nodesOfSlots(lease.Spec.Slice.Nodes)
	doomed := c.openRunLeasesOnNodes(victimKey, nodes)

	// FAIL CLOSED. A run's funding class is per-LEASE, not per-run: one run can hold
	// an Owned lease and an Unfunded lease at once. Deleting the victim's pods on
	// this node would kill the containers of every lease above — and if any of them
	// is funded, that is somebody's paid-for work, and evicting it is the resolver's
	// call, not this function's. Decline the swap instead.
	//
	// (ev is the derivation taken once before pass 2, and pass 2 closes leases as it
	// runs, so a squatter this calls Unfunded may be funded in the post-closure world.
	// That is a known low-severity staleness, tracked separately; it errs toward
	// reclaiming, never toward evicting a lease this evaluation calls funded.)
	for _, other := range doomed {
		if other == lease {
			continue
		}
		class, classified := ev.Class(other)
		if !classified || class != funding.ClassUnfunded {
			return false
		}
	}

	for _, other := range doomed {
		CloseLease(other, "ReclaimedBySpare", now)
	}

	victim := c.State.Runs[victimKey]
	if victim == nil {
		// The Run object is already gone; its leases are the only thing left, and
		// they are now closed. cleanupDeletedRun sweeps its pods.
		return true
	}
	c.removeRunPodsOnNodes(victimKey, nodes)

	if runnableGPUsForRun(victimKey, c.State.Leases) >= minRunnableGPUs(victim) {
		// Still wide enough to make progress: an unfunded malleable run that lost
		// one group above its minimum keeps running, and the leases it still holds
		// still have their containers — we deleted only the pods on the nodes whose
		// leases we closed. Nothing to record in the tracker: Running is the
		// lattice's bottom, and a later Failed from the node-failure path must still
		// be able to override it.
		victim.Status.Width = summarizeRunWidth(victim, c.State.Leases)
		return true
	}

	msg := "reclaimed by a node-failure swap; will re-admit when capacity allows"
	if phases.apply(victim, victimKey, RunPhasePending, msg) {
		// Only clear the reservation pointers when this verdict actually stuck. If
		// a worse verdict already landed for this run in this pass, the run is
		// terminal and the sweep owns it.
		victim.Status.PendingReservation = nil
		victim.Status.EarliestStart = nil
	}
	victim.Status.Width = summarizeRunWidth(victim, c.State.Leases)
	c.emit(victim, EventTypeWarning, "ReclaimedBySpare", fmt.Sprintf(
		"unfunded work on slots %v was reclaimed to cover a node failure", lease.Spec.Slice.Nodes))
	return true
}

// openRunLeasesOnNodes lists the run's OPEN leases — any role — holding at least one
// slot on any of the given nodes. These are exactly the leases whose containers a
// node-scoped pod removal would delete, which is why the two must move together.
func (c *RunController) openRunLeasesOnNodes(runKey string, nodes map[string]int) []*v1.Lease {
	var out []*v1.Lease
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		if lease.Status.Closed {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		for _, slot := range lease.Spec.Slice.Nodes {
			if nodes[nodeFromSlot(slot)] > 0 {
				out = append(out, lease)
				break
			}
		}
	}
	return out
}

// removeRunPodsOnNodes deletes the run's pods on the given nodes. Node granularity is
// forced: a PodManifest names a machine, not a node#ordinal, so a container cannot be
// matched to the exact slot it occupies. R28b is what makes finer possible.
func (c *RunController) removeRunPodsOnNodes(runKey string, nodes map[string]int) {
	kept := c.State.Pods[:0]
	for _, pod := range c.State.Pods {
		sameRun := keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName]) == runKey
		if sameRun && pod.NodeName != "" && nodes[pod.NodeName] > 0 {
			continue
		}
		kept = append(kept, pod)
	}
	c.State.Pods = kept
}

// removeAllRunPods deletes every pod of the run. A TERMINAL run's containers must
// stop: releaseRun hands its GPUs back to the ledger, and Bridge.apply deletes
// exactly the pods absent from State.Pods — so a pod left behind keeps holding a GPU
// the ledger now calls free. The engine then plans new work onto it and the
// kube-scheduler can never bind it.
//
// This is completeRun's cull, which the success path has always done, applied to the
// failure paths, which never did.
func (c *RunController) removeAllRunPods(run *v1.Run) {
	kept := c.State.Pods[:0]
	for _, pod := range c.State.Pods {
		if pod.Namespace == run.Namespace && pod.Labels[binder.LabelRunName] == run.Name {
			continue
		}
		kept = append(kept, pod)
	}
	c.State.Pods = kept
}

// releaseRun retires a TERMINAL run in BOTH planes: it closes every lease the run
// still holds, and deletes every pod. It reports how many leases it closed.
//
// Both, or neither. A terminal run holding an open lease charges its budget forever
// and keeps the ledger marking its GPUs occupied — the immortal-lease class. And a
// terminal run whose LEASES are closed but whose PODS survive is the same lie told
// backwards: Bridge.apply deletes exactly the pods absent from State.Pods, so a pod
// left behind keeps holding a GPU the ledger has just handed back. The engine then
// plans new work onto that GPU and the kube-scheduler can never bind it.
//
// The success path has always done both (completeRun closes the leases and culls the
// pods). Every failure path did only half, for as long as this file has existed. This
// function is why a fourth caller cannot repeat that: the pod cull is not a step a
// caller may forget, it is inside the only function that closes a terminal run's
// leases.
//
// CALL IT ONLY ON A TERMINAL RUN. The checkpoint-grace window is a deliberate,
// bounded half-plane state: failGroupWithoutSpare parks the run Pending with a
// CheckpointDeadline and leaves its containers running SO THEY CAN WRITE A
// CHECKPOINT. It closes the dead group's lease and calls nothing here.
func (c *RunController) releaseRun(run *v1.Run, reason string, now time.Time) int {
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	closed := 0
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		if lease.Status.Closed {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		CloseLease(lease, reason, now)
		closed++
	}
	// The other plane. Not optional, not a caller's responsibility.
	c.removeAllRunPods(run)
	return closed
}

func ptrString(value string) *string { return &value }

func ptrInt64(value int64) *int64 { return &value }

func (c *RunController) planReservation(run *v1.Run, snapshot *topology.Snapshot, packPlan *pack.Plan, packErr *pack.PlanError, coverErr *cover.PlanError, ev *funding.Evaluation, request cover.Request, now time.Time) error {
	if ev == nil {
		ev = c.evaluate(now)
	}

	var planPtr *pack.Plan
	if packPlan != nil {
		copy := *packPlan
		planPtr = &copy
	}
	spareTotal := expectedSpareTotal(run, planPtr)
	request.Quantity = run.Spec.Resources.TotalGPUs + spareTotal
	forecastStart := time.Now()
	forecastResult, err := forecast.Plan(forecast.Input{
		Run:          run,
		Now:          now,
		Snapshot:     snapshot,
		PackPlan:     planPtr,
		PackErr:      packErr,
		CoverErr:     coverErr,
		CoverRequest: request,
		Evaluation:   ev,
		Runs:         c.State.Runs,
	})
	// forecast.Plan is an inline library call made from this reconcile path,
	// not a separate "forecast controller" (audit finding #24) — the metric
	// is observed at the one call site that exists.
	metrics.ObserveForecastLatency(run.Spec.Resources.GPUType, time.Since(forecastStart))
	if err != nil {
		run.Status.Phase = RunPhasePending
		run.Status.Message = fmt.Sprintf("reservation planning failed: %v", err)
		return nil
	}
	if len(forecastResult.IntendedSlice.Nodes) == 0 && len(forecastResult.IntendedSlice.Domain) == 0 {
		// No matching domain exists (e.g. zero nodes of the flavor). A
		// reservation without nodes or a domain fails its own validating
		// webhook, so park plainly until matching capacity appears.
		run.Status.Phase = RunPhasePending
		run.Status.Message = fmt.Sprintf("no capacity in any matching domain: %s", forecastResult.Reason)
		return nil
	}

	reservationName := fmt.Sprintf("%s-res-%d", run.Name, now.Unix())
	earliest := v1.NewTime(forecastResult.EarliestStart)
	status := v1.ReservationStatus{
		State:    "Pending",
		Reason:   forecastResult.Reason,
		Forecast: forecastResult.Forecast.DeepCopy(),
	}
	if forecastResult.EarliestStart.After(now) {
		seconds := int64(forecastResult.EarliestStart.Sub(now).Seconds())
		status.CountdownSeconds = ptrInt64(seconds)
	}

	reservation := &v1.Reservation{
		ObjectMeta: v1.ObjectMeta{
			Name:      reservationName,
			Namespace: run.Namespace,
		},
		Spec: v1.ReservationSpec{
			RunRef: v1.RunReference{
				Name:      run.Name,
				Namespace: run.Namespace,
			},
			IntendedSlice:  forecastResult.IntendedSlice,
			PayingEnvelope: forecastResult.PayingEnvelope,
			EarliestStart:  earliest,
		},
		Status: status,
	}

	key := keys.NamespacedKey(run.Namespace, reservationName)
	for existingKey, existing := range c.State.Reservations {
		if existing.Spec.RunRef.Name == run.Name && existing.Spec.RunRef.Namespace == run.Namespace {
			delete(c.State.Reservations, existingKey)
			// The superseded reservation is leaving Pending; its backlog
			// series must not linger under the old key (audit finding #21).
			metrics.ClearReservationBacklog(existingKey)
		}
	}
	c.State.Reservations[key] = reservation

	// Keyed by reservation (not just flavor) so concurrent reservations of
	// the same flavor do not collapse onto one series. The run reconciler's
	// resync requeue (and the reservation reconciler's countdown poll) keep
	// calling this while the reservation stays Pending, so the value tracks
	// the shrinking countdown instead of freezing at creation time.
	if !forecastResult.EarliestStart.IsZero() {
		delta := forecastResult.EarliestStart.Sub(now).Seconds()
		if delta < 0 {
			delta = 0
		}
		metrics.SetReservationBacklog(key, run.Spec.Resources.GPUType, delta)
	}

	run.Status.Phase = RunPhasePending
	run.Status.Message = fmt.Sprintf("reservation %s scheduled for %s (deficit %d GPUs)", reservationName, forecastResult.EarliestStart.Format(time.RFC3339), forecastResult.Forecast.DeficitGPUs)
	run.Status.PendingReservation = ptrString(reservationName)
	run.Status.EarliestStart = &earliest
	c.emit(run, EventTypeNormal, "Reserved", run.Status.Message)
	return nil
}

func (c *RunController) applyResolution(result resolver.Result, now time.Time) {
	if len(result.Actions) == 0 {
		return
	}
	closedGroups := map[string]struct{}{}
	affectedRuns := map[string]struct{}{}
	// Runs hit only by the unfunded-first phase were healthy work bumped
	// for funded demand: they requeue instead of failing terminally, and
	// re-admit when quota or capacity returns (demote-not-kill, R14). Any
	// funded cut on the same run keeps the terminal ruling.
	reclaimedOnly := map[string]bool{}
	for _, action := range result.Actions {
		lease := action.Lease
		if lease == nil || lease.Status.Closed {
			continue
		}
		CloseLease(lease, action.Reason, now)
		// Counted here, not during planning: a resolver result can be
		// discarded (e.g. the lottery errors), and only applied actions
		// should show up in metrics.
		metrics.IncResolverAction(string(action.Kind))
		// action.Reason carries the attested lottery/reclaim seed for
		// ActionLottery/ActionReclaimUnfunded (e.g. "RandomPreempt(0x...)");
		// emitting it as a real Warning event makes the seed discoverable
		// without grepping controller logs (audit finding #23).
		c.emit(action.Run, EventTypeWarning, "ResolverAction", fmt.Sprintf("%s: %s (lease %s)", action.Kind, action.Reason, lease.Name))
		runKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		if _, seen := affectedRuns[runKey]; !seen {
			reclaimedOnly[runKey] = true
		}
		if action.Kind != resolver.ActionReclaimUnfunded {
			reclaimedOnly[runKey] = false
		}
		affectedRuns[runKey] = struct{}{}
		if action.GroupIndex != "" {
			key := runKey + "::" + action.GroupIndex
			closedGroups[key] = struct{}{}
		}
	}

	if len(closedGroups) > 0 {
		var pods []binder.PodManifest
		for _, pod := range c.State.Pods {
			runName := pod.Labels[binder.LabelRunName]
			group := pod.Labels[binder.LabelGroupIndex]
			key := keys.NamespacedKey(pod.Namespace, runName) + "::" + group
			if _, ok := closedGroups[key]; ok {
				continue
			}
			pods = append(pods, pod)
		}
		c.State.Pods = pods
	}

	for runKey := range affectedRuns {
		run := c.State.Runs[runKey]
		if run == nil {
			continue
		}
		// The old test here was `activeGPUsForRun(...) > 0`, which said a fixed-width
		// gang missing half its groups is "Running". It is not: "start together or
		// not at all". The lottery cuts group-by-group, so a deficit smaller than a
		// fixed run's width cuts a strict subset of its groups — and the run then
		// reported healthy while making no progress and charging a budget for its
		// surviving ranks, forever. Nothing repairs a fixed-width Running run:
		// reconcileElasticRun returns immediately for a non-malleable run, and
		// topUpActiveGang is only reachable pre-Running.
		//
		// The width is TOTAL runnable width — base gang plus grow — not base-gang
		// width. Measuring the base gang here was a reaper, and it was caught by
		// reading the resolver rather than by any test: the lottery's own guard
		// (pkg/resolver/resolver.go:503) permits a cut while `Remaining - grp.GPUs
		// >= MinTotalGPUs`, and `Remaining` counts grow leases. So the resolver may
		// legitimately cut a malleable run's BASE group while its grow ranks still
		// cover the declared minimum. Comparing base width against a total-GPU
		// minimum then failed a run that was, by the resolver's own reckoning, still
		// runnable — and swept its surviving leases.
		//
		// baseGangGPUsForRun remains right for ADOPTION (run_controller.go:203),
		// where a run reconstructing itself from leases after a restart must not
		// reach "width" on grow leases alone. That is a different question: this run
		// already assembled, and 96 live ranks are 96 live ranks whatever funding
		// provenance minted them.
		allocated := runnableGPUsForRun(runKey, c.State.Leases)
		expected := minRunnableGPUs(run)
		switch {
		case allocated >= expected:
			run.Status.Phase = RunPhaseRunning
			run.Status.Message = "shrunk by resolver"
			c.emit(run, EventTypeWarning, "ResolverShrink", run.Status.Message)
		case reclaimedOnly[runKey]:
			run.Status.Phase = RunPhasePending
			run.Status.Message = "reclaimed by funded demand; will re-admit when quota allows"
			run.Status.PendingReservation = nil
			run.Status.EarliestStart = nil
			c.emit(run, EventTypeWarning, "ResolverReclaimed", run.Status.Message)
		case run.Status.CheckpointDeadline != nil && now.Before(run.Status.CheckpointDeadline.Time):
			// The run is inside an unexpired checkpoint-grace window: an earlier
			// capacity loss parked it Pending and promised its containers stay up
			// until the deadline SO THEY CAN WRITE A CHECKPOINT (releaseRun's
			// contract; failGroupWithoutSpare sets the deadline). Reaping it here —
			// the default branch's releaseRun deletes every pod — destroys the very
			// checkpoint the grace exists to save, minutes before the deadline the
			// engine itself set. Honor the deadline, not the phase (adversarial
			// review 2026-07-10, c74e0ef: reproduced ending a funded run 25 minutes
			// early). Hold it parked; Reconcile fails it the moment the deadline
			// passes (run_controller.go:167).
			run.Status.Phase = RunPhasePending
			run.Status.Message = "resolver cut during checkpoint grace; holding containers until the deadline"
			c.emit(run, EventTypeWarning, "ResolverGraceHeld", run.Status.Message)
		default:
			run.Status.Phase = RunPhaseFailed
			run.Status.Message = "ended by resolver"
			// A terminal run holds no open lease. This was the one terminal-failing
			// path that never swept: failRun and HandleNodeFailure both close what
			// the run still holds, and this branch did not. The resolver only sees
			// leases inside the reservation's scope, and a run's spare can lie
			// outside it — so a scoped cut could end a run while its out-of-scope
			// spare stayed open forever, charging its budget and holding healthy
			// GPUs. The immortal-lease class, reached by a third door.
			//
			// Closing them here also returns the capacity the deficit was chasing:
			// a dead gang's surviving ranks were still occupying the ledger.
			if closed := c.releaseRun(run, "RunFailed", now); closed > 0 {
				c.emit(run, EventTypeWarning, "LeasesReleased", fmt.Sprintf(
					"released %d open lease(s) still held by the failed run", closed))
			}
			c.emit(run, EventTypeWarning, "ResolverEnded", run.Status.Message)
		}
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
	}
}

func activeLeasePointers(leases []v1.Lease) []*v1.Lease {
	var result []*v1.Lease
	for i := range leases {
		if leases[i].Status.Closed {
			continue
		}
		result = append(result, &leases[i])
	}
	return result
}

func computeDeficit(snapshot *topology.Snapshot, scope map[string]string, requested int) int {
	if snapshot == nil {
		return requested
	}
	free := totalFreeInScope(snapshot, scope)
	if free >= requested {
		return 0
	}
	return requested - free
}

func totalFreeInScope(snapshot *topology.Snapshot, scope map[string]string) int {
	if len(scope) == 0 {
		return snapshot.TotalFreeGPUs()
	}
	total := 0
	for _, dom := range snapshot.Domains {
		if scope[topology.LabelRegion] != "" && dom.Key.Region != scope[topology.LabelRegion] {
			continue
		}
		if scope[topology.LabelCluster] != "" && dom.Key.Cluster != scope[topology.LabelCluster] {
			continue
		}
		if scope[topology.LabelFabricDomain] != "" && dom.Key.Fabric != scope[topology.LabelFabricDomain] {
			continue
		}
		total += dom.FreeGPUs()
	}
	return total
}

func computeUsage(leases []v1.Lease, now time.Time) map[string]int {
	usage := make(map[string]int)
	for _, lease := range leases {
		if lease.Status.Closed {
			continue
		}
		if lease.Spec.Interval.End != nil && !now.Before(lease.Spec.Interval.End.Time) {
			continue
		}
		for _, id := range lease.Spec.Slice.Nodes {
			node := id
			if idx := strings.IndexRune(id, '#'); idx >= 0 {
				node = id[:idx]
			}
			usage[node]++
		}
	}
	return usage
}

func planPlacement(run *v1.Run, snapshot *topology.Snapshot) (pack.Plan, error) {
	allowSpread := run.Spec.AllowCrossGroupSpread()
	var groupSize *int
	if run.Spec.Locality != nil && run.Spec.Locality.GroupGPUs != nil {
		value := int(*run.Spec.Locality.GroupGPUs)
		groupSize = &value
	}
	spares := 0
	if run.Spec.Spares != nil {
		spares = int(*run.Spec.Spares)
	}
	req := pack.Request{
		Flavor:                run.Spec.Resources.GPUType,
		TotalGPUs:             int(run.Spec.Resources.TotalGPUs),
		GroupGPUs:             groupSize,
		AllowCrossGroupSpread: allowSpread,
		SparesPerGroup:        spares,
	}
	return pack.Planner(snapshot, req)
}

func deriveLocation(plan pack.Plan) map[string]string {
	if len(plan.Groups) == 0 {
		return nil
	}
	first := plan.Groups[0].Domain
	return map[string]string{
		topology.LabelRegion:       first.Region,
		topology.LabelCluster:      first.Cluster,
		topology.LabelFabricDomain: first.Fabric,
	}
}

func expectedSpareTotal(run *v1.Run, plan *pack.Plan) int32 {
	if plan != nil {
		return int32(plan.TotalSpares)
	}
	if run.Spec.Spares == nil || *run.Spec.Spares <= 0 {
		return 0
	}
	spares := *run.Spec.Spares
	groups := int32(1)
	if run.Spec.Locality != nil && run.Spec.Locality.GroupGPUs != nil && *run.Spec.Locality.GroupGPUs > 0 {
		groupSize := *run.Spec.Locality.GroupGPUs
		if groupSize > 0 {
			total := run.Spec.Resources.TotalGPUs
			groups = (total + groupSize - 1) / groupSize
		}
	}
	return spares * groups
}

// emitIntentPods ensures the run's Active gang has its full width of
// unscheduled workload pods in the world for the scheduler plugin to place and
// fund. It mints no leases (the plugin does). Pods are uniform — gpusPerPod
// each, width of them — so the plugin's per-pod funding attribution holds; the
// pack plan's nodes are attached as an advisory placement hint (the bridge
// renders them as soft nodeAffinity, never a nodeName pin). Idempotent: it only
// tops up the pods that do not yet exist.
func (c *RunController) emitIntentPods(run *v1.Run, packPlan pack.Plan) int {
	gpusPerPod, width := intentPodShape(run)
	created := c.emitCohortPods(run, packPlacements(packPlan, gpusPerPod, 0), gpusPerPod, width, "0", "Start", nil)
	created += c.emitSparePods(run, packPlan, gpusPerPod, "Start", nil)
	return created
}

// emitPromisePods emits a promised-but-unfunded activation's intent gang (R3):
// identical to emitIntentPods, but every pod — actives and spares — is marked
// lease-reason=Promise and carries the payer provenance the activation
// attributed the demand to, so the plugin mints from it without the funding
// gate (which would refuse this gang; that refusal is why the promise exists).
// Idempotent like emitIntentPods: it only tops up to the declared widths.
func (c *RunController) emitPromisePods(run *v1.Run, packPlan pack.Plan, payer cover.Segment) int {
	extra := map[string]string{
		binder.AnnotationPayerOwner:     payer.Owner,
		binder.AnnotationPayerNamespace: payer.Namespace,
		binder.AnnotationPayerBudget:    payer.BudgetName,
		binder.AnnotationPayerEnvelope:  payer.EnvelopeName,
	}
	gpusPerPod, width := intentPodShape(run)
	created := c.emitCohortPods(run, packPlacements(packPlan, gpusPerPod, 0), gpusPerPod, width, "0", binder.LeaseReasonPromise, extra)
	created += c.emitSparePods(run, packPlan, gpusPerPod, binder.LeaseReasonPromise, extra)
	return created
}

// emitSparePods emits the run's declared spares as held, unscheduled RoleSpare
// intent pods (gpusPerPod each) advisory-targeted at pack's spare placements.
// The base gang's cover already funds active+spares, so the plugin binds these
// and mints RoleSpare leases from the leftover payers — real, funded standby
// capacity that sits out the active width and that a node-failure swap lands on.
// Idempotent: only tops up to the declared spare-pod count.
func (c *RunController) emitSparePods(run *v1.Run, packPlan pack.Plan, gpusPerPod int, reason string, extra map[string]string) int {
	if gpusPerPod <= 0 || packPlan.TotalSpares <= 0 || packPlan.TotalSpares%gpusPerPod != 0 {
		return 0
	}
	// A spare consumed by a node-failure swap (its lease closed with reason
	// "Swap") is not re-provisioned: that funded capacity now carries the
	// swapped-in active work. Only genuinely-missing spares are topped up.
	count := packPlan.TotalSpares/gpusPerPod - c.consumedSpareCount(run)
	if count <= 0 {
		return 0
	}
	placements := sparePlacements(packPlan, gpusPerPod)
	// Presence is keyed by pod NAME, not a raw count of surviving spares — the same
	// fix emitCohortPods carries for the active cohort (R2), which the spare path
	// never got (#91). A count-based scan re-emits indices `existing..count`, so a
	// spare that went missing OUT OF ORDER (an eviction, or removeSparePodOnNodes
	// closing the wrong sibling when two spares share a group/node) makes it rebuild
	// an index a survivor still owns: two pods, then two leases, of one name — which
	// CheckTransition reads as a closure-reason rewrite (INV-CLOSED-MONOTONE). Fill
	// only the genuinely-missing indices instead.
	present := make(map[string]bool, count)
	for i := range c.State.Pods {
		p := &c.State.Pods[i]
		if p.Namespace == run.Namespace && p.Labels[binder.LabelRunName] == run.Name &&
			p.Labels[binder.LabelRunRole] == binder.RoleSpare {
			present[p.Name] = true
		}
	}
	created := 0
	for i := 0; i < count; i++ {
		name := sparePodName(run, i)
		if present[name] {
			continue
		}
		node := ""
		// A spare belongs to the group it covers: that is how findSpareLease pairs it
		// with the rank it will replace.
		group := "0"
		if len(placements) > 0 {
			node = placements[i%len(placements)].Node
			if i < len(placements) {
				group = placements[i].Group
			}
		}
		created++
		annotations := map[string]string{
			binder.AnnotationExpectedWidth: strconv.Itoa(count),
			binder.AnnotationLeaseReason:   reason,
		}
		for k, v := range extra {
			annotations[k] = v
		}
		c.State.Pods = append(c.State.Pods, binder.PodManifest{
			Namespace: run.Namespace,
			Name:      name,
			NodeName:  node, // advisory only; the bridge turns this into soft affinity
			GPUs:      gpusPerPod,
			Labels: map[string]string{
				binder.LabelRunName:    run.Name,
				binder.LabelRunRole:    binder.RoleSpare,
				binder.LabelGroupIndex: group,
			},
			Annotations: annotations,
		})
	}
	return created
}

// sparePodName is the deterministic name of a run's i-th spare intent pod — the
// spare counterpart of cohortPodName. Presence in emitSparePods is keyed by it so a
// top-up never rebuilds an index a surviving spare still owns (#91).
func sparePodName(run *v1.Run, i int) string {
	return fmt.Sprintf("%s-spare-%d", run.Name, i)
}

// consumedSpareCount is how many of a run's spares have been promoted by a
// node-failure swap (their RoleSpare lease closed with reason "Swap"). Those
// spare slots are gone for good — the swap re-used their funded capacity — so
// emitSparePods must not re-provision them.
func (c *RunController) consumedSpareCount(run *v1.Run) int {
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	n := 0
	for i := range c.State.Leases {
		l := &c.State.Leases[i]
		if l.Status.Closed && l.Spec.Slice.Role == binder.RoleSpare && l.Status.ClosureReason == "Swap" &&
			keys.NamespacedKey(l.Spec.RunRef.Namespace, l.Spec.RunRef.Name) == runKey {
			n++
		}
	}
	return n
}

// removeSparePodOnNodes drops the run's held-spare pod manifest for group, bound
// to one of nodes (the reclaimed spare node), so the bridge deletes the real spare
// Pod, freeing its GPU for the swap pod that hard-targets that node. Removes at
// most one spare (the swap consumes one held slot).
//
// group matters: R28b (e681a96) stamps every spare pod with its placement group,
// and two groups' spares can legally co-locate on one machine (pkg/pack assigns
// spare domains per-group with no cross-group exclusion). Matching on node alone
// deleted whichever spare the API listed first — stranding the swapping group's own
// pod and freeing a sibling group's, a class-5 identity coarsening. Key on the
// group the caller is actually retiring.
func (c *RunController) removeSparePodOnNodes(run *v1.Run, group string, nodes []string) {
	want := buildNodeSet(nodes)
	for i := range c.State.Pods {
		p := &c.State.Pods[i]
		if p.Namespace != run.Namespace || p.Labels[binder.LabelRunName] != run.Name ||
			p.Labels[binder.LabelRunRole] != binder.RoleSpare {
			continue
		}
		if podGroupIndex(p) != group {
			continue
		}
		if p.NodeName == "" || want[p.NodeName] == 0 {
			continue
		}
		c.State.Pods = append(c.State.Pods[:i], c.State.Pods[i+1:]...)
		return
	}
}

// cohortPodName is the deterministic name of a cohort's i-th Active intent pod.
// The base gang ("0") carries no cohort marker, so its pods keep the original
// name shape.
func cohortPodName(run *v1.Run, cohort string, i int) string {
	if cohort == "0" {
		return fmt.Sprintf("%s-active-%d", run.Name, i)
	}
	return fmt.Sprintf("%s-c%s-active-%d", run.Name, cohort, i)
}

// emitCohortPods tops up one cohort of a run to `count` uniform, unscheduled
// Active intent pods (gpusPerPod each) for the scheduler plugin to place and
// fund. Cohort "0" is the base gang; each elastic-grow step uses "1","2",… so
// the plugin gangs and funds that delta separately from the base. Idempotent per
// cohort; returns how many pods it created this pass. extra (may be nil) adds
// per-pod annotations beyond the standard set — the Promise path uses it to
// carry the payer provenance (R3).
//
// Presence is keyed by pod NAME, not by a count of surviving pods: R2's gang
// top-up re-emits a member that went missing from the MIDDLE of the cohort, and
// a count-based scan would then rebuild the wrong index (creating a duplicate of
// the last pod while the missing one never returns).
func (c *RunController) emitCohortPods(run *v1.Run, placements []podPlacement, gpusPerPod, count int, cohort, reason string, extra map[string]string) int {
	if gpusPerPod <= 0 || count <= 0 {
		return 0
	}
	present := make(map[string]bool, count)
	for i := range c.State.Pods {
		p := &c.State.Pods[i]
		if p.Namespace == run.Namespace && p.Labels[binder.LabelRunName] == run.Name &&
			p.Labels[binder.LabelRunRole] == binder.RoleActive && podCohort(p) == cohort {
			present[p.Name] = true
		}
	}
	countStr := strconv.Itoa(count)
	created := 0
	for i := 0; i < count; i++ {
		name := cohortPodName(run, cohort, i)
		if present[name] {
			continue
		}
		node := ""
		// The group is AUTHORITATIVE and the node is a hint, so they are resolved
		// differently. When the plan is in hand the pod takes its planned group; when
		// it is not — topUpActiveGang re-emitting a rank whose plan is long gone — the
		// group is recomputed from the Run spec, which is what the packer laid out.
		group := groupIndexForPodIndex(run, i, gpusPerPod)
		if len(placements) > 0 {
			node = placements[i%len(placements)].Node
			if i < len(placements) {
				group = placements[i].Group
			}
		}
		created++
		annotations := map[string]string{
			binder.AnnotationExpectedWidth: countStr,
			binder.AnnotationLeaseReason:   reason,
		}
		for k, v := range extra {
			annotations[k] = v
		}
		if cohort != "0" {
			annotations[binder.AnnotationCohort] = cohort
		}
		c.State.Pods = append(c.State.Pods, binder.PodManifest{
			Namespace: run.Namespace,
			Name:      name,
			NodeName:  node, // advisory only; the bridge turns this into soft affinity
			GPUs:      gpusPerPod,
			Labels: map[string]string{
				binder.LabelRunName:    run.Name,
				binder.LabelRunRole:    binder.RoleActive,
				binder.LabelGroupIndex: group,
			},
			Annotations: annotations,
		})
	}
	return created
}

// expectedActiveGPUs is the active width, in GPUs, that the run's base intent
// gang was emitted at. CRD validation pins Width×GPUsPerPod == TotalGPUs for a
// roled run, so this is TotalGPUs either way — but it is derived from the same
// shape emitCohortPods emits, so the two can never drift.
func expectedActiveGPUs(run *v1.Run) int {
	gpusPerPod, width := intentPodShape(run)
	return gpusPerPod * width
}

// minRunnableGPUs is the smallest active width at which the run is a real,
// running gang rather than a half-assembled one.
//
// A fixed-width run must hold all of it: "start together or not at all". A
// MALLEABLE run may legitimately run anywhere in [MinTotalGPUs, MaxTotalGPUs] —
// that is quota-semantics' demote-not-kill — so a malleable run that shrank to a
// width at or above its minimum is running, not broken, and the elastic loop
// grows it back. Holding malleable runs to TotalGPUs here would terminally fail
// (via the node-failure checkpoint grace) a run that merely lost a group and was
// happily continuing at reduced width.
func minRunnableGPUs(run *v1.Run) int {
	if run.Spec.Malleable != nil && run.Spec.Malleable.MinTotalGPUs > 0 {
		return int(run.Spec.Malleable.MinTotalGPUs)
	}
	return expectedActiveGPUs(run)
}

// leaseGroupIndex is the ONE way to ask which placement group a Lease belongs to.
//
// It reads the label and does not invent one. Before R28b the sole committer stamped
// no group index at all, so this defaulted to "0" — and because every pod was ALSO
// stamped "0", the default looked harmless while it silently merged every group of
// every run into one. The resolver's lottery cut whole runs instead of groups, the
// elastic loop shrank in whole-run units, and a reclaim that asked for "the pods of
// this group" got the pods of the entire run.
//
// The default is gone. A persisted Lease with no group index is a bug in the mint,
// and pkg/invariant's INV-GROUP-STAMPED fails the build rather than papering over it.
func leaseGroupIndex(lease *v1.Lease) string {
	if lease.Labels == nil {
		return ""
	}
	return lease.Labels[binder.LabelGroupIndex]
}

// podGroupIndex is the spare/active pod counterpart of leaseGroupIndex: the
// placement group R28b stamps onto every emitted pod. Missing reads as "" (the
// same convention leaseGroupIndex uses), so a lease and its pod compare equal.
func podGroupIndex(p *binder.PodManifest) string {
	if p.Labels == nil {
		return ""
	}
	return p.Labels[binder.LabelGroupIndex]
}

// runnableGPUsForRun is the run's TOTAL live width: every open, non-spare lease,
// whatever minted it. It answers "how many ranks are running right now", which is
// the question a run's PHASE turns on.
//
// This is not baseGangGPUsForRun, and confusing the two is a reaper in both
// directions. Use this one to judge whether a run that has already assembled is
// still runnable; use baseGangGPUsForRun to judge whether a run is assembling.
//
//	runnable  — phase decisions (applyResolution) and the width invariant
//	base gang — adoption, which must not reach width on grow leases alone
//
// The resolver agrees with this one: its lottery guard (pkg/resolver/resolver.go:503)
// permits a cut while `Remaining - grp.GPUs >= MinTotalGPUs`, and `Remaining`
// counts grow leases. Judging the phase on base width alone therefore failed
// malleable runs the resolver had deliberately left runnable.
func runnableGPUsForRun(runKey string, leases []v1.Lease) int {
	total := 0
	for i := range leases {
		lease := &leases[i]
		if lease.Status.Closed || lease.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		total += len(lease.Spec.Slice.Nodes)
	}
	return total
}

// baseGangGPUsForRun is the run's BASE-gang active width: open, non-spare leases
// that are not elastic-grow width. It is what adoption compares against
// expectedActiveGPUs, and the distinction is load-bearing: grow leases are width
// added on top of the base gang, so counting them lets a run whose base gang
// never assembled (or whose base nodes all failed) reach full "width" on grow
// leases alone and adopt to Running holding zero base-gang GPUs.
//
// A Lease records no cohort — Spec.Reason is the only durable signal separating a
// grow lease from a base one. Swap and Promise leases DO count: each stands in for
// a base-gang member. (Reconstructing the cohort itself is R2 pt3's job.)
func baseGangGPUsForRun(runKey string, leases []v1.Lease) int {
	total := 0
	for i := range leases {
		lease := &leases[i]
		if lease.Status.Closed || lease.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		if lease.Spec.Reason == binder.LeaseReasonGrow {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		total += len(lease.Spec.Slice.Nodes)
	}
	return total
}

// gangProvenance recovers the lease-reason and payer annotations the run's base
// gang was emitted under, so a topped-up member is minted on the same terms as
// its siblings. A Promise gang (R3) is pre-authorized and skips the plugin's
// funding gate: re-emitting one of its members as a plain "Start" pod would send
// it into a gate that is expected to refuse it, wedging the run for good.
//
// A surviving sibling pod is the authority; if every pod is gone, the open
// leases the plugin already minted carry the same provenance durably.
func (c *RunController) gangProvenance(run *v1.Run) (string, map[string]string) {
	payer := func(owner, namespace, budget, envelope string) map[string]string {
		// namespace is intentionally NOT required: it is empty on legacy leases/pods
		// minted before the field existed, and an empty payer-namespace keys the same
		// legacy envelope, so their attribution is preserved rather than dropped.
		if owner == "" || budget == "" || envelope == "" {
			return nil
		}
		return map[string]string{
			binder.AnnotationPayerOwner:     owner,
			binder.AnnotationPayerNamespace: namespace,
			binder.AnnotationPayerBudget:    budget,
			binder.AnnotationPayerEnvelope:  envelope,
		}
	}
	for i := range c.State.Pods {
		p := &c.State.Pods[i]
		if p.Namespace != run.Namespace || p.Labels[binder.LabelRunName] != run.Name {
			continue
		}
		if p.Labels[binder.LabelRunRole] != binder.RoleActive || podCohort(p) != "0" {
			continue
		}
		if p.Annotations[binder.AnnotationLeaseReason] == binder.LeaseReasonPromise {
			return binder.LeaseReasonPromise, payer(
				p.Annotations[binder.AnnotationPayerOwner],
				p.Annotations[binder.AnnotationPayerNamespace],
				p.Annotations[binder.AnnotationPayerBudget],
				p.Annotations[binder.AnnotationPayerEnvelope],
			)
		}
	}
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		if lease.Status.Closed || lease.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		if lease.Spec.Reason == binder.LeaseReasonPromise {
			return binder.LeaseReasonPromise, payer(lease.Spec.Owner, lease.Spec.PaidByBudgetNamespace, lease.Spec.PaidByBudget, lease.Spec.PaidByEnvelope)
		}
	}
	return "Start", nil
}

// topUpActiveGang re-creates whichever of the base gang's Active intent pods have
// gone missing, on the same terms the gang was emitted under. Idempotent: it
// creates nothing when every member's pod object is still present (the common
// case — a member that is merely unbound still has its pod, and the plugin's
// committed-count accounting re-admits it). Returns how many pods it created.
func (c *RunController) topUpActiveGang(run *v1.Run) int {
	gpusPerPod, width := intentPodShape(run)
	reason, extra := c.gangProvenance(run)
	return c.emitCohortPods(run, nil, gpusPerPod, width, "0", reason, extra)
}

// podCohort returns a pod manifest's cohort ("0" for the base gang).
func podCohort(p *binder.PodManifest) string {
	if c := p.Annotations[binder.AnnotationCohort]; c != "" {
		return c
	}
	return "0"
}

// nextCohortForRun is the next elastic-grow cohort number for a run (base is 0).
func nextCohortForRun(pods []binder.PodManifest, run *v1.Run) int {
	maxC := 0
	for i := range pods {
		p := &pods[i]
		if p.Namespace == run.Namespace && p.Labels[binder.LabelRunName] == run.Name {
			if n, err := strconv.Atoi(podCohort(p)); err == nil && n > maxC {
				maxC = n
			}
		}
	}
	return maxC + 1
}

// emitSwapPod re-emits a failed group's pod as a node-failure SWAP: one
// unscheduled pod hard-targeted (via the swap-node annotation) at the reclaimed
// spare node, carrying the spare's funding provenance (owner/budget/envelope) so
// the plugin mints the Swap lease from it without re-funding. The controller
// mints nothing.
func (c *RunController) emitSwapPod(run *v1.Run, groupIndex string, spareLease *v1.Lease, failedNode string, now time.Time) {
	slots := spareLease.Spec.Slice.Nodes
	if len(slots) == 0 {
		return
	}
	node := nodeFromSlot(slots[0])
	// Inherit the rendezvous identity of the member that died on the failed node
	// (R9 9A-1), and remove that dead pod so its stale DNS A record does not shadow
	// the replacement. The swap pod keeps a fresh, UNIQUE object name — Bridge.apply
	// diffs pods by name, so reusing the dead pod's name in the same pass would be a
	// no-op collision — but serves the same `<hostname>.<svc>` address, so training
	// ranks re-rendezvous to the same member with no reconfiguration.
	hostname := c.takeOverFailedMember(run, groupIndex, failedNode)
	c.State.Pods = append(c.State.Pods, binder.PodManifest{
		Namespace: run.Namespace,
		Name:      fmt.Sprintf("%s-g%s-swap-%d", run.Name, groupIndex, now.UnixNano()),
		Hostname:  hostname,
		NodeName:  node,
		GPUs:      len(slots),
		Labels: map[string]string{
			binder.LabelRunName:    run.Name,
			binder.LabelGroupIndex: groupIndex,
			binder.LabelRunRole:    binder.RoleActive,
		},
		Annotations: map[string]string{
			binder.AnnotationExpectedWidth:  "1",
			binder.AnnotationLeaseReason:    "Swap",
			binder.AnnotationSwapNode:       node,
			binder.AnnotationPayerOwner:     spareLease.Spec.Owner,
			binder.AnnotationPayerNamespace: spareLease.Spec.PaidByBudgetNamespace,
			binder.AnnotationPayerBudget:    spareLease.Spec.PaidByBudget,
			binder.AnnotationPayerEnvelope:  spareLease.Spec.PaidByEnvelope,
		},
	})
}

// takeOverFailedMember removes the run's Active pod that died on failedNode in group
// and returns its rendezvous hostname for the swap replacement to inherit (R9 9A-1).
// Removing it deletes the dead pod's stale DNS record (Bridge.apply deletes pods
// absent from state.Pods) so it cannot shadow the replacement's `<hostname>.<svc>`.
// Returns "" when no such pod is present — the pod already GC'd with its node — in
// which case the swap takes a fresh identity and that rank re-rendezvous on the new
// address (correct, just not name-stable for that one member).
func (c *RunController) takeOverFailedMember(run *v1.Run, group, failedNode string) string {
	for i := range c.State.Pods {
		p := &c.State.Pods[i]
		if p.Namespace != run.Namespace || p.Labels[binder.LabelRunName] != run.Name {
			continue
		}
		if p.Labels[binder.LabelRunRole] != binder.RoleActive || podGroupIndex(p) != group || p.NodeName != failedNode {
			continue
		}
		hostname := p.Name
		if p.Hostname != "" {
			hostname = p.Hostname
		}
		c.State.Pods = append(c.State.Pods[:i], c.State.Pods[i+1:]...)
		return hostname
	}
	return ""
}

// intentPodShape returns the uniform (gpusPerPod, width) an intent gang emits.
// A roled Run uses its role's GPUsPerPod × Width; a legacy Roles-less Run emits
// one 1-GPU pod per requested GPU (uniform, so every cover segment funds a whole
// number of pods).
func intentPodShape(run *v1.Run) (gpusPerPod, width int) {
	if len(run.Spec.Roles) > 0 {
		r := run.Spec.Roles[0]
		return int(r.GPUsPerPod), int(r.Width)
	}
	return 1, int(run.Spec.Resources.TotalGPUs)
}

// podPlacement is one intent pod's ADVISORY node and its AUTHORITATIVE group.
//
// The node is a hint: the bridge turns it into soft affinity and the scheduler
// decides. The group is not a hint. It names which fast-fabric gang the rank belongs
// to, and the resolver, the elastic loop and the node-failure swap all address work
// by it. Before R28b the packer computed groups and every pod was stamped "0", so
// none of those three could address anything.
type podPlacement struct {
	Node  string
	Group string
}

// groupGPUsFor is the run's declared fast-fabric group size, in GPUs. Unset means
// one group: the whole gang.
func groupGPUsFor(run *v1.Run) int {
	if len(run.Spec.Roles) > 0 && run.Spec.Roles[0].GroupGPUs != nil {
		if g := int(*run.Spec.Roles[0].GroupGPUs); g > 0 {
			return g
		}
	}
	if run.Spec.Locality != nil && run.Spec.Locality.GroupGPUs != nil {
		if g := int(*run.Spec.Locality.GroupGPUs); g > 0 {
			return g
		}
	}
	return expectedActiveGPUs(run)
}

// groupSizesFor is the run's active GPUs laid into fast-fabric groups — the same
// layout the packer produces, because it calls the packer's own pack.DeriveGroups.
// topUpActiveGang re-emits a lost pod long after the plan that placed it is gone,
// and the group it stamps must match the one the packer chose; sharing one function
// is what guarantees it (they used to be two copies — R27 #61).
func groupSizesFor(run *v1.Run) []int {
	total := expectedActiveGPUs(run)
	size := groupGPUsFor(run)
	if total <= 0 || size <= 0 {
		return nil
	}
	// One grouping rule, in pkg/pack. This side used to carry a copy so it could
	// answer without a plan, but pack.DeriveGroups needs no plan either — total and
	// size are all it takes — so the copy was pure drift risk (R27 #61). Delegate.
	return pack.DeriveGroups(total, &size)
}

// groupIndexForPodIndex answers "which group does the i-th pod of the base gang
// belong to?" from the Run spec alone. Pods are emitted in group order, gpusPerPod at
// a time, exactly as the packer lays groups out.
func groupIndexForPodIndex(run *v1.Run, i, gpusPerPod int) string {
	sizes := groupSizesFor(run)
	if len(sizes) == 0 || gpusPerPod <= 0 {
		return "0"
	}
	offset := i * gpusPerPod
	for idx, size := range sizes {
		if offset < size {
			return strconv.Itoa(idx)
		}
		offset -= size
	}
	// More pods than the spec's width: the last group owns the overflow rather than
	// inventing a group the packer never planned.
	return strconv.Itoa(len(sizes) - 1)
}

// nextGroupIndex is one past the highest group index the run currently occupies, in
// EITHER plane. An elastic grow appends new groups; it must not renumber the ones the
// gang is already running on, or a later swap would address the wrong ranks.
func (c *RunController) nextGroupIndex(run *v1.Run) int {
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	highest := -1
	note := func(raw string) {
		if idx, err := strconv.Atoi(raw); err == nil && idx > highest {
			highest = idx
		}
	}
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		if lease.Status.Closed {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) == runKey {
			note(leaseGroupIndex(lease))
		}
	}
	for _, pod := range c.State.Pods {
		if keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName]) == runKey {
			note(pod.Labels[binder.LabelGroupIndex])
		}
	}
	return highest + 1
}

// packPlacements turns a pack.Plan into one podPlacement per intent pod, in the order
// the pods are emitted. groupOffset shifts the plan's group numbering, which an
// elastic grow needs so its new groups sit above the base gang's.
func packPlacements(plan pack.Plan, gpusPerPod, groupOffset int) []podPlacement {
	if gpusPerPod <= 0 {
		return nil
	}
	var out []podPlacement
	for _, g := range plan.Groups {
		group := strconv.Itoa(g.GroupIndex + groupOffset)
		for _, np := range g.NodePlacements {
			n := np.GPUs / gpusPerPod
			if n < 1 {
				n = 1
			}
			for k := 0; k < n; k++ {
				out = append(out, podPlacement{Node: np.Node, Group: group})
			}
		}
	}
	return out
}

// sparePlacements does the same for the spares the packer reserved beside each group.
// A spare belongs to the group it covers: that is how findSpareLease matches them.
func sparePlacements(plan pack.Plan, gpusPerPod int) []podPlacement {
	if gpusPerPod <= 0 {
		return nil
	}
	var out []podPlacement
	for _, g := range plan.Groups {
		group := strconv.Itoa(g.GroupIndex)
		for _, sp := range g.SparePlacements {
			n := sp.GPUs / gpusPerPod
			if n < 1 {
				n = 1
			}
			for k := 0; k < n; k++ {
				out = append(out, podPlacement{Node: sp.Node, Group: group})
			}
		}
	}
	return out
}

func (c *RunController) reconcileElasticRun(run *v1.Run, snapshot *topology.Snapshot, inventory *cover.Inventory, now time.Time) error {
	if run.Spec.Malleable == nil {
		return nil
	}

	width := summarizeRunWidth(run, c.State.Leases)
	desired := width.Desired
	allocated := width.Allocated
	if run.Status.Width != nil {
		run.Status.Width.Pending = ""
	}
	// The elastic-width gauge reflects the CURRENT allocated width every pass.
	// A grow now mints asynchronously (the plugin funds the grow cohort), so the
	// gauge advances when those leases land, not the instant grow is requested.
	metrics.SetElasticWidth(keys.NamespacedKey(run.Namespace, run.Name), float64(allocated))

	if desired > allocated {
		growBy := desired - allocated
		step := run.Spec.Malleable.StepGPUs
		if growBy > step {
			growBy = step
		}
		if growBy <= 0 {
			return nil
		}
		if err := c.growRun(run, snapshot, now, int(growBy)); err != nil {
			if run.Status.Width == nil {
				run.Status.Width = width
			}
			run.Status.Width.Pending = fmt.Sprintf("Grow to %d", desired)
			run.Status.Message = fmt.Sprintf("waiting to grow: %v", err)
			return nil
		}
		newWidth := summarizeRunWidth(run, c.State.Leases)
		run.Status.Width = newWidth
		if newWidth.Allocated < desired {
			run.Status.Width.Pending = fmt.Sprintf("Grow to %d", desired)
		}
		run.Status.Message = fmt.Sprintf("grew to %d GPUs", newWidth.Allocated)
		metrics.IncElasticGrow(run.Spec.Resources.GPUType)
		metrics.SetElasticWidth(keys.NamespacedKey(run.Namespace, run.Name), float64(newWidth.Allocated))
		return nil
	}

	if desired < allocated {
		if err := c.shrinkRun(run, desired, c.evaluate(now), now); err != nil {
			if run.Status.Width == nil {
				run.Status.Width = width
			}
			run.Status.Width.Pending = fmt.Sprintf("Shrink to %d", desired)
			run.Status.Message = fmt.Sprintf("unable to shrink: %v", err)
			return nil
		}
		newWidth := summarizeRunWidth(run, c.State.Leases)
		run.Status.Width = newWidth
		if newWidth.Allocated > desired {
			run.Status.Width.Pending = fmt.Sprintf("Shrink to %d", desired)
		}
		run.Status.Message = fmt.Sprintf("shrunk to %d GPUs", newWidth.Allocated)
		metrics.IncElasticShrink(run.Spec.Resources.GPUType)
		metrics.SetElasticWidth(keys.NamespacedKey(run.Namespace, run.Name), float64(newWidth.Allocated))
	}

	return nil
}

func (c *RunController) growRun(run *v1.Run, snapshot *topology.Snapshot, now time.Time, add int) error {
	if add <= 0 {
		return nil
	}
	gpusPerPod, _ := intentPodShape(run)
	if gpusPerPod <= 0 || add%gpusPerPod != 0 {
		return fmt.Errorf("grow delta %d is not a multiple of gpusPerPod %d", add, gpusPerPod)
	}

	var groupSize *int
	if run.Spec.Locality != nil && run.Spec.Locality.GroupGPUs != nil {
		value := int(*run.Spec.Locality.GroupGPUs)
		groupSize = &value
	}
	// Pack only the delta (no new spares — spares are established at the base)
	// for the advisory placement hint and to confirm the delta can fit now.
	plan, err := pack.Planner(snapshot, pack.Request{
		Flavor:                run.Spec.Resources.GPUType,
		TotalGPUs:             add,
		GroupGPUs:             groupSize,
		AllowCrossGroupSpread: run.Spec.AllowCrossGroupSpread(),
		SparesPerGroup:        0,
	})
	if err != nil {
		return err
	}

	// Emit the delta as a NEW cohort of unscheduled intent pods. The scheduler
	// plugin gangs and funds that cohort's delta incrementally against the live
	// ledger (which already holds the base leases) and mints "Grow" leases; the
	// run's width grows from those leases. The controller mints nothing.
	cohort := strconv.Itoa(nextCohortForRun(c.State.Pods, run))
	c.emitCohortPods(run, packPlacements(plan, gpusPerPod, c.nextGroupIndex(run)), gpusPerPod, add/gpusPerPod, cohort, binder.LeaseReasonGrow, nil)
	return nil
}

func (c *RunController) shrinkRun(run *v1.Run, target int32, ev *funding.Evaluation, now time.Time) error {
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	groups := collectElasticGroups(runKey, c.State.Leases, ev)
	if len(groups) == 0 {
		return fmt.Errorf("no active groups to shrink")
	}

	width := summarizeRunWidth(run, c.State.Leases)
	current := width.Allocated
	if target >= current {
		return nil
	}

	var ordered []*elasticGroup
	for _, grp := range groups {
		if grp.ActiveGPUs == 0 {
			continue
		}
		ordered = append(ordered, grp)
	}
	if len(ordered) == 0 {
		return fmt.Errorf("no active groups to shrink")
	}

	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].NonOwnedGPUs == ordered[j].NonOwnedGPUs {
			return ordered[i].Index > ordered[j].Index
		}
		return ordered[i].NonOwnedGPUs > ordered[j].NonOwnedGPUs
	})

	freed := int32(0)
	removed := make(map[string]struct{})
	for _, grp := range ordered {
		if current-freed <= target {
			break
		}
		if current-freed-int32(grp.ActiveGPUs) < target {
			continue
		}
		for _, lease := range grp.Active {
			CloseLease(lease, "Shrink", now)
		}
		for _, lease := range grp.Spares {
			CloseLease(lease, "Shrink", now)
		}
		freed += int32(grp.ActiveGPUs)
		removed[strconv.Itoa(grp.Index)] = struct{}{}
	}

	// Both planes drop together. The pods of any group whose leases we closed
	// must go with them, even when we FALL SHORT of the exact target: returning
	// the shortfall error before removing them stranded the closed groups' pods —
	// the ledger freed the GPUs (leases closed) while the containers kept holding
	// them. That is the same half-plane class as the SwapDeclined fix, a different
	// door. Remove first, then report the shortfall.
	if len(removed) > 0 {
		c.removePodsForGroups(runKey, removed)
	}

	if current-freed > target {
		return fmt.Errorf("insufficient groups available to reach target width")
	}
	return nil
}

func summarizeRunWidth(run *v1.Run, leases []v1.Lease) *v1.RunWidthStatus {
	if run == nil {
		return nil
	}
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	allocated := int32(0)
	for i := range leases {
		lease := &leases[i]
		if lease.Status.Closed {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		if lease.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		allocated += int32(len(lease.Spec.Slice.Nodes))
	}

	status := &v1.RunWidthStatus{Allocated: allocated}
	if run.Spec.Malleable != nil {
		status.Min = run.Spec.Malleable.MinTotalGPUs
		status.Max = run.Spec.Malleable.MaxTotalGPUs
		status.Desired = run.Spec.Malleable.Desired()
	} else {
		total := run.Spec.Resources.TotalGPUs
		status.Min = total
		status.Max = total
		status.Desired = total
	}
	return status
}

// summarizeRunFunding surfaces the derived four-class breakdown for humans
// and dashboards. Status is a cache, never an authority: nothing in the
// control path reads it back (quota-semantics.md Decision 3).
func summarizeRunFunding(run *v1.Run, ev *funding.Evaluation) *v1.RunFundingStatus {
	if run == nil || ev == nil {
		return nil
	}
	acct := ev.Run(keys.NamespacedKey(run.Namespace, run.Name))
	if acct == nil {
		return nil
	}
	status := &v1.RunFundingStatus{
		OwnedGPUs:        acct.GPUs[funding.ClassOwned],
		OwnedGPUHours:    acct.GPUHours[funding.ClassOwned],
		SharedGPUs:       acct.GPUs[funding.ClassShared],
		SharedGPUHours:   acct.GPUHours[funding.ClassShared],
		BorrowedGPUs:     acct.GPUs[funding.ClassBorrowed],
		BorrowedGPUHours: acct.GPUHours[funding.ClassBorrowed],
		UnfundedGPUs:     acct.GPUs[funding.ClassUnfunded],
		UnfundedGPUHours: acct.GPUHours[funding.ClassUnfunded],
	}
	if len(acct.Lenders) > 0 || len(acct.LenderHours) > 0 {
		owners := make(map[string]struct{}, len(acct.Lenders))
		for owner := range acct.Lenders {
			owners[owner] = struct{}{}
		}
		for owner := range acct.LenderHours {
			owners[owner] = struct{}{}
		}
		for owner := range owners {
			status.Lenders = append(status.Lenders, v1.RunFundingLenderShare{
				Owner:    owner,
				GPUs:     acct.Lenders[owner],
				GPUHours: acct.LenderHours[owner],
			})
		}
		sort.Slice(status.Lenders, func(i, j int) bool {
			return status.Lenders[i].Owner < status.Lenders[j].Owner
		})
	}

	if status.OwnedGPUs == 0 && status.SharedGPUs == 0 && status.BorrowedGPUs == 0 && status.UnfundedGPUs == 0 &&
		status.OwnedGPUHours == 0 && status.SharedGPUHours == 0 && status.BorrowedGPUHours == 0 && status.UnfundedGPUHours == 0 {
		return nil
	}
	return status
}

type elasticGroup struct {
	Index      int
	Active     []*v1.Lease
	Spares     []*v1.Lease
	ActiveGPUs int
	// NonOwnedGPUs counts active width whose derived class is not Owned:
	// shrink returns other people's capacity (shared, borrowed, unfunded)
	// before the run's own.
	NonOwnedGPUs int
}

func collectElasticGroups(runKey string, leases []v1.Lease, ev *funding.Evaluation) map[int]*elasticGroup {
	groups := make(map[int]*elasticGroup)
	for i := range leases {
		lease := &leases[i]
		if lease.Status.Closed {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		idx, err := strconv.Atoi(leaseGroupIndex(lease))
		if err != nil {
			continue
		}
		grp, ok := groups[idx]
		if !ok {
			grp = &elasticGroup{Index: idx}
			groups[idx] = grp
		}
		if lease.Spec.Slice.Role == binder.RoleSpare {
			grp.Spares = append(grp.Spares, lease)
			continue
		}
		grp.Active = append(grp.Active, lease)
		slots := len(lease.Spec.Slice.Nodes)
		grp.ActiveGPUs += slots
		if ev != nil {
			if class, ok := ev.Class(lease); ok && class != funding.ClassOwned {
				grp.NonOwnedGPUs += slots
			}
		}
	}
	return groups
}

// borrowedGPUsForRun counts the run's active sponsor-funded (Borrowed
// class) width — the quantity spec.funding.maxBorrowGPUs caps. Family
// shared capacity is not borrowing and does not count (R15).
func borrowedGPUsForRun(ev *funding.Evaluation, run *v1.Run) int32 {
	if run == nil || ev == nil {
		return 0
	}
	acct := ev.Run(keys.NamespacedKey(run.Namespace, run.Name))
	if acct == nil {
		return 0
	}
	return acct.GPUs[funding.ClassBorrowed]
}

func (c *RunController) removePodsForGroups(runKey string, groups map[string]struct{}) {
	if len(groups) == 0 {
		return
	}
	var pods []binder.PodManifest
	for _, pod := range c.State.Pods {
		runLabel := pod.Labels[binder.LabelRunName]
		group := pod.Labels[binder.LabelGroupIndex]
		if keys.NamespacedKey(pod.Namespace, runLabel) == runKey {
			if _, ok := groups[group]; ok {
				continue
			}
		}
		pods = append(pods, pod)
	}
	c.State.Pods = pods
}

func leaseContainsNode(lease *v1.Lease, node string) bool {
	for _, slot := range lease.Spec.Slice.Nodes {
		if nodeFromSlot(slot) == node {
			return true
		}
	}
	return false
}

// openLeaseCountForRun counts the run's non-closed leases.
func openLeaseCountForRun(leases []v1.Lease, runKey string) int {
	count := 0
	for i := range leases {
		lease := &leases[i]
		if lease.Status.Closed {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) == runKey {
			count++
		}
	}
	return count
}

func findSpareLease(leases []v1.Lease, runKey, group string) (*v1.Lease, int) {
	for idx := range leases {
		lease := &leases[idx]
		if lease.Status.Closed {
			continue
		}
		if lease.Spec.Slice.Role != binder.RoleSpare {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		if leaseGroupIndex(lease) != group {
			continue
		}
		return lease, idx
	}
	return nil, -1
}

func leaseNodeNames(lease *v1.Lease) []string {
	result := make([]string, len(lease.Spec.Slice.Nodes))
	for i, slot := range lease.Spec.Slice.Nodes {
		result[i] = nodeFromSlot(slot)
	}
	return result
}

// buildSlotSet keys on the FULL node#ordinal slot, not the node. R22: a swap
// reclaims only the exact GPUs it re-places onto.
func buildSlotSet(slots []string) map[string]struct{} {
	set := make(map[string]struct{}, len(slots))
	for _, slot := range slots {
		set[slot] = struct{}{}
	}
	return set
}

// leaseOccupiesSlots reports whether the lease holds any of the exact slots.
func leaseOccupiesSlots(lease *v1.Lease, slots map[string]struct{}) bool {
	for _, slot := range lease.Spec.Slice.Nodes {
		if _, ok := slots[slot]; ok {
			return true
		}
	}
	return false
}

// nodesOfSlots maps node#ordinal SLOTS to the set of MACHINES they sit on.
//
// The conversion is explicit and named because the two are different keys and this
// file has shipped a defect from confusing them (R22 compared machines where slots
// were meant). buildNodeSet below takes machines already; handing it slots yields a
// set that matches nothing, silently — which is exactly what the first draft of
// reclaimSquatter's node-scoped eviction did, closing no lease at all.
func nodesOfSlots(slots []string) map[string]int {
	counts := make(map[string]int, len(slots))
	for _, slot := range slots {
		counts[nodeFromSlot(slot)]++
	}
	return counts
}

func buildNodeSet(nodes []string) map[string]int {
	counts := make(map[string]int, len(nodes))
	for _, node := range nodes {
		counts[node]++
	}
	return counts
}

// CloseLease is the SOLE CLOSER. Every transition of a Lease from open to closed
// in this repository goes through this function, and hack/antifake enforces that
// mechanically: no other non-test file may assign Lease.Status.Closed, .Ended, or
// .ClosureReason.
//
// The rule exists because closure used to be cloned. applyResolution and
// cleanupDeletedRun each hand-rolled the same three assignments, so a closure
// could be half-stamped — Closed without Ended, which makes funding.effectiveEnd
// bill the lease to its START instant so it accrues nothing for its entire life —
// and no single place could be instrumented, metered, or fixed. Three
// implementations of one obligation is three chances to drift.
//
// Closing is idempotent: a lease already closed keeps its original ending and
// reason. A Lease is an immutable fact, and the first closure is the true one.
func CloseLease(lease *v1.Lease, reason string, now time.Time) {
	if lease.Status.Closed {
		return
	}
	lease.Status.Closed = true
	ended := v1.NewTime(now)
	lease.Status.Ended = &ended
	lease.Status.ClosureReason = reason
}

func nodeFromSlot(slot string) string {
	if idx := strings.IndexRune(slot, '#'); idx >= 0 {
		return slot[:idx]
	}
	return slot
}
