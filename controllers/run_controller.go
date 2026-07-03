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

	// A gang whose active pods have all Succeeded is complete: finalize it and
	// close its leases so it stops holding GPUs and charging its budget. A
	// single pod failure neither completes nor fails the run (owner decision):
	// the run keeps running until every active pod succeeds.
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

	if open := openLeaseCountForRun(c.State.Leases, key); run.Status.Phase != RunPhaseFailed && open > 0 {
		// Same half-applied-admission adoption as in activateReservation,
		// reachable from any watch event (lease creates included), so the
		// wedge heals without waiting for an activation tick. Failed runs
		// are excluded: adoption must not resurrect them (ruling
		// 2026-07-02).
		run.Status.Phase = RunPhaseRunning
		run.Status.Message = fmt.Sprintf("adopted %d open leases from an earlier admission", open)
		c.releasePendingReservations(run, now)
		run.Status.PendingReservation = nil
		run.Status.EarliestStart = nil
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		run.Status.Funding = summarizeRunFunding(run, ev)
		result = "adopted"
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

		coverPlan, err := inventory.Plan(request)
		if err != nil {
			if planErr, ok := err.(*cover.PlanError); ok && planErr.Reason != cover.FailureReasonInvalidRequest {
				if err := c.planReservation(run, snapshot, &packPlan, nil, planErr, ev, request, now); err != nil {
					result = "error"
					return err
				}
				result = "reserved"
				return nil
			}
			run.Status.Phase = RunPhasePending
			run.Status.Message = err.Error()
			result = "waiting"
			return nil
		}

		bindResult, err := binder.Materialize(binder.Request{Run: run.DeepCopy(), CoverPlan: coverPlan, PackPlan: packPlan, Now: now, NameSeed: leaseSeqBase(key, c.State.Leases)})
		if err != nil {
			result = "error"
			return err
		}

		c.State.Pods = append(c.State.Pods, bindResult.Pods...)
		c.State.Leases = append(c.State.Leases, bindResult.Leases...)

		// Invariant 8: no Pending reservation exists for a Running run.
		// Without this, a run that reserves and then binds directly would
		// materialize a second set of pods/leases when the reservation
		// activates.
		c.releasePendingReservations(run, now)

		run.Status.Phase = RunPhaseRunning
		run.Status.Message = fmt.Sprintf("bound %d GPUs", packPlan.TotalGPUs)
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))
		result = "bound"
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
				Owner:          seg.Owner,
				RunRef:         v1.RunReference{Name: run.Name, Namespace: run.Namespace},
				Slice:          v1.LeaseSlice{Nodes: nodes, Role: binder.RoleActive},
				Interval:       v1.LeaseInterval{Start: v1.NewTime(now)},
				PaidByBudget:   seg.BudgetName,
				PaidByEnvelope: seg.EnvelopeName,
				Reason:         "Hypothetical",
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

// runGangComplete reports whether every active (non-spare) pod of the run has
// reached the Succeeded phase. Spare pods are held capacity and do not gate
// completion; a run with no active pods (never bound) is not complete. A pod
// that Failed is simply not Succeeded, so it holds the run open rather than
// completing or failing it.
func (c *RunController) runGangComplete(run *v1.Run) bool {
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
		if pod.Phase != binder.PodPhaseSucceeded {
			return false
		}
	}
	return sawActive
}

// completeRun finalizes a succeeded gang: it closes the run's open leases
// (reason Completed) so the funding derivation stops counting them, drops the
// run's pods, and records the terminal phase.
func (c *RunController) completeRun(run *v1.Run, now time.Time) {
	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		if lease.Status.Closed {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		closeLease(lease, "Completed", now)
	}
	kept := c.State.Pods[:0]
	for _, pod := range c.State.Pods {
		if pod.Namespace == run.Namespace && pod.Labels[binder.LabelRunName] == run.Name {
			continue
		}
		kept = append(kept, pod)
	}
	c.State.Pods = kept
	run.Status.Phase = RunPhaseComplete
	run.Status.Message = "run completed: all active pods succeeded"
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
	run.Status.Width = summarizeRunWidth(run, c.State.Leases)
	run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))
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

// failRun terminally fails a pre-admission run (a follow cycle, or an upstream
// failure past grace). It never held leases, so there is nothing to close.
func (c *RunController) failRun(run *v1.Run, msg string) {
	run.Status.Phase = RunPhaseFailed
	run.Status.Message = msg
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
	run.Status.FollowDeadline = nil
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
	}
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
}

// ActivateReservations attempts to start any due reservations in sorted key
// order, invoking the resolver if capacity deficits remain. A reservation
// that fails to activate is recorded on its status and does not block later
// reservations; the collected errors are returned as an aggregate.
func (c *RunController) ActivateReservations(now time.Time) error {
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
			continue
		}
		if err := c.activateReservation(key, reservation, now); err != nil {
			reservation.Status.Reason = fmt.Sprintf("activation failed: %v", err)
			errs = append(errs, fmt.Errorf("reservation %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

func (c *RunController) activateReservation(key string, reservation *v1.Reservation, now time.Time) error {
	runKey := keys.NamespacedKey(reservation.Spec.RunRef.Namespace, reservation.Spec.RunRef.Name)
	run, ok := c.State.Runs[runKey]
	if !ok {
		// The run is gone, so the reservation can never activate; fail it
		// terminally rather than retrying every tick.
		reservation.Status.State = "Failed"
		reservation.Status.CountdownSeconds = nil
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

	if open := openLeaseCountForRun(c.State.Leases, runKey); open > 0 {
		// Open leases for a run that never reached Running mean an earlier
		// evaluation materialized them but its run-status write was lost
		// (the bridge's apply is not atomic — R28). Finish that activation
		// instead of planning again against the run's own capacity, which
		// would report the run's own leases as a deficit forever.
		activated := v1.NewTime(now)
		reservation.Status.State = "Released"
		reservation.Status.Reason = "Activated"
		reservation.Status.ActivatedAt = &activated
		reservation.Status.ReleasedAt = &activated
		reservation.Status.CountdownSeconds = nil
		run.Status.Phase = RunPhaseRunning
		run.Status.Message = fmt.Sprintf("adopted %d open leases from an earlier activation", open)
		run.Status.PendingReservation = nil
		run.Status.EarliestStart = nil
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))
		return nil
	}

	usage := computeUsage(c.State.Leases, now)
	snapshot, err := topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
	if err != nil {
		return err
	}

	ev := c.evaluate(now)
	inventory := cover.NewInventory(ev)

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
		}
	}

	result, err := binder.Materialize(binder.Request{Run: run.DeepCopy(), CoverPlan: coverPlan, PackPlan: plan, Now: now, NameSeed: leaseSeqBase(runKey, c.State.Leases)})
	if err != nil {
		return err
	}

	c.State.Pods = append(c.State.Pods, result.Pods...)
	c.State.Leases = append(c.State.Leases, result.Leases...)

	activated := v1.NewTime(now)
	reservation.Status.State = "Released"
	reservation.Status.Reason = "Activated"
	reservation.Status.ActivatedAt = &activated
	reservation.Status.ReleasedAt = &activated
	reservation.Status.CountdownSeconds = nil

	run.Status.Phase = RunPhaseRunning
	run.Status.Message = fmt.Sprintf("reservation %s activated", reservation.Name)
	run.Status.PendingReservation = nil
	run.Status.EarliestStart = nil
	run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))

	return nil
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
			segment.BudgetName = acct.Key.Budget
			segment.EnvelopeName = acct.Key.Envelope
			found = true
			break
		}
		if !found && acct.Spec.Flavor == run.Spec.Resources.GPUType {
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
	return fmt.Errorf("run %s has no envelope to fund reservation %s (budget removed)", runKey, reservation.Name)
}

// HandleNodeFailure performs a spare swap when a node fails.
func (c *RunController) HandleNodeFailure(nodeName string, now time.Time) error {
	if now.IsZero() {
		now = c.Clock.Now()
	}
	handled := false
	for i := range c.State.Leases {
		lease := &c.State.Leases[i]
		if lease.Status.Closed {
			continue
		}
		if lease.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		if !leaseContainsNode(lease, nodeName) {
			continue
		}
		handled = true
		runKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		run := c.State.Runs[runKey]
		groupIndex := lease.Labels[binder.LabelGroupIndex]
		if run == nil {
			closeLease(lease, "NodeFailure", now)
			continue
		}
		spareLease, spareIdx := findSpareLease(c.State.Leases, runKey, groupIndex)
		if spareLease == nil {
			closeLease(lease, "NodeFailure", now)
			run.Status.Phase = RunPhaseFailed
			run.Status.Message = fmt.Sprintf("node %s failed without spare coverage", nodeName)
			continue
		}
		spareNodes := leaseNodeNames(spareLease)
		spareSet := buildNodeSet(spareNodes)
		for j := range c.State.Leases {
			if j == spareIdx || j == i {
				continue
			}
			other := &c.State.Leases[j]
			if other.Status.Closed {
				continue
			}
			if !leasesOverlap(other, spareSet) {
				continue
			}
			closeLease(other, "ReclaimedBySpare", now)
		}
		closeLease(spareLease, "Swap", now)
		closeLease(lease, "NodeFailure", now)
		newLease := createSwapLease(run, groupIndex, spareLease, now)
		c.State.Leases = append(c.State.Leases, newLease)
		c.updatePodsAfterSwap(run, groupIndex, nodeName, spareSet)
		run.Status.Phase = RunPhaseRunning
		run.Status.Message = fmt.Sprintf("group %s swapped to spare after node %s failure", groupIndex, nodeName)
	}
	if !handled {
		return fmt.Errorf("no active lease found on node %s", nodeName)
	}
	return nil
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
	forecastResult, err := forecast.Plan(forecast.Input{
		Run:          run,
		Now:          now,
		Snapshot:     snapshot,
		PackPlan:     planPtr,
		PackErr:      packErr,
		CoverErr:     coverErr,
		CoverRequest: request,
		Evaluation:   ev,
	})
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
	if !forecastResult.EarliestStart.IsZero() {
		delta := forecastResult.EarliestStart.Sub(now).Seconds()
		if delta < 0 {
			delta = 0
		}
		metrics.SetReservationBacklog(run.Spec.Resources.GPUType, delta)
	}
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
		}
	}
	c.State.Reservations[key] = reservation

	run.Status.Phase = RunPhasePending
	run.Status.Message = fmt.Sprintf("reservation %s scheduled for %s (deficit %d GPUs)", reservationName, forecastResult.EarliestStart.Format(time.RFC3339), forecastResult.Forecast.DeficitGPUs)
	run.Status.PendingReservation = ptrString(reservationName)
	run.Status.EarliestStart = &earliest
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
		ended := v1.NewTime(now)
		lease.Status.Closed = true
		lease.Status.Ended = &ended
		lease.Status.ClosureReason = action.Reason
		// Counted here, not during planning: a resolver result can be
		// discarded (e.g. the lottery errors), and only applied actions
		// should show up in metrics.
		metrics.IncResolverAction(string(action.Kind))
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
		active := activeGPUsForRun(runKey, c.State.Leases)
		switch {
		case active > 0:
			run.Status.Phase = RunPhaseRunning
			run.Status.Message = "shrunk by resolver"
		case reclaimedOnly[runKey]:
			run.Status.Phase = RunPhasePending
			run.Status.Message = "reclaimed by funded demand; will re-admit when quota allows"
			run.Status.PendingReservation = nil
			run.Status.EarliestStart = nil
		default:
			run.Status.Phase = RunPhaseFailed
			run.Status.Message = "ended by resolver"
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

func activeGPUsForRun(runKey string, leases []v1.Lease) int {
	total := 0
	for i := range leases {
		lease := leases[i]
		if lease.Status.Closed {
			continue
		}
		key := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		if key != runKey {
			continue
		}
		if lease.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		total += len(lease.Spec.Slice.Nodes)
	}
	return total
}

// leaseSeqBase returns the number of leases (open or closed) that exist for
// the run, seeding the binder's name sequence so successive
// materializations cannot collide.
func leaseSeqBase(runKey string, leases []v1.Lease) int {
	count := 0
	for i := range leases {
		lease := &leases[i]
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) == runKey {
			count++
		}
	}
	return count
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

	if desired > allocated {
		growBy := desired - allocated
		step := run.Spec.Malleable.StepGPUs
		if growBy > step {
			growBy = step
		}
		if growBy <= 0 {
			return nil
		}
		if err := c.growRun(run, snapshot, inventory, now, int(growBy)); err != nil {
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
	}

	return nil
}

func (c *RunController) growRun(run *v1.Run, snapshot *topology.Snapshot, inventory *cover.Inventory, now time.Time, add int) error {
	if add <= 0 {
		return nil
	}

	allowSpread := run.Spec.AllowCrossGroupSpread()
	var groupSize *int
	if run.Spec.Locality != nil && run.Spec.Locality.GroupGPUs != nil {
		value := int(*run.Spec.Locality.GroupGPUs)
		groupSize = &value
	}
	spares := 0
	if run.Spec.Spares != nil && *run.Spec.Spares > 0 {
		spares = int(*run.Spec.Spares)
	}

	plan, err := pack.Planner(snapshot, pack.Request{
		Flavor:                run.Spec.Resources.GPUType,
		TotalGPUs:             add,
		GroupGPUs:             groupSize,
		AllowCrossGroupSpread: allowSpread,
		SparesPerGroup:        spares,
	})
	if err != nil {
		return err
	}

	quantity := add + plan.TotalSpares
	request := cover.Request{
		Owner:       run.Spec.Owner,
		Flavor:      run.Spec.Resources.GPUType,
		Quantity:    int32(quantity),
		Location:    deriveLocation(plan),
		Now:         now,
		Admitted:    run.CreationTimestamp.Time,
		RunKey:      keys.NamespacedKey(run.Namespace, run.Name),
		AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow,
	}
	if run.Spec.Funding != nil {
		request.Sponsors = append(request.Sponsors, run.Spec.Funding.Sponsors...)
		if run.Spec.Funding.MaxBorrowGPUs != nil {
			remaining := *run.Spec.Funding.MaxBorrowGPUs - borrowedGPUsForRun(c.evaluate(now), run)
			if remaining < 0 {
				remaining = 0
			}
			request.MaxBorrowGPUs = &remaining
		}
	}

	coverPlan, err := inventory.Plan(request)
	if err != nil {
		return err
	}

	runKey := keys.NamespacedKey(run.Namespace, run.Name)
	offset := maxGroupIndexForRun(runKey, c.State.Leases) + 1
	result, err := binder.Materialize(binder.Request{
		Run:              run.DeepCopy(),
		CoverPlan:        coverPlan,
		PackPlan:         plan,
		Now:              now,
		GroupIndexOffset: offset,
		LeaseReason:      "Grow",
		NameSeed:         leaseSeqBase(runKey, c.State.Leases),
	})
	if err != nil {
		return err
	}

	c.State.Pods = append(c.State.Pods, result.Pods...)
	c.State.Leases = append(c.State.Leases, result.Leases...)
	run.Status.Funding = summarizeRunFunding(run, c.evaluate(now))
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
			closeLease(lease, "Shrink", now)
		}
		for _, lease := range grp.Spares {
			closeLease(lease, "Shrink", now)
		}
		freed += int32(grp.ActiveGPUs)
		removed[strconv.Itoa(grp.Index)] = struct{}{}
	}

	if current-freed > target {
		return fmt.Errorf("insufficient groups available to reach target width")
	}

	if len(removed) > 0 {
		c.removePodsForGroups(runKey, removed)
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
		idxStr := "0"
		if lease.Labels != nil {
			if val, ok := lease.Labels[binder.LabelGroupIndex]; ok {
				idxStr = val
			}
		}
		idx, err := strconv.Atoi(idxStr)
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

func leaseActive(lease *v1.Lease, now time.Time) bool {
	if lease.Status.Closed {
		return false
	}
	if now.Before(lease.Spec.Interval.Start.Time) {
		return false
	}
	if lease.Spec.Interval.End != nil && !now.Before(lease.Spec.Interval.End.Time) {
		return false
	}
	if lease.Status.Ended != nil && !now.Before(lease.Status.Ended.Time) {
		return false
	}
	return true
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

func maxGroupIndexForRun(runKey string, leases []v1.Lease) int {
	maxIdx := -1
	for i := range leases {
		lease := &leases[i]
		if lease.Status.Closed {
			continue
		}
		if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
			continue
		}
		idxStr := "0"
		if lease.Labels != nil {
			if val, ok := lease.Labels[binder.LabelGroupIndex]; ok {
				idxStr = val
			}
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			continue
		}
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	return maxIdx
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
		if lease.Labels[binder.LabelGroupIndex] != group {
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

func buildNodeSet(nodes []string) map[string]int {
	counts := make(map[string]int, len(nodes))
	for _, node := range nodes {
		counts[node]++
	}
	return counts
}

func leasesOverlap(lease *v1.Lease, nodes map[string]int) bool {
	for _, slot := range lease.Spec.Slice.Nodes {
		if _, ok := nodes[nodeFromSlot(slot)]; ok {
			return true
		}
	}
	return false
}

func closeLease(lease *v1.Lease, reason string, now time.Time) {
	if lease.Status.Closed {
		return
	}
	lease.Status.Closed = true
	ended := v1.NewTime(now)
	lease.Status.Ended = &ended
	lease.Status.ClosureReason = reason
}

func createSwapLease(run *v1.Run, group string, spare *v1.Lease, now time.Time) v1.Lease {
	nodes := append([]string{}, spare.Spec.Slice.Nodes...)
	// The promoted lease keeps the spare's funding provenance (payer owner,
	// budget, envelope): the derivation classifies it from those facts, so
	// sponsor-paid capacity keeps counting against MaxBorrowGPUs and the
	// lender's caps without any role stamping (R15).
	role := binder.RoleActive
	labels := map[string]string{
		binder.LabelRunName:    run.Name,
		binder.LabelGroupIndex: group,
		binder.LabelRunRole:    role,
	}
	return v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: run.Namespace,
			Name:      fmt.Sprintf("%s-g%s-swap-%d", run.Name, group, now.UnixNano()),
			Labels:    labels,
		},
		Spec: v1.LeaseSpec{
			Owner: spare.Spec.Owner,
			RunRef: v1.RunReference{
				Name:      run.Name,
				Namespace: run.Namespace,
			},
			Slice: v1.LeaseSlice{
				Nodes: nodes,
				Role:  role,
			},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(now)},
			PaidByBudget:   spare.Spec.PaidByBudget,
			PaidByEnvelope: spare.Spec.PaidByEnvelope,
			Reason:         "Swap",
		},
	}
}

func (c *RunController) updatePodsAfterSwap(run *v1.Run, group, failedNode string, spareNodes map[string]int) {
	var pods []binder.PodManifest
	for _, pod := range c.State.Pods {
		runName := pod.Labels[binder.LabelRunName]
		groupIndex := pod.Labels[binder.LabelGroupIndex]
		role := pod.Labels[binder.LabelRunRole]
		if runName == run.Name && groupIndex == group {
			if pod.NodeName == failedNode {
				continue
			}
			if _, ok := spareNodes[pod.NodeName]; ok {
				continue
			}
		}
		if _, ok := spareNodes[pod.NodeName]; ok && role != binder.RoleActive {
			continue
		}
		pods = append(pods, pod)
	}
	c.State.Pods = pods
	for node, count := range spareNodes {
		labels := map[string]string{
			binder.LabelRunName:    run.Name,
			binder.LabelGroupIndex: group,
			binder.LabelRunRole:    binder.RoleActive,
		}
		podName := fmt.Sprintf("%s-g%s-swap-%s", run.Name, group, node)
		c.State.Pods = append(c.State.Pods, binder.PodManifest{
			Namespace: run.Namespace,
			Name:      podName,
			NodeName:  node,
			GPUs:      count,
			Labels:    labels,
		})
	}
}

func nodeFromSlot(slot string) string {
	if idx := strings.IndexRune(slot, '#'); idx >= 0 {
		return slot[:idx]
	}
	return slot
}
