package controllers

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/budget"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/forecast"
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
	if run.Status.Phase == RunPhaseComplete {
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		return nil
	}

	now := c.Clock.Now()
	usage := computeUsage(c.State.Leases, now)
	snapshot, err := topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
	if err != nil {
		run.Status.Phase = RunPhasePending
		run.Status.Message = err.Error()
		run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		return nil
	}

	states, err := c.budgetStates(now)
	if err != nil {
		return err
	}
	inventory, err := c.coverInventoryFromStates(states)
	if err != nil {
		return err
	}

	run.Status.Width = summarizeRunWidth(run, c.State.Leases)

	if run.Status.Phase == RunPhaseRunning {
		if run.Spec.Malleable != nil {
			if err := c.reconcileElasticRun(run, snapshot, inventory, now); err != nil {
				return err
			}
			run.Status.Width = summarizeRunWidth(run, c.State.Leases)
		}
		return nil
	}

	packPlan, err := planPlacement(run, snapshot)
	if err != nil {
		if planErr, ok := err.(*pack.PlanError); ok && planErr.Reason != pack.FailureReasonInvalidRequest {
			request := cover.Request{Owner: run.Spec.Owner, Flavor: run.Spec.Resources.GPUType, Quantity: run.Spec.Resources.TotalGPUs, Now: now, AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow}
			if run.Spec.Funding != nil {
				request.Sponsors = append(request.Sponsors, run.Spec.Funding.Sponsors...)
			}
			return c.planReservation(run, snapshot, nil, planErr, nil, states, request, now)
		}
		run.Status.Phase = RunPhasePending
		run.Status.Message = err.Error()
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
	run.Status.Width = summarizeRunWidth(run, c.State.Leases)
	return nil
}

// ActivateReservations attempts to start any due reservations, invoking the resolver if deficits remain.
func (c *RunController) ActivateReservations(now time.Time) error {
	for key, reservation := range c.State.Reservations {
		if reservation.Status.State != "Pending" && reservation.Status.State != "" {
			continue
		}
		if reservation.Spec.EarliestStart.Time.After(now) {
			continue
		}
		if err := c.activateReservation(key, reservation, now); err != nil {
			return err
		}
	}
	return nil
}

func (c *RunController) activateReservation(key string, reservation *v1.Reservation, now time.Time) error {
	runKey := namespacedKey(reservation.Spec.RunRef.Namespace, reservation.Spec.RunRef.Name)
	run, ok := c.State.Runs[runKey]
	if !ok {
		return fmt.Errorf("run %s referenced by reservation %s not found", runKey, key)
	}

	usage := computeUsage(c.State.Leases, now)
	snapshot, err := topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
	if err != nil {
		return err
	}

	states, err := c.budgetStates(now)
	if err != nil {
		return err
	}
	inventory, err := c.coverInventoryFromStates(states)
	if err != nil {
		return err
	}

	location := reservation.Spec.IntendedSlice.Domain
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

	plan, err := planPlacement(run, snapshot)
	var packErr *pack.PlanError
	if err != nil {
		if pe, ok := err.(*pack.PlanError); ok {
			if pe.Reason != pack.FailureReasonInsufficientCapacity {
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
			if ce.Reason != cover.FailureReasonInsufficientCapacity {
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
		deficit := computeDeficit(snapshot, scope, totalNeeded)
		if deficit <= 0 {
			deficit = totalNeeded
		}
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
		}
		resolution, err := resolver.Resolve(resInput)
		if err != nil {
			return err
		}
		c.applyResolution(resolution, now)

		// rebuild snapshot and states after resolution
		usage = computeUsage(c.State.Leases, now)
		snapshot, err = topology.BuildSnapshotForFlavor(c.State.Nodes, usage, run.Spec.Resources.GPUType)
		if err != nil {
			return err
		}
		plan, err = planPlacement(run, snapshot)
		if err != nil {
			return err
		}
		states, err = c.budgetStates(now)
		if err != nil {
			return err
		}
		inventory, err = c.coverInventoryFromStates(states)
		if err != nil {
			return err
		}
		spareTotal = expectedSpareTotal(run, &plan)
		request.Quantity = run.Spec.Resources.TotalGPUs + spareTotal
		coverPlan, err = inventory.Plan(request)
		if err != nil {
			return err
		}
	}

	result, err := binder.Materialize(binder.Request{Run: run.DeepCopy(), CoverPlan: coverPlan, PackPlan: plan, Now: now})
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

	return nil
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
		runKey := namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
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

func (c *RunController) applyResolution(result resolver.Result, now time.Time) {
	if len(result.Actions) == 0 {
		return
	}
	closedGroups := map[string]struct{}{}
	affectedRuns := map[string]struct{}{}
	for _, action := range result.Actions {
		lease := action.Lease
		if lease == nil || lease.Status.Closed {
			continue
		}
		ended := v1.NewTime(now)
		lease.Status.Closed = true
		lease.Status.Ended = &ended
		lease.Status.ClosureReason = action.Reason
		runKey := namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
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
			key := namespacedKey(pod.Namespace, runName) + "::" + group
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
		if active == 0 {
			run.Status.Phase = RunPhaseFailed
			run.Status.Message = "ended by resolver"
		} else {
			run.Status.Phase = RunPhaseRunning
			run.Status.Message = "shrunk by resolver"
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
		key := namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
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
		if err := c.shrinkRun(run, desired, now); err != nil {
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

	allowSpread := true
	if run.Spec.Locality != nil && run.Spec.Locality.AllowCrossGroupSpread != nil {
		allowSpread = *run.Spec.Locality.AllowCrossGroupSpread
	}
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
		AllowBorrow: run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow,
	}
	if run.Spec.Funding != nil {
		request.Sponsors = append(request.Sponsors, run.Spec.Funding.Sponsors...)
	}

	coverPlan, err := inventory.Plan(request)
	if err != nil {
		return err
	}

	offset := maxGroupIndexForRun(namespacedKey(run.Namespace, run.Name), c.State.Leases) + 1
	result, err := binder.Materialize(binder.Request{
		Run:              run.DeepCopy(),
		CoverPlan:        coverPlan,
		PackPlan:         plan,
		Now:              now,
		GroupIndexOffset: offset,
		LeaseReason:      "Grow",
	})
	if err != nil {
		return err
	}

	c.State.Pods = append(c.State.Pods, result.Pods...)
	c.State.Leases = append(c.State.Leases, result.Leases...)
	return nil
}

func (c *RunController) shrinkRun(run *v1.Run, target int32, now time.Time) error {
	runKey := namespacedKey(run.Namespace, run.Name)
	groups := collectElasticGroups(runKey, c.State.Leases)
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
		if ordered[i].BorrowedGPUs == ordered[j].BorrowedGPUs {
			return ordered[i].Index > ordered[j].Index
		}
		return ordered[i].BorrowedGPUs > ordered[j].BorrowedGPUs
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
	runKey := namespacedKey(run.Namespace, run.Name)
	allocated := int32(0)
	for i := range leases {
		lease := &leases[i]
		if lease.Status.Closed {
			continue
		}
		if namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
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
		desired := run.Spec.Malleable.MaxTotalGPUs
		if run.Spec.Malleable.DesiredTotalGPUs != nil {
			desired = *run.Spec.Malleable.DesiredTotalGPUs
		}
		status.Desired = desired
	} else {
		total := run.Spec.Resources.TotalGPUs
		status.Min = total
		status.Max = total
		status.Desired = total
	}
	return status
}

type elasticGroup struct {
	Index        int
	Active       []*v1.Lease
	Spares       []*v1.Lease
	ActiveGPUs   int
	BorrowedGPUs int
}

func collectElasticGroups(runKey string, leases []v1.Lease) map[int]*elasticGroup {
	groups := make(map[int]*elasticGroup)
	for i := range leases {
		lease := &leases[i]
		if lease.Status.Closed {
			continue
		}
		if namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
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
		if lease.Spec.Slice.Role == binder.RoleBorrowed {
			grp.BorrowedGPUs += slots
		}
	}
	return groups
}

func (c *RunController) removePodsForGroups(runKey string, groups map[string]struct{}) {
	if len(groups) == 0 {
		return
	}
	var pods []binder.PodManifest
	for _, pod := range c.State.Pods {
		runLabel := pod.Labels[binder.LabelRunName]
		group := pod.Labels[binder.LabelGroupIndex]
		if namespacedKey(pod.Namespace, runLabel) == runKey {
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
		if namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
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

func findSpareLease(leases []v1.Lease, runKey, group string) (*v1.Lease, int) {
	for idx := range leases {
		lease := &leases[idx]
		if lease.Status.Closed {
			continue
		}
		if lease.Spec.Slice.Role != binder.RoleSpare {
			continue
		}
		if namespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) != runKey {
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
	labels := map[string]string{
		binder.LabelRunName:    run.Name,
		binder.LabelGroupIndex: group,
		binder.LabelRunRole:    binder.RoleActive,
	}
	return v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: run.Namespace,
			Name:      fmt.Sprintf("%s-g%s-swap-%d", run.Name, group, now.UnixNano()),
			Labels:    labels,
		},
		Spec: v1.LeaseSpec{
			Owner: run.Spec.Owner,
			RunRef: v1.RunReference{
				Name:      run.Name,
				Namespace: run.Namespace,
			},
			Slice: v1.LeaseSlice{
				Nodes: nodes,
				Role:  binder.RoleActive,
			},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(now)},
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
