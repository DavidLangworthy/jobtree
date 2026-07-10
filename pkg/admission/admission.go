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
// leaseSeqBase, borrowedGPUsForRun) are moved copies of the controller's private
// helpers; the controller's admission-only copies are deleted in the P2b
// cutover.
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
	// Quantity, when > 0, funds exactly this many GPUs against the live ledger
	// (no spares added) instead of the run's full Resources.TotalGPUs+spares.
	// The plugin passes it for an elastic-grow cohort so the DELTA is funded
	// incrementally on top of the base leases already in the ledger, rather than
	// re-funding the whole run.
	Quantity int32
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

// Feasible answers the plugin's atomic gate question: against the live world,
// can this Run's gang be placed (topology) AND funded (cover)? It runs the same
// pack + funding.Evaluate + cover sequence the controller's admission loop uses,
// stopping short of minting. A non-nil error means the gang cannot be admitted
// now — a pack error (no topology fits) or a cover error (no funding) — which is
// the plugin's signal to reject the pods (Unschedulable) so the controller
// forecasts a reservation. The returned funding.Evaluation is reused by callers
// (e.g. status mirrors). This is the authoritative check the plugin repeats at
// Permit (optimistic) and PreBind (under lock, before the mint) per §9 D6.
func Feasible(in Input) (pack.Plan, cover.Plan, *funding.Evaluation, error) {
	if in.Run == nil {
		return pack.Plan{}, cover.Plan{}, nil, fmt.Errorf("run must be provided")
	}
	run := in.Run

	usage := computeUsage(in.Leases, in.Now)
	snapshot, err := topology.BuildSnapshotForFlavor(in.Nodes, usage, run.Spec.Resources.GPUType)
	if err != nil {
		return pack.Plan{}, cover.Plan{}, nil, err
	}

	// A grow cohort funds an explicit DELTA (no new spares) against the ledger
	// that already holds the base leases; the base gang funds the full run plus
	// its spares.
	totalGPUs := int(run.Spec.Resources.TotalGPUs)
	spares := runSpares(run)
	if in.Quantity > 0 {
		totalGPUs = int(in.Quantity)
		spares = 0
	}

	packPlan, err := planPlacement(run, snapshot, totalGPUs, spares)
	if err != nil {
		return pack.Plan{}, cover.Plan{}, nil, err
	}

	ev := funding.Evaluate(funding.Input{
		Budgets: in.Budgets,
		Leases:  in.Leases,
		Runs:    in.Runs,
		Now:     in.Now,
		Period:  in.Period,
	})
	inventory := cover.NewInventory(ev)

	request := cover.Request{
		Owner:       run.Spec.Owner,
		Flavor:      run.Spec.Resources.GPUType,
		Quantity:    int32(totalGPUs) + int32(packPlan.TotalSpares),
		Location:    deriveLocation(packPlan),
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
		return packPlan, cover.Plan{}, ev, err
	}
	return packPlan, coverPlan, ev, nil
}

// Plan is Feasible followed by binder.Materialize — the full commit sequence,
// returning the leases and intended placement. Retained for the controller's
// current admission loop and the parity tests; the plugin uses Feasible +
// PerPodPayer + PodLease so it can mint one lease per real pod against the
// actual bound node.
func Plan(in Input) (*Result, error) {
	packPlan, coverPlan, _, err := Feasible(in)
	if err != nil {
		return nil, err
	}
	key := keys.NamespacedKey(in.Run.Namespace, in.Run.Name)
	res, err := binder.Materialize(binder.Request{
		Run:         in.Run.DeepCopy(),
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

// PerPodPayer expands a cover plan into one paying Segment per pod, assuming
// each pod consumes gpusPerPod GPUs. A pod is funded wholly by one envelope, so
// each segment's quantity must be a multiple of gpusPerPod (true when envelope
// grants and pod size align, which they do for real GPU workloads). The order
// matches the cover plan's family-proximity order (own before borrowed), so
// zipping it against the gang's pods (in a stable order) attributes owned pods
// before borrowed ones deterministically.
func PerPodPayer(plan cover.Plan, gpusPerPod int) ([]cover.Segment, error) {
	if gpusPerPod <= 0 {
		return nil, fmt.Errorf("gpusPerPod must be positive, got %d", gpusPerPod)
	}
	var out []cover.Segment
	for _, seg := range plan.Segments {
		if seg.Quantity <= 0 {
			continue
		}
		if int(seg.Quantity)%gpusPerPod != 0 {
			return nil, fmt.Errorf("cover segment %s/%s quantity %d is not a multiple of pod size %d",
				seg.BudgetName, seg.EnvelopeName, seg.Quantity, gpusPerPod)
		}
		for i := 0; i < int(seg.Quantity)/gpusPerPod; i++ {
			out = append(out, seg)
		}
	}
	return out, nil
}

// leaseLabels builds a minted Lease's labels. The group index is omitted only when
// the caller has none (the phantom pending leases the plugin folds into its funding
// arithmetic and never creates).
func leaseLabels(runName, role, groupIndex string) map[string]string {
	labels := map[string]string{
		binder.LabelRunName: runName,
		binder.LabelRunRole: role,
	}
	if groupIndex != "" {
		labels[binder.LabelGroupIndex] = groupIndex
	}
	return labels
}

// PodLease builds the immutable Lease for one workload pod: gpusPerPod GPUs on
// the node the scheduler actually bound it to, funded by seg. This is the
// plugin's mint at PreBind — the single point a Lease becomes a fact. name must
// be unique and stable for the pod (the plugin uses the pod's own name) so the
// create is idempotent across PreBind retries.
func PodLease(run *v1.Run, seg cover.Segment, node string, gpusPerPod int, name string, now time.Time, reason string) v1.Lease {
	return PodLeaseWithRole(run, seg, node, gpusPerPod, name, now, reason, binder.RoleActive, "")
}

// PodLeaseWithRole is PodLease with an explicit slice role and placement group. A
// held spare mints an identically-funded lease with RoleSpare so it sits out the
// gang's active width (summarizeRunWidth skips spares) yet is real, funded,
// reclaimable capacity a node-failure swap can land on.
//
// groupIndex names the fast-fabric group the rank belongs to, copied from the pod the
// plugin is binding. It is not decoration: pkg/resolver buckets a run's leases by it
// to cut one group rather than the whole run, the elastic loop shrinks by it, and a
// node-failure swap pairs a dead rank with the spare of its own group by it. Until
// R28b this function stamped no group at all, every pod was stamped "0", and none of
// those three could address anything. An empty groupIndex is accepted here only for
// the plugin's in-memory phantom pending leases, which never reach the API.
func PodLeaseWithRole(run *v1.Run, seg cover.Segment, node string, gpusPerPod int, name string, now time.Time, reason, role, groupIndex string) v1.Lease {
	if reason == "" {
		reason = "Start"
	}
	if role == "" {
		role = binder.RoleActive
	}
	slots := make([]string, gpusPerPod)
	for i := range slots {
		slots[i] = fmt.Sprintf("%s#%d", node, i)
	}
	return v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: run.Namespace,
			Name:      name,
			Labels:    leaseLabels(run.Name, role, groupIndex),
		},
		Spec: v1.LeaseSpec{
			Owner:          seg.Owner,
			RunRef:         v1.RunReference{Name: run.Name, Namespace: run.Namespace},
			Slice:          v1.LeaseSlice{Nodes: slots, Role: role},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(now)},
			PaidByBudget:   seg.BudgetName,
			PaidByEnvelope: seg.EnvelopeName,
			Reason:         reason,
		},
	}
}

// --- helpers moved from controllers/run_controller.go (admission-only) ---

func planPlacement(run *v1.Run, snapshot *topology.Snapshot, totalGPUs, spares int) (pack.Plan, error) {
	var groupSize *int
	if run.Spec.Locality != nil && run.Spec.Locality.GroupGPUs != nil {
		value := int(*run.Spec.Locality.GroupGPUs)
		groupSize = &value
	}
	return pack.Planner(snapshot, pack.Request{
		Flavor:                run.Spec.Resources.GPUType,
		TotalGPUs:             totalGPUs,
		GroupGPUs:             groupSize,
		AllowCrossGroupSpread: run.Spec.AllowCrossGroupSpread(),
		SparesPerGroup:        spares,
	})
}

// runSpares is the run's declared per-group spare count (0 if none).
func runSpares(run *v1.Run) int {
	if run.Spec.Spares != nil && *run.Spec.Spares > 0 {
		return int(*run.Spec.Spares)
	}
	return 0
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
