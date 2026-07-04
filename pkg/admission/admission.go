// Package admission is the pure decision core of the jobtree scheduler plugin:
// given a Run and the live world (budgets, leases, runs, nodes), it composes the
// moat functions — pack (topology placement), funding.Evaluate + cover (who
// pays), and binder.Materialize (the mint) — into the single commit the plugin
// performs at bind time.
//
// It is deliberately free of any kube-scheduler-framework dependency so the
// whole admit/deny/commit surface is unit-testable in-memory and fast. The
// framework extension points (Filter/Permit/PreBind) in cmd/scheduler/plugin
// are thin adapters that call Plan and enforce/commit its result.
//
// This is the new home of the admission logic that controllers/run_controller.go
// performs today inside Reconcile (the cover→pack→binder.Materialize loop). Per
// the single-committer design (docs/project/borrow-vs-build.md §9), that logic
// moves here; the controller keeps only lifecycle, forecast, and reclaim. The
// small helpers below (computeUsage, planPlacement, deriveLocation,
// expectedSpareTotal, leaseSeqBase, borrowedGPUsForRun) are moved copies of the
// controller's private helpers; the controller's admission-only copies are
// deleted in the P2b cutover.
package admission

import (
	"fmt"
	"strings"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/pack"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// Input is the world slice the admission decision needs. It mirrors the fields
// controllers.ClusterState exposes to the current admission loop.
type Input struct {
	Run     *v1.Run
	Budgets []v1.Budget
	Runs    map[string]*v1.Run // keyed by keys.NamespacedKey, for funding.Evaluate
	Leases  []v1.Lease         // the live ledger (open + closed)
	Nodes   []topology.SourceNode
	Now     time.Time
	Period  time.Duration // funding accounting horizon; <=0 uses funding.DefaultPeriod
	// Reason is the LeaseReason stamped on minted leases (Start/Grow/Swap/...).
	// Empty defaults to "Start" (binder.Materialize's default).
	Reason string
}

// Result is an admittable gang's committed plan: the topology placement, the
// funding cover, and the concrete leases to mint. Pods carries the intended
// node for each group-slice so Filter can enforce placement on the real,
// controller-emitted pods.
type Result struct {
	Pack   pack.Plan
	Cover  cover.Plan
	Leases []v1.Lease
	Pods   []binder.PodManifest
}

// Plan decides whether the Run's gang can be admitted against the live world
// and, if so, returns the leases to mint. A non-nil error means the gang cannot
// be admitted now: a pack error (no topology fits) or a cover error (no funding)
// is the caller's signal to leave the pods pending so the controller forecasts a
// reservation. This is the exact cover→pack→binder.Materialize sequence
// run_controller.go performs today, minus the inline reclaim (reclaim stays a
// controller/PostFilter concern per §9).
func Plan(in Input) (*Result, error) {
	if in.Run == nil {
		return nil, fmt.Errorf("run must be provided")
	}
	run := in.Run

	usage := computeUsage(in.Leases, in.Now)
	snapshot, err := topology.BuildSnapshotForFlavor(in.Nodes, usage, run.Spec.Resources.GPUType)
	if err != nil {
		return nil, err
	}

	packPlan, err := planPlacement(run, snapshot)
	if err != nil {
		return nil, err
	}

	ev := funding.Evaluate(funding.Input{
		Budgets: in.Budgets,
		Leases:  in.Leases,
		Runs:    in.Runs,
		Now:     in.Now,
		Period:  in.Period,
	})
	inventory := cover.NewInventory(ev)

	location := deriveLocation(packPlan)
	spareTotal := expectedSpareTotal(run, &packPlan)
	request := cover.Request{
		Owner:       run.Spec.Owner,
		Flavor:      run.Spec.Resources.GPUType,
		Quantity:    run.Spec.Resources.TotalGPUs + spareTotal,
		Location:    location,
		Now:         in.Now,
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
		return nil, err
	}

	key := keys.NamespacedKey(run.Namespace, run.Name)
	res, err := binder.Materialize(binder.Request{
		Run:         run.DeepCopy(),
		CoverPlan:   coverPlan,
		PackPlan:    packPlan,
		Now:         in.Now,
		LeaseReason: in.Reason,
		NameSeed:    leaseSeqBase(key, in.Leases),
	})
	if err != nil {
		return nil, err
	}

	return &Result{Pack: packPlan, Cover: coverPlan, Leases: res.Leases, Pods: res.Pods}, nil
}

// --- helpers moved from controllers/run_controller.go (admission-only) ---

func planPlacement(run *v1.Run, snapshot *topology.Snapshot) (pack.Plan, error) {
	var groupSize *int
	if run.Spec.Locality != nil && run.Spec.Locality.GroupGPUs != nil {
		value := int(*run.Spec.Locality.GroupGPUs)
		groupSize = &value
	}
	spares := 0
	if run.Spec.Spares != nil {
		spares = int(*run.Spec.Spares)
	}
	return pack.Planner(snapshot, pack.Request{
		Flavor:                run.Spec.Resources.GPUType,
		TotalGPUs:             int(run.Spec.Resources.TotalGPUs),
		GroupGPUs:             groupSize,
		AllowCrossGroupSpread: run.Spec.AllowCrossGroupSpread(),
		SparesPerGroup:        spares,
	})
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
		total := run.Spec.Resources.TotalGPUs
		groups = (total + groupSize - 1) / groupSize
	}
	return spares * groups
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
