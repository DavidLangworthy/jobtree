package binder

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/pack"
)

// The fuzz tests below generate random-but-valid pack and cover plans and
// assert the invariant library from docs/project/testing-and-simulation.md:
//
//	invariant 1 (placement validity): per-node pod GPU totals never exceed
//	the GPUs the pack plan allocated on that node, and every lease's slots
//	live on its pod's node;
//	invariant 2 (conservation): lease GPUs sum to the pack plan total and
//	per-envelope lease totals equal the cover segment quantities;
//	invariant 4 (name uniqueness): pod and lease names are unique.
//
// Plus the R15 role invariant: roles are lease facts, Active|Spare only.
// Every non-spare slice is RoleActive regardless of Segment.Borrowed —
// funding class (owned/shared/borrowed/unfunded) is derived downstream by
// pkg/funding from the payer fields, never encoded as a role (Decision 3).

// byteFeed doles out fuzz bytes as bounded integers; once the input is
// exhausted every draw returns the lower bound, keeping generation total.
type byteFeed struct {
	data []byte
	pos  int
}

func (f *byteFeed) next() byte {
	if f.pos >= len(f.data) {
		return 0
	}
	b := f.data[f.pos]
	f.pos++
	return b
}

// intn returns a value in [1, n].
func (f *byteFeed) intn(n int) int {
	if n <= 1 {
		return 1
	}
	return 1 + int(f.next())%n
}

func (f *byteFeed) bool() bool {
	return f.next()%2 == 1
}

// fuzzScenario is a randomly generated, internally consistent Materialize
// input plus the node capacities the pack plan was drawn from.
type fuzzScenario struct {
	req        Request
	capacities map[string]int
}

var fuzzBudgets = []struct {
	budget string
	owner  string
}{
	{budget: "rai", owner: "org:ai:rai"},
	{budget: "mm", owner: "org:ai:mm"},
	{budget: "infra", owner: "org:infra"},
}

// Envelope names deliberately repeat across budgets: the design review's
// lease-name collision needs two budgets that both contain a "west".
var fuzzEnvelopes = []string{"west", "east", "west"}

func genScenario(f *byteFeed) (fuzzScenario, bool) {
	numNodes := f.intn(5)
	capacities := make(map[string]int, numNodes)
	free := make(map[string]int, numNodes)
	nodeNames := make([]string, numNodes)
	for i := 0; i < numNodes; i++ {
		name := fmt.Sprintf("node-%d", i)
		cap := f.intn(8)
		nodeNames[i] = name
		capacities[name] = cap
		free[name] = cap
	}

	// carve allocation chunks out of remaining node capacity
	carve := func(maxChunks int) []pack.NodeAllocation {
		var allocs []pack.NodeAllocation
		chunks := f.intn(maxChunks)
		for c := 0; c < chunks; c++ {
			start := f.intn(numNodes) - 1
			var node string
			for i := 0; i < numNodes; i++ {
				candidate := nodeNames[(start+i)%numNodes]
				if free[candidate] > 0 {
					node = candidate
					break
				}
			}
			if node == "" {
				break
			}
			take := f.intn(free[node])
			free[node] -= take
			allocs = append(allocs, pack.NodeAllocation{Node: node, GPUs: take})
		}
		return allocs
	}

	numGroups := f.intn(3)
	var groups []pack.GroupPlacement
	total := 0
	for g := 0; g < numGroups; g++ {
		allocs := carve(3)
		if len(allocs) == 0 {
			break
		}
		size := 0
		for _, a := range allocs {
			size += a.GPUs
		}
		placement := pack.GroupPlacement{GroupIndex: len(groups), Size: size, NodePlacements: allocs}
		if f.bool() {
			spares := carve(2)
			spareTotal := 0
			for _, a := range spares {
				spareTotal += a.GPUs
			}
			if spareTotal > 0 {
				placement.Spares = spareTotal
				placement.SparePlacements = spares
			}
		}
		total += placement.Size + placement.Spares
		groups = append(groups, placement)
	}
	if len(groups) == 0 || total == 0 {
		return fuzzScenario{}, false
	}

	// partition the exact total into segments so Materialize must succeed
	var segments []cover.Segment
	remaining := total
	for remaining > 0 {
		qty := f.intn(remaining)
		which := fuzzBudgets[f.intn(len(fuzzBudgets))-1]
		segments = append(segments, cover.Segment{
			BudgetName:   which.budget,
			EnvelopeName: fuzzEnvelopes[f.intn(len(fuzzEnvelopes))-1],
			Owner:        which.owner,
			Quantity:     int32(qty),
			// Borrowed varies freely: it marks sponsor segments for the
			// downstream funding derivation and must never influence the
			// role the binder assigns (assertRoles checks this).
			Borrowed: f.bool(),
		})
		remaining -= qty
	}

	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"}}
	req := Request{
		Run:       run,
		PackPlan:  pack.Plan{Flavor: "H100-80GB", TotalGPUs: total, Groups: groups},
		CoverPlan: cover.Plan{Segments: segments},
		Now:       time.Unix(1767225600, 0),
	}
	return fuzzScenario{req: req, capacities: capacities}, true
}

func FuzzMaterializeInvariants(f *testing.F) {
	// Seeds biased toward misaligned segment/chunk boundaries; the corpus
	// runs as plain unit tests under `go test`.
	f.Add([]byte{2, 2, 2, 1, 1, 4, 0, 3, 1, 0, 1, 1, 0})
	f.Add([]byte{1, 3, 1, 2, 3, 0, 2, 1, 1, 1, 2, 0, 1, 1})
	f.Add([]byte{4, 7, 2, 5, 3, 3, 2, 1, 0, 4, 1, 2, 2, 6, 1, 0, 2, 1})
	f.Add([]byte{3, 2, 2, 2, 1, 2, 1, 1, 2, 1, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1})
	f.Add([]byte{5, 8, 8, 8, 8, 8, 2, 3, 8, 1, 3, 2, 7, 0, 5, 1, 3, 1, 1})

	f.Fuzz(func(t *testing.T, data []byte) {
		feed := &byteFeed{data: data}
		scenario, ok := genScenario(feed)
		if !ok {
			t.Skip("degenerate scenario")
		}
		res, err := Materialize(scenario.req)
		if err != nil {
			t.Fatalf("materialize failed on a consistent plan: %v\npack=%+v\ncover=%+v",
				err, scenario.req.PackPlan, scenario.req.CoverPlan)
		}
		assertInvariants(t, scenario, res)
	})
}

func assertInvariants(t *testing.T, scenario fuzzScenario, res Result) {
	t.Helper()
	assertPlacementValidity(t, scenario, res)
	assertConservation(t, scenario, res)
	assertNameUniqueness(t, res)
}

// invariant 1: per-node pod GPU totals never exceed what the pack plan
// allocated there, and each lease's slots are on its pod's node.
func assertPlacementValidity(t *testing.T, scenario fuzzScenario, res Result) {
	t.Helper()
	allocated := make(map[string]int)
	for _, group := range scenario.req.PackPlan.Groups {
		for _, alloc := range group.NodePlacements {
			allocated[alloc.Node] += alloc.GPUs
		}
		for _, alloc := range group.SparePlacements {
			allocated[alloc.Node] += alloc.GPUs
		}
	}

	podTotals := make(map[string]int)
	for _, pod := range res.Pods {
		if pod.GPUs <= 0 {
			t.Errorf("pod %s has %d GPUs", pod.Name, pod.GPUs)
		}
		podTotals[pod.NodeName] += pod.GPUs
	}
	for node, gpus := range podTotals {
		if gpus > allocated[node] {
			t.Errorf("node %s: pods claim %d GPUs but the pack plan allocated %d", node, gpus, allocated[node])
		}
		if cap, ok := scenario.capacities[node]; ok && gpus > cap {
			t.Errorf("node %s: pods claim %d GPUs but the node has %d", node, gpus, cap)
		}
	}

	if len(res.Pods) != len(res.Leases) {
		t.Fatalf("expected pods and leases to pair up, got %d pods and %d leases", len(res.Pods), len(res.Leases))
	}
	for i, lease := range res.Leases {
		pod := res.Pods[i]
		if len(lease.Spec.Slice.Nodes) != pod.GPUs {
			t.Errorf("lease %s has %d slots but pod %s claims %d GPUs",
				lease.Name, len(lease.Spec.Slice.Nodes), pod.Name, pod.GPUs)
		}
		for _, slot := range lease.Spec.Slice.Nodes {
			node := slot
			if idx := strings.IndexRune(slot, '#'); idx >= 0 {
				node = slot[:idx]
			}
			if node != pod.NodeName {
				t.Errorf("lease %s slot %s is not on pod %s's node %s", lease.Name, slot, pod.Name, pod.NodeName)
			}
		}
		if lease.Labels[LabelGroupIndex] == "" {
			t.Errorf("lease %s missing group index label", lease.Name)
		} else if _, err := strconv.Atoi(lease.Labels[LabelGroupIndex]); err != nil {
			t.Errorf("lease %s group index label %q is not numeric", lease.Name, lease.Labels[LabelGroupIndex])
		}
	}
}

// invariant 2: lease GPUs sum to the pack plan total and per-envelope lease
// totals equal the cover segment quantities.
func assertConservation(t *testing.T, scenario fuzzScenario, res Result) {
	t.Helper()
	planTotal := 0
	for _, group := range scenario.req.PackPlan.Groups {
		planTotal += group.Size
		for _, alloc := range group.SparePlacements {
			planTotal += alloc.GPUs
		}
	}
	leaseTotal := 0
	envelopeTotals := make(map[string]int)
	for _, lease := range res.Leases {
		gpus := len(lease.Spec.Slice.Nodes)
		leaseTotal += gpus
		envelopeTotals[lease.Spec.Owner+"/"+lease.Spec.PaidByEnvelope] += gpus
	}
	if leaseTotal != planTotal {
		t.Errorf("leases cover %d GPUs but the pack plan places %d", leaseTotal, planTotal)
	}

	segmentTotals := make(map[string]int)
	for _, seg := range scenario.req.CoverPlan.Segments {
		segmentTotals[seg.Owner+"/"+seg.EnvelopeName] += int(seg.Quantity)
	}
	for key, want := range segmentTotals {
		if envelopeTotals[key] != want {
			t.Errorf("envelope %s: segments fund %d GPUs but leases charge %d", key, want, envelopeTotals[key])
		}
	}
	for key, got := range envelopeTotals {
		if _, ok := segmentTotals[key]; !ok {
			t.Errorf("envelope %s: leases charge %d GPUs but no segment funds it", key, got)
		}
	}
}

// invariant 4: pod and lease names are unique per namespace.
func assertNameUniqueness(t *testing.T, res Result) {
	t.Helper()
	podNames := make(map[string]struct{}, len(res.Pods))
	for _, pod := range res.Pods {
		key := pod.Namespace + "/" + pod.Name
		if _, dup := podNames[key]; dup {
			t.Errorf("duplicate pod name %s", key)
		}
		podNames[key] = struct{}{}
	}
	leaseNames := make(map[string]struct{}, len(res.Leases))
	for _, lease := range res.Leases {
		key := lease.Namespace + "/" + lease.Name
		if _, dup := leaseNames[key]; dup {
			t.Errorf("duplicate lease name %s", key)
		}
		leaseNames[key] = struct{}{}
	}
}
