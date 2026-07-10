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
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// gpuResource is the extended resource the fake/real device plugin advertises
// and each workload pod requests.
const gpuResource = corev1.ResourceName("nvidia.com/gpu")

const (
	// gangTTL bounds how long an idle gang commit lingers before the sweep drops
	// it. Kept well above the 2m Permit timeout so a slowly-forming or
	// slowly-minting gang is never reaped mid-flight; its only job is to reclaim
	// commits that were abandoned (a member that never bound, an unfundable gang
	// nobody retried, a deleted run) so phantom pending leases cannot leak (R1).
	gangTTL = 15 * time.Minute
	// sweepInterval is how often the plugin runs the TTL sweep. The PostBind fast
	// path reclaims the common case; this is only the backstop.
	sweepInterval = 5 * time.Minute
)

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
	// pending are the placeholder leases this gang has decided to mint but whose
	// real leases may not yet be in the API; they are folded into other gangs'
	// funding checks to close the decide→mint overspend window. minted[i] is set
	// true once pod i's REAL lease has been created (in PreBind), at which point
	// pending[i] is retired from the fold — the API's real lease now counts it,
	// so folding the phantom too would double-count (R1).
	pending []v1.Lease
	minted  []bool
	// lastTouched is the clock reading at the last decide/mint/postBind for this
	// gang; the sweep drops a gang idle past gangTTL so an abandoned commit (a
	// member that never bound, an unfundable gang) cannot leak its phantoms or
	// its map entry forever (R1).
	lastTouched time.Time
}

// fullyMinted reports whether every one of a fundable gang's pods has had its
// real lease created — at which point the in-memory commit is redundant with the
// API and may be garbage-collected.
func (g *gangCommit) fullyMinted() bool {
	if !g.fundable || len(g.minted) == 0 {
		return false
	}
	for _, m := range g.minted {
		if !m {
			return false
		}
	}
	return true
}

func newGangManager(reader client.Reader, clock func() time.Time) *gangManager {
	return &gangManager{reader: reader, clock: clock, gangs: map[string]*gangCommit{}}
}

// gangKey identifies the admission unit a pod belongs to: the run, plus its
// cohort. The base gang (cohort "0"/absent) and each elastic-grow cohort are
// independent gangs that assemble and fund separately.
func gangKey(pod *corev1.Pod) string {
	k := keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName])
	if c := pod.Annotations[binder.AnnotationCohort]; c != "" && c != "0" {
		return k + "#" + c
	}
	return k
}

// isGrowCohort reports whether the pod belongs to an elastic-grow cohort (funded
// as a delta) rather than the base gang (funded as the full run + spares).
func isGrowCohort(pod *corev1.Pod) bool {
	c := pod.Annotations[binder.AnnotationCohort]
	return c != "" && c != "0"
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
	start := m.clock()
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
	g.lastTouched = m.clock()
	g.gpusPerPod = podInt(pod, binder.AnnotationGPUs, 1)

	// loadWorld and the pending fold below run UNDER m.mu, together and atomically:
	// the fold retires another gang's phantom the instant its minted[i] flips, so
	// the snapshot must reflect that gang's real lease at that same instant (the
	// read-your-write the direct client gives). Reading a snapshot outside the lock
	// — or from an eventually-consistent cache — breaks that invariant and can
	// double-fund a gang; R4's cached/snapshot reads are therefore deferred to a
	// part that first makes the fold + PostBind staleness-robust (see R4 spec pt1b).
	world, run, err := m.loadWorld(ctx, pod)
	if err != nil {
		g.reason = fmt.Sprintf("load world: %v", err)
		metrics.ObserveDecideLatency("error", m.clock().Sub(start))
		return false, g.reason
	}
	// A grow cohort funds only its DELTA against the live ledger (which already
	// holds the base leases); the base gang funds the full run + spares.
	if isGrowCohort(pod) {
		world.Quantity = int32(podInt(pod, binder.AnnotationExpectedWidth, 1) * g.gpusPerPod)
	}
	// Fold other gangs' not-yet-minted commitments into the ledger so two
	// gangs cannot both fund against the same free capacity. A phantom whose
	// real lease already exists (minted[i]) is skipped: loadWorld's List already
	// counts the real lease, so folding the phantom too would double-count (R1).
	for k, other := range m.gangs {
		if k == key {
			continue
		}
		for i, pl := range other.pending {
			if i < len(other.minted) && other.minted[i] {
				continue
			}
			world.Leases = append(world.Leases, pl)
		}
	}
	// The ledger size fed to the replay is the R4 hot-path cost signal (compaction
	// pt2 will bound it to open leases).
	metrics.SetEvaluateInputSize(float64(len(world.Leases)))

	_, coverPlan, _, err := admission.Feasible(world)
	if err != nil {
		g.reason = err.Error()
		metrics.ObserveDecideLatency("unfundable", m.clock().Sub(start))
		return false, g.reason
	}
	payers, err := admission.PerPodPayer(coverPlan, g.gpusPerPod)
	if err != nil {
		g.reason = err.Error()
		metrics.ObserveDecideLatency("unfundable", m.clock().Sub(start))
		return false, g.reason
	}
	g.payers = payers
	g.fundable = true
	g.pending = pendingLeases(run, payers, g.gpusPerPod, m.clock())
	g.minted = make([]bool, len(g.pending))
	g.lastTouched = m.clock()
	metrics.ObserveDecideLatency("fundable", m.clock().Sub(start))
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
	g.lastTouched = m.clock()
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

// verdict reports a gang's decided funding outcome without triggering a
// decision. A held spare uses it to allow itself once the active gang has
// funded (its base cover already paid for the spares), rather than gating the
// active width. decided is false until the active completer runs decide.
func (m *gangManager) verdict(pod *corev1.Pod) (fundable, decided bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gangs[gangKey(pod)]
	if g == nil || !g.decided {
		return false, false
	}
	return g.fundable, true
}

// committedCount reports how many of a gang's pods have already claimed a payer
// (are minting or minted). Permit adds this to the count of still-waiting members
// so a gang that lost a member to a transient failure can re-assemble its full
// width from the survivors instead of wedging (R2). It is 0 for a gang that has
// not yet funded, so the first funding decision still requires the whole active
// set to be simultaneously waiting.
func (m *gangManager) committedCount(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.gangs[key]; g != nil {
		return g.claimed
	}
	return 0
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

// notifyMinted retires a pod's phantom pending lease once its REAL lease has been
// created (called from PreBind after a successful Create). From this point the
// API's real lease is what other gangs' funding checks count, so the phantom must
// no longer be folded — otherwise the gang double-counts itself forever (R1). It
// does NOT delete the gang: the pod may still fail to bind, and R2's recovery
// needs the commit state to survive until PostBind confirms the bind.
func (m *gangManager) notifyMinted(pod *corev1.Pod) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gangs[gangKey(pod)]
	if g == nil {
		return
	}
	g.lastTouched = m.clock()
	if idx, ok := g.assigned[pod.Name]; ok && idx < len(g.minted) {
		g.minted[idx] = true
	}
}

// postBind garbage-collects a gang once all of its pods have both minted their
// real leases and bound (PostBind fires only after a successful bind). At that
// point the in-memory commit is fully redundant with the API, so dropping it
// stops the phantom fold and the unbounded map growth (R1). A gang with an
// unbound member is left intact for R2 recovery and the TTL sweep.
func (m *gangManager) postBind(pod *corev1.Pod) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := gangKey(pod)
	g := m.gangs[key]
	if g == nil {
		return
	}
	g.lastTouched = m.clock()
	if g.fullyMinted() {
		delete(m.gangs, key)
	}
}

// sweep drops any gang idle past gangTTL: an abandoned commit (a member that
// never bound, an unfundable gang whose pods never retried, a run deleted
// mid-flight) would otherwise leak its phantom pending leases into every future
// funding decision and grow m.gangs without bound. gangTTL is kept well above
// permitTimeout so an actively-forming gang is never swept. Called on a ticker
// from the plugin's New (backstop to the PostBind fast path).
func (m *gangManager) sweep(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, g := range m.gangs {
		if now.Sub(g.lastTouched) > gangTTL {
			delete(m.gangs, key)
		}
	}
}

// runSweep drives sweep on a ticker until ctx is cancelled (the scheduler's
// lifetime). Split from sweep so the reaping logic stays directly unit-testable
// with an injected clock.
func (m *gangManager) runSweep(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweep(m.clock())
		}
	}
}

// spareLeaseProvenanceValid reports whether a real Spare lease for the run
// carries the funding provenance seg. PreBind uses it as defense-in-depth before
// minting a node-failure swap from pod-carried provenance: the swap path skips
// the funding gate and trusts the pod's payer-* annotations, so without this a
// hand-crafted pod stamped lease-reason=Swap could mint a Lease against ANY
// envelope. Requiring a matching spare the run actually held means a forged swap
// can only ever charge an envelope for which a real spare lease exists — which an
// attacker cannot fabricate without lease-create RBAC. This backs up the
// mandatory-scheduler policy (R5/R6); it holds even if that policy is absent.
func (m *gangManager) spareLeaseProvenanceValid(ctx context.Context, ns, runName string, seg cover.Segment) bool {
	var leaseList v1.LeaseList
	if err := m.reader.List(ctx, &leaseList); err != nil {
		return false
	}
	for i := range leaseList.Items {
		l := &leaseList.Items[i]
		if l.Spec.RunRef.Namespace != ns || l.Spec.RunRef.Name != runName {
			continue
		}
		if l.Spec.Slice.Role != binder.RoleSpare {
			continue
		}
		if l.Spec.Owner == seg.Owner && l.Spec.PaidByBudgetNamespace == seg.Namespace &&
			l.Spec.PaidByBudget == seg.BudgetName && l.Spec.PaidByEnvelope == seg.EnvelopeName {
			return true
		}
	}
	return false
}

// promiseProvenanceValid reports whether a Promise pod's carried provenance may
// charge the envelope it names, for its own Run. PreBind uses it as
// defense-in-depth before minting a promised activation from pod-carried
// provenance: like the swap path, Promise skips the funding gate and trusts the
// pod's payer-* annotations, so without this a hand-crafted pod stamped
// lease-reason=Promise could mint a gate-free Lease charging a victim's budget.
//
// The load-bearing fields are the CHARGED ones — PaidByBudget/PaidByEnvelope
// (seg.BudgetName/seg.EnvelopeName) — because funding.Evaluate resolves every
// charge by EnvelopeKey{PaidByBudget, PaidByEnvelope} and derives the owner/tier
// from the real Budget object, never from the lease's cosmetic Spec.Owner. So
// pinning seg.Owner alone (which Evaluate ignores) would let a pod whose own run
// it owns point payer-budget/envelope at another owner's envelope and mint a
// gate-free charge there. We instead resolve the named Budget and require it to
// be owned by the run's own owner and to carry the named envelope — the exact
// invariant the controller's opportunisticCoverPlan upholds (it only ever
// attributes a promise to an envelope the run's owner owns). This backs up the
// mandatory-scheduler policy (R5/R6), which restricts the annotations themselves
// to the controller ServiceAccount; it holds even if that policy is absent.
func (m *gangManager) promiseProvenanceValid(ctx context.Context, ns, runName string, seg cover.Segment) bool {
	var runList v1.RunList
	if err := m.reader.List(ctx, &runList); err != nil {
		return false
	}
	var run *v1.Run
	for i := range runList.Items {
		r := &runList.Items[i]
		if r.Namespace == ns && r.Name == runName {
			run = r
			break
		}
	}
	if run == nil {
		return false
	}
	// seg.Owner is not what Evaluate charges, but a legitimate segment always has
	// it equal to the run owner; keep it consistent so the minted lease's
	// Spec.Owner is not misleading.
	if seg.Owner != run.Spec.Owner {
		return false
	}
	// The charge itself: the named budget must be owned by the run's owner and
	// must actually carry the named envelope.
	var budgetList v1.BudgetList
	if err := m.reader.List(ctx, &budgetList); err != nil {
		return false
	}
	for i := range budgetList.Items {
		b := &budgetList.Items[i]
		if b.Name != seg.BudgetName {
			continue
		}
		if b.Spec.Owner != run.Spec.Owner {
			return false
		}
		for _, env := range b.Spec.Envelopes {
			if env.Name == seg.EnvelopeName {
				return true
			}
		}
		return false
	}
	return false
}

// loadWorld reads the live cluster into an admission.Input for the pod's Run.
func (m *gangManager) loadWorld(ctx context.Context, pod *corev1.Pod) (admission.Input, *v1.Run, error) {
	// The run key is the run, without any cohort suffix gangKey adds — a grow
	// cohort's pods still belong to the same Run object.
	runKey := keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName])
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
		if rk == runKey {
			run = r
		}
	}
	if run == nil {
		return admission.Input{}, nil, fmt.Errorf("run %s not found for pod %s", runKey, pod.Name)
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
