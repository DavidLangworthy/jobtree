package plugin

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// gpuResource is the extended resource the fake/real device plugin advertises
// and each workload pod requests.
const gpuResource = corev1.ResourceName("nvidia.com/gpu")

// gangManager is the plugin's single committer. It serializes the funding
// decision across gangs (one Run's Active pod set) and records per-pod payers
// so PreBind can mint one Lease per pod against the node the scheduler actually
// chose. It is the plugin-side embodiment of borrow-vs-build.md §9: the sole
// place funding becomes a fact.
type gangManager struct {
	reader client.Reader // reads Runs/Budgets/Leases/Nodes (our CRDs + core)
	clock  func() time.Time

	mu    sync.Mutex
	gangs map[string]*gangCommit // keyed by namespace/run
}

// gangCommit is the decided funding state for one gang. Once decided, every
// member reads the same verdict; fundable gangs hand out payers one per pod.
type gangCommit struct {
	decided    bool
	fundable   bool
	reason     string
	payers     []cover.Segment // one paying envelope per pod, owned-before-borrowed
	claimed    int             // distinct pods that have claimed a payer
	assigned   map[string]int  // pod name -> payer index (idempotent across PreBind retries)
	gpusPerPod int
	// pending are the leases this gang will mint but has not yet; they are
	// folded into other gangs' funding checks until the real leases appear in
	// the API, closing the decide→mint overspend window.
	pending []v1.Lease
}

func newGangManager(reader client.Reader, clock func() time.Time) *gangManager {
	return &gangManager{reader: reader, clock: clock, gangs: map[string]*gangCommit{}}
}

// gangKey identifies the Active pod set a pod belongs to.
func gangKey(pod *corev1.Pod) string {
	return keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName])
}

func podInt(pod *corev1.Pod, annotation string, def int) int {
	if v, ok := pod.Annotations[annotation]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// decide runs (once per gang) the atomic funding check for a completed gang and
// records the per-pod payers. It must be called under no external lock; it takes
// the manager mutex itself. present is the number of gang members simultaneously
// waiting (including the caller), already confirmed >= expected width.
func (m *gangManager) decide(ctx context.Context, pod *corev1.Pod) (fundable bool, reason string) {
	key := gangKey(pod)
	m.mu.Lock()
	defer m.mu.Unlock()

	g := m.gangs[key]
	if g == nil {
		g = &gangCommit{}
		m.gangs[key] = g
	}
	if g.decided {
		return g.fundable, g.reason
	}
	g.decided = true
	g.gpusPerPod = podInt(pod, binder.AnnotationGPUs, 1)

	world, run, err := m.loadWorld(ctx, pod)
	if err != nil {
		g.reason = fmt.Sprintf("load world: %v", err)
		return false, g.reason
	}
	// Fold other gangs' not-yet-minted commitments into the ledger so two
	// gangs cannot both fund against the same free capacity.
	for k, other := range m.gangs {
		if k == key {
			continue
		}
		world.Leases = append(world.Leases, other.pending...)
	}

	_, coverPlan, _, err := admission.Feasible(world)
	if err != nil {
		g.reason = err.Error()
		return false, g.reason
	}
	payers, err := admission.PerPodPayer(coverPlan, g.gpusPerPod)
	if err != nil {
		g.reason = err.Error()
		return false, g.reason
	}
	g.payers = payers
	g.fundable = true
	g.pending = pendingLeases(run, payers, g.gpusPerPod, m.clock())
	return true, ""
}

// claimPayer hands a paying envelope to a PreBinding pod. It is idempotent per
// pod: a PreBind retry for the same pod returns the same envelope rather than
// consuming another (so a transient mint failure does not exhaust the gang).
// The second return is false if the gang is not fundable or has been exhausted
// (more distinct pods than the funded width — a bug, surfaced).
func (m *gangManager) claimPayer(pod *corev1.Pod) (cover.Segment, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gangs[gangKey(pod)]
	if g == nil || !g.fundable {
		return cover.Segment{}, 0, false
	}
	if idx, ok := g.assigned[pod.Name]; ok {
		return g.payers[idx], g.gpusPerPod, true
	}
	if g.claimed >= len(g.payers) {
		return cover.Segment{}, 0, false
	}
	idx := g.claimed
	g.claimed++
	if g.assigned == nil {
		g.assigned = map[string]int{}
	}
	g.assigned[pod.Name] = idx
	return g.payers[idx], g.gpusPerPod, true
}

// forget drops a gang's state (on Unreserve of an unbound member) so a later
// retry re-decides against fresh state. It is a no-op once any pod has claimed
// a payer, since those leases are (being) minted and must not be re-derived.
func (m *gangManager) forget(pod *corev1.Pod) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := gangKey(pod)
	if g := m.gangs[key]; g != nil && g.claimed == 0 {
		delete(m.gangs, key)
	}
}

// loadWorld reads the live cluster into an admission.Input for the pod's Run.
func (m *gangManager) loadWorld(ctx context.Context, pod *corev1.Pod) (admission.Input, *v1.Run, error) {
	key := gangKey(pod)
	var runList v1.RunList
	if err := m.reader.List(ctx, &runList); err != nil {
		return admission.Input{}, nil, fmt.Errorf("list runs: %w", err)
	}
	runs := make(map[string]*v1.Run, len(runList.Items))
	var run *v1.Run
	for i := range runList.Items {
		r := &runList.Items[i]
		rk := keys.NamespacedKey(r.Namespace, r.Name)
		runs[rk] = r
		if rk == key {
			run = r
		}
	}
	if run == nil {
		return admission.Input{}, nil, fmt.Errorf("run %s not found for pod %s", key, pod.Name)
	}

	var budgetList v1.BudgetList
	if err := m.reader.List(ctx, &budgetList); err != nil {
		return admission.Input{}, nil, fmt.Errorf("list budgets: %w", err)
	}
	var leaseList v1.LeaseList
	if err := m.reader.List(ctx, &leaseList); err != nil {
		return admission.Input{}, nil, fmt.Errorf("list leases: %w", err)
	}
	var nodeList corev1.NodeList
	if err := m.reader.List(ctx, &nodeList); err != nil {
		return admission.Input{}, nil, fmt.Errorf("list nodes: %w", err)
	}

	var nodes []topology.SourceNode
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		if !schedulableNode(n) {
			continue
		}
		gpus := 0
		if qty, ok := n.Status.Capacity[gpuResource]; ok {
			gpus = int(qty.Value())
		}
		nodes = append(nodes, topology.SourceNode{Name: n.Name, Labels: n.Labels, GPUs: gpus})
	}

	return admission.Input{
		Run:     run,
		Budgets: budgetList.Items,
		Runs:    runs,
		Leases:  leaseList.Items,
		Nodes:   nodes,
		Now:     m.clock(),
		Reason:  pod.Annotations[binder.AnnotationLeaseReason],
	}, run, nil
}

// pendingLeases builds placeholder leases (no bound node yet) that represent a
// decided gang's funding claim, so concurrent gangs' checks account for it
// before the real per-pod leases exist. Only the payer and GPU count matter for
// the cross-gang funding math; nodes are filled with the payer envelope as a
// stand-in and never persisted.
func pendingLeases(run *v1.Run, payers []cover.Segment, gpusPerPod int, now time.Time) []v1.Lease {
	out := make([]v1.Lease, 0, len(payers))
	for i, seg := range payers {
		out = append(out, admission.PodLease(run, seg, fmt.Sprintf("pending-%s-%d", run.Name, i), gpusPerPod, "", now, "Start"))
	}
	return out
}

// schedulableNode mirrors the bridge's nodeUsable gate: a node must be Ready and
// neither unschedulable nor carrying a NoSchedule/NoExecute taint to count as
// capacity.
func schedulableNode(node *corev1.Node) bool {
	if node.Spec.Unschedulable {
		return false
	}
	for _, taint := range node.Spec.Taints {
		if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
			return false
		}
	}
	ready := false
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			ready = cond.Status == corev1.ConditionTrue
		}
	}
	return ready
}
