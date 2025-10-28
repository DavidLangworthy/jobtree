package controllers

import (
	"fmt"
	"strings"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/budget"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/forecast"
	"github.com/davidlangworthy/jobtree/pkg/pack"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// Run phases used for status reporting.
const (
	RunPhasePending  = "Pending"
	RunPhaseRunning  = "Running"
	RunPhaseFailed   = "Failed"
	RunPhaseComplete = "Completed"
)

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

// Reconcile admits the run identified by namespace/name when feasible.
func (c *RunController) Reconcile(namespace, name string) error {
	key := namespacedKey(namespace, name)
	run, ok := c.State.Runs[key]
	if !ok {
		return fmt.Errorf("run %s/%s not found", namespace, name)
	}
	if run.Status.Phase == RunPhaseRunning || run.Status.Phase == RunPhaseComplete {
		return nil
	}

	now := c.Clock.Now()
	usage := computeUsage(c.State.Leases, now)
	snapshot, err := topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
	if err != nil {
		run.Status.Phase = RunPhasePending
		run.Status.Message = err.Error()
		return nil
	}

	states, err := c.budgetStates(now)
	if err != nil {
		return err
	}

	packPlan, err := planPlacement(run, snapshot)
	if err != nil {
		if planErr, ok := err.(*pack.PlanError); ok && planErr.Reason != pack.FailureReasonInvalidRequest {
			return c.planReservation(run, snapshot, nil, planErr, nil, states, cover.Request{Owner: run.Spec.Owner, Flavor: run.Spec.Resources.GPUType, Quantity: run.Spec.Resources.TotalGPUs, Now: now, AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow}, now)
		}
		run.Status.Phase = RunPhasePending
		run.Status.Message = err.Error()
		return nil
	}

	inventory, err := c.coverInventoryFromStates(states)
	if err != nil {
		return err
	}

	location := deriveLocation(packPlan)
	request := cover.Request{
		Owner:       run.Spec.Owner,
		Flavor:      run.Spec.Resources.GPUType,
		Quantity:    run.Spec.Resources.TotalGPUs,
		Location:    location,
		Now:         now,
		AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow,
	}
	if run.Spec.Funding != nil {
		request.Sponsors = append(request.Sponsors, run.Spec.Funding.Sponsors...)
	}

	coverPlan, err := inventory.Plan(request)
	if err != nil {
		if planErr, ok := err.(*cover.PlanError); ok && planErr.Reason != cover.FailureReasonInvalidRequest {
			return c.planReservation(run, snapshot, &packPlan, nil, planErr, states, request, now)
		}
		run.Status.Phase = RunPhasePending
		run.Status.Message = err.Error()
		return nil
	}

	result, err := binder.Materialize(binder.Request{Run: run.DeepCopy(), CoverPlan: coverPlan, PackPlan: packPlan, Now: now})
	if err != nil {
		return err
	}

	c.State.Pods = append(c.State.Pods, result.Pods...)
	c.State.Leases = append(c.State.Leases, result.Leases...)

	run.Status.Phase = RunPhaseRunning
	run.Status.Message = fmt.Sprintf("bound %d GPUs", packPlan.TotalGPUs)
	return nil
}

func (c *RunController) coverInventory(now time.Time) (*cover.Inventory, error) {
	states, err := c.budgetStates(now)
	if err != nil {
		return nil, err
	}
	return cover.NewInventory(states), nil
}

func (c *RunController) coverInventoryFromStates(states []*budget.BudgetState) (*cover.Inventory, error) {
	return cover.NewInventory(states), nil
}

func (c *RunController) budgetStates(now time.Time) ([]*budget.BudgetState, error) {
	states := make([]*budget.BudgetState, 0, len(c.State.Budgets))
	for i := range c.State.Budgets {
		budgetObj := c.State.Budgets[i]
		filtered := filterLeasesByOwner(c.State.Leases, budgetObj.Spec.Owner)
		copy := budgetObj
		st := budget.BuildBudgetState(&copy, filtered, now)
		states = append(states, st)
	}
	return states, nil
}

func ptrString(value string) *string { return &value }

func ptrInt64(value int64) *int64 { return &value }

func (c *RunController) planReservation(run *v1.Run, snapshot *topology.Snapshot, packPlan *pack.Plan, packErr *pack.PlanError, coverErr *cover.PlanError, states []*budget.BudgetState, request cover.Request, now time.Time) error {
	if states == nil {
		computed, err := c.budgetStates(now)
		if err != nil {
			return err
		}
		states = computed
	}

	var planPtr *pack.Plan
	if packPlan != nil {
		copy := *packPlan
		planPtr = &copy
	}
	forecastResult, err := forecast.Plan(forecast.Input{
		Run:          run,
		Now:          now,
		Snapshot:     snapshot,
		PackPlan:     planPtr,
		PackErr:      packErr,
		CoverErr:     coverErr,
		CoverRequest: request,
		BudgetStates: states,
	})
	if err != nil {
		run.Status.Phase = RunPhasePending
		run.Status.Message = fmt.Sprintf("reservation planning failed: %v", err)
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

	key := namespacedKey(run.Namespace, reservationName)
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

func filterLeasesByOwner(all []v1.Lease, owner string) []v1.Lease {
	var leases []v1.Lease
	for _, lease := range all {
		if lease.Spec.Owner == owner {
			leases = append(leases, lease)
		}
	}
	return leases
}

func namespacedKey(namespace, name string) string {
	return namespace + "/" + name
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
	allowSpread := true
	if run.Spec.Locality != nil && run.Spec.Locality.AllowCrossGroupSpread != nil {
		allowSpread = *run.Spec.Locality.AllowCrossGroupSpread
	}
	var groupSize *int
	if run.Spec.Locality != nil && run.Spec.Locality.GroupGPUs != nil {
		value := int(*run.Spec.Locality.GroupGPUs)
		groupSize = &value
	}
	req := pack.Request{
		Flavor:                run.Spec.Resources.GPUType,
		TotalGPUs:             int(run.Spec.Resources.TotalGPUs),
		GroupGPUs:             groupSize,
		AllowCrossGroupSpread: allowSpread,
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
