package plugin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/pack"
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
	// runUIDs caches each Run's UID so a plugin Event can be MIRRORED onto the Run
	// with a reference kubectl will match (R20). It is a cache, not state: a miss
	// costs one Get, a stale entry costs an Event attached to a dead UID, and the
	// sweep drops entries with their gang. Never read by any funding decision.
	runUIDs map[string]types.UID
	// lastForming throttles the Run-visible GangForming mirror per gang.
	lastForming map[string]time.Time
	// parkedAt records when each member entered Permit's Wait, so Unreserve can tell a
	// framework TIMEOUT from any other unreserve rather than guessing (R20). Entries
	// are dropped on forget, which the framework calls for every unreserved pod.
	parkedAt map[string]time.Time
}

// gangCommit is the decided funding state for one gang. Once decided, every
// member reads the same verdict; fundable gangs hand out payers one per pod.
type gangCommit struct {
	decided  bool
	fundable bool
	reason   string
	// refusal records WHICH planner said no (R20). Stored rather than returned so
	// decide's signature — and its nineteen call sites — stay as they are, and so a
	// cached verdict carries the same distinction as a fresh one.
	refusal    refusalKind
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
	pending []v1.GPULease
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
	return &gangManager{
		reader:      reader,
		clock:       clock,
		gangs:       map[string]*gangCommit{},
		runUIDs:     map[string]types.UID{},
		lastForming: map[string]time.Time{},
		parkedAt:    map[string]time.Time{},
	}
}

// refusalKind separates the two ways admission.Feasible can say no. They send a
// researcher to different places — one to their budget, one to the cluster — and
// collapsing them into one string is why Permit called every refusal "not fundable".
type refusalKind int

const (
	refusalUnfundable  refusalKind = iota // the cover step: no envelope pays for this
	refusalUnplaceable                    // the pack step: the hardware cannot hold it
)

// classifyRefusal reads the typed error the planners return. It matches on TYPE, never
// on message text: pack and cover both return a *PlanError with a Reason, and the
// distinction is exactly what R20 needs preserved.
func classifyRefusal(err error) refusalKind {
	var packErr *pack.PlanError
	if errors.As(err, &packErr) {
		return refusalUnplaceable
	}
	return refusalUnfundable
}

// runUID returns the cached UID for a Run, fetching it once per gang lifetime.
//
// Deliberately best-effort: a failed Get returns "" and the caller skips the Run
// mirror rather than emitting an Event nothing can find. Narration must never be able
// to fail a scheduling decision.
func (m *gangManager) runUID(namespace, name string) types.UID {
	key := keys.NamespacedKey(namespace, name)
	m.mu.Lock()
	if uid, ok := m.runUIDs[key]; ok {
		m.mu.Unlock()
		return uid
	}
	m.mu.Unlock()

	var run v1.Run
	if err := m.reader.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &run); err != nil {
		return ""
	}
	m.mu.Lock()
	m.runUIDs[key] = run.UID
	m.mu.Unlock()
	return run.UID
}

// refusalOf reports which planner refused a decided gang. Unfundable is the safe
// default: it is what Permit said for every refusal before R20, so an unclassified
// path degrades to the old message rather than to a confident wrong one.
func (m *gangManager) refusalOf(key string) refusalKind {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g := m.gangs[key]; g != nil {
		return g.refusal
	}
	return refusalUnfundable
}

// noteWaiting stamps the moment a member parked at the gate.
func (m *gangManager) noteWaiting(podName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, already := m.parkedAt[podName]; !already {
		m.parkedAt[podName] = m.clock()
	}
}

// waitedOutTimeout reports whether this member sat at the gate for the full
// permitTimeout — i.e. the framework's Permit timeout is what ended it, rather than a
// sibling's rejection or a bind failure.
func (m *gangManager) waitedOutTimeout(podName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	parked, ok := m.parkedAt[podName]
	return ok && m.clock().Sub(parked) >= permitTimeout
}

// shouldReportForming rate-limits the GangForming mirror to one Event per gang per
// formingEventInterval, and reports true the first time it sees a gang.
func (m *gangManager) shouldReportForming(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	if last, ok := m.lastForming[key]; ok && now.Sub(last) < formingEventInterval {
		return false
	}
	m.lastForming[key] = now
	return true
}

// gangKey identifies the admission unit a pod belongs to: the run, plus its
// cohort. The base gang (cohort "0"/absent) and each elastic-grow cohort are
// independent gangs that assemble and fund separately.
func gangKey(pod *corev1.Pod) string {
	k := keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName])
	if c := cohortOf(pod); c != "0" {
		return k + "#" + c
	}
	return k
}

// cohortOf is a pod's admission cohort, normalized so the base gang is always the
// literal "0" (an absent annotation and "0" are the same cohort). The plugin stamps
// this onto the minted lease so gang membership is recoverable from the lease.
func cohortOf(pod *corev1.Pod) string {
	if c := pod.Annotations[binder.AnnotationCohort]; c != "" {
		return c
	}
	return "0"
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
	// Fold other gangs' not-yet-minted commitments into the ledger so two gangs
	// cannot both fund against the same free capacity.
	//
	// STALENESS-ROBUST (R4 pt1b): skip a phantom only when its REAL lease is actually
	// present in the loaded snapshot — NOT by the in-memory minted flag. The flag flips
	// the instant we Create the lease, but a reader (a direct read AFTER the write, or
	// an eventually-consistent CACHE that has not yet caught it) may not see that real
	// lease yet; skipping the phantom then leaves the capacity counted by NEITHER the
	// phantom nor the (missing) real lease, and the two gangs double-fund. That is the
	// exact hazard that reverted R4 pt1b's first cache. Keying off the snapshot makes
	// the fold correct for the direct reader AND unblocks the informer cache: whether
	// the real lease is visible is a property of the world we actually replayed.
	//
	// A phantom is matched to its real lease by the pod that claimed its slot (the
	// in-memory assigned map is reliable — it is set by our own claimPayer) and the
	// durable pod-name identity stamped on the minted lease (Phase 4/5).
	present := map[string]bool{}
	for i := range world.Leases {
		l := &world.Leases[i]
		if l.Status.Closed {
			continue
		}
		if pn := binder.LeasePodName(l); pn != "" {
			present[pn] = true
		}
	}
	for k, other := range m.gangs {
		if k == key {
			continue
		}
		// Which pod claimed each phantom slot (reverse of assigned).
		slotPod := make([]string, len(other.pending))
		for pn, idx := range other.assigned {
			if idx >= 0 && idx < len(slotPod) {
				slotPod[idx] = pn
			}
		}
		for i, pl := range other.pending {
			if pod := slotPod[i]; pod != "" && present[pod] {
				continue // this slot's real lease is in the snapshot; it already counts
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
		g.refusal = classifyRefusal(err)
		metrics.ObserveDecideLatency("unfundable", m.clock().Sub(start))
		return false, g.reason
	}
	payers, err := admission.PerPodPayer(coverPlan, g.gpusPerPod)
	if err != nil {
		g.reason = err.Error()
		g.refusal = classifyRefusal(err)
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
// Reconstruct rebuilds in-memory gang commitments from the OPEN leases in the API
// after a scheduler restart. gangs starts empty on New, so committedCount returns 0
// and Permit's (waiting + committed >= expected) degrades to (waiting >= expected):
// a lone surviving member of a partially-bound gang parks, times out at
// permitTimeout, and loops forever (R2 problem #2, reintroduced by any restart).
//
// It groups open ACTIVE leases by gang (run + cohort — durable identity stamped at
// mint, R2 pt3 / Phase 4) and rebuilds one already-decided gangCommit per gang: the
// already-minted members from each lease's OWN provenance (so a PreBind retry of an
// already-bound pod returns its original payer and does not consume a delta payer),
// and — for the base gang — the un-minted remainder funded as a DELTA against the
// live ledger (which already holds and charges those leases), never the full width on
// top of them (which double-counts). A grow cohort's minted count is restored (so its
// gate holds) but its remainder is left for the controller's re-emit + a fresh decide,
// since its target width is not on the Run object.
//
// Called from New before the plugin serves. A failure is non-fatal: the plugin serves
// (decide still funds fresh gangs); only in-flight partial gangs stay at risk.
func (m *gangManager) Reconstruct(ctx context.Context) error {
	var runList v1.RunList
	if err := m.reader.List(ctx, &runList); err != nil {
		return fmt.Errorf("list runs: %w", err)
	}
	runs := make(map[string]*v1.Run, len(runList.Items))
	for i := range runList.Items {
		r := &runList.Items[i]
		runs[keys.NamespacedKey(r.Namespace, r.Name)] = r
	}
	var budgetList v1.BudgetList
	if err := m.reader.List(ctx, &budgetList); err != nil {
		return fmt.Errorf("list budgets: %w", err)
	}
	var leaseList v1.GPULeaseList
	if err := m.reader.List(ctx, &leaseList); err != nil {
		return fmt.Errorf("list leases: %w", err)
	}
	var nodeList corev1.NodeList
	if err := m.reader.List(ctx, &nodeList); err != nil {
		return fmt.Errorf("list nodes: %w", err)
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

	// Group the OPEN, ACTIVE leases by gang key. Spares are not gang-active-width
	// members (Permit gates on active width); closed leases are settled facts.
	byGang := map[string][]*v1.GPULease{}
	cohortOfGang := map[string]string{}
	runKeyOfGang := map[string]string{}
	for i := range leaseList.Items {
		l := &leaseList.Items[i]
		if l.Status.Closed || l.Spec.Slice.Role == binder.RoleSpare {
			continue
		}
		runKey := keys.NamespacedKey(l.Spec.RunRef.Namespace, l.Spec.RunRef.Name)
		cohort := binder.LeaseCohort(l)
		key := runKey
		if cohort != "0" {
			key = runKey + "#" + cohort
		}
		byGang[key] = append(byGang[key], l)
		cohortOfGang[key] = cohort
		runKeyOfGang[key] = runKey
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for key, leases := range byGang {
		if m.gangs[key] != nil {
			continue // already tracked (a gang that decided since start)
		}
		run := runs[runKeyOfGang[key]]
		if run == nil {
			continue // run gone — its orphan leases are cleanupDeletedRun's / the settle sweep's
		}
		gpusPerPod := len(leases[0].Spec.Slice.Nodes)
		if gpusPerPod <= 0 {
			gpusPerPod = 1
		}
		g := &gangCommit{decided: true, fundable: true, lastTouched: m.clock(), gpusPerPod: gpusPerPod, assigned: map[string]int{}}
		// The already-minted members, from each lease's own provenance.
		for _, l := range leases {
			idx := len(g.payers)
			g.payers = append(g.payers, cover.Segment{
				Owner:        l.Spec.Owner,
				Namespace:    l.Spec.PaidByBudgetNamespace,
				BudgetName:   l.Spec.PaidByBudget,
				EnvelopeName: l.Spec.PaidByEnvelope,
			})
			g.minted = append(g.minted, true)
			if pn := binder.LeasePodName(l); pn != "" {
				g.assigned[pn] = idx
			}
		}
		g.claimed = len(g.payers)

		// Delta-fund the un-minted remainder of the BASE gang only.
		if cohortOfGang[key] == "0" {
			expected := int(run.Spec.Resources.TotalGPUs) / gpusPerPod
			if delta := expected - g.claimed; delta > 0 {
				world := admission.Input{
					Run: run, Budgets: budgetList.Items, Runs: runs, Leases: leaseList.Items,
					Nodes: nodes, Now: m.clock(), Quantity: int32(delta * gpusPerPod),
				}
				if _, coverPlan, _, err := admission.Feasible(world); err == nil {
					if deltaPayers, perr := admission.PerPodPayer(coverPlan, gpusPerPod); perr == nil {
						for _, seg := range deltaPayers {
							g.payers = append(g.payers, seg)
							g.minted = append(g.minted, false)
						}
						// pending must align with payers (full width), not just the delta:
						// the staleness-robust fold indexes phantoms by the pod that claimed
						// each slot, and the minted slots (0..claimed-1) carry the assigned
						// pods. A delta-only pending would misalign, so the fold would read a
						// delta phantom against a MINTED pod's slot, see its real lease
						// present, and wrongly skip the survivor's phantom — letting another
						// gang take the survivor's capacity. Full-width pending: the minted
						// slots' phantoms are skipped (their real lease is present), the delta
						// slots' (no assigned pod) are folded.
						g.pending = pendingLeases(run, g.payers, gpusPerPod, m.clock())
					}
				}
				// If the delta cannot fund now (capacity gone / budget tightened), the
				// survivor holds: committedCount is restored so the gate is no longer
				// wedged, and the run re-admits through the controller's normal path.
			}
		}
		m.gangs[key] = g
	}
	return nil
}

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
	// The park stamp is per-pod bookkeeping for one trip through the gate; dropping it
	// here is what keeps parkedAt bounded by the pods currently in flight.
	delete(m.parkedAt, pod.Name)
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
			// The R20 narration caches are bounded by the same sweep, so an
			// observability feature cannot become the unbounded map R1 was about.
			delete(m.lastForming, key)
		}
	}
	// runUIDs is keyed by run, not by gang (a run's grow cohorts share it), so it is
	// reaped when no gang for that run remains.
	for key := range m.runUIDs {
		if _, base := m.gangs[key]; base {
			continue
		}
		live := false
		for gk := range m.gangs {
			if strings.HasPrefix(gk, key+"#") {
				live = true
				break
			}
		}
		if !live {
			delete(m.runUIDs, key)
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
	var leaseList v1.GPULeaseList
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
	// R7: the promise path only ever charges the run's OWN envelopes, so the
	// charged Budget must live in the run's own namespace — which the API server
	// authenticates (metadata.namespace cannot be forged). Namespace equality is
	// forge-proof where the old owner-string equality was two writable fields
	// agreeing with each other; with Run.Spec.Owner deleted it is also the only
	// check available, and it is strictly stronger.
	if seg.Namespace != run.Namespace {
		return false
	}
	// The charge itself: the named budget must live in the run's namespace and
	// must actually carry the named envelope.
	var budgetList v1.BudgetList
	if err := m.reader.List(ctx, &budgetList); err != nil {
		return false
	}
	for i := range budgetList.Items {
		b := &budgetList.Items[i]
		if b.Namespace != run.Namespace || b.Name != seg.BudgetName {
			continue
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
	var leaseList v1.GPULeaseList
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
func pendingLeases(run *v1.Run, payers []cover.Segment, gpusPerPod int, now time.Time) []v1.GPULease {
	out := make([]v1.GPULease, 0, len(payers))
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
