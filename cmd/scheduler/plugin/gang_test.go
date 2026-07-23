package plugin

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func testScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("core scheme: %v", err)
	}
	if err := v1.AddToScheme(s); err != nil {
		t.Fatalf("v1 scheme: %v", err)
	}
	return s
}

func gpuNode(name string, gpus int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			topology.LabelRegion:       "us-west",
			topology.LabelCluster:      "cluster-a",
			topology.LabelFabricDomain: "island-a",
			topology.LabelGPUFlavor:    "H100-80GB",
		}},
		Status: corev1.NodeStatus{
			Capacity:   corev1.ResourceList{gpuResource: *resource.NewQuantity(gpus, resource.DecimalSI)},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func teamBudget(concurrency int32) *v1.Budget {
	return &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "team"},
		Spec: v1.BudgetSpec{Owner: "org:ai:team", Envelopes: []v1.BudgetEnvelope{{
			Name: "west", Flavor: "H100-80GB", Concurrency: concurrency,
			Selector: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
		}}},
	}
}

func trainRun() *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
	}
}

func gangPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "train-pod-0",
			Labels:      map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive},
			Annotations: map[string]string{binder.AnnotationGPUs: "4", binder.AnnotationExpectedWidth: "1", binder.AnnotationFlavor: "H100-80GB"},
		},
	}
}

func newManager(t *testing.T, objs ...client.Object) *gangManager {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return newGangManager(c, func() time.Time { return now })
}

// A gang that fits and funds: decide returns fundable and claimPayer hands out
// the paying envelope for a pod's mint.
func TestGangDecideFundable(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 4))
	pod := gangPod()

	fundable, reason := m.decide(context.Background(), pod)
	if !fundable {
		t.Fatalf("expected fundable, got not-fundable: %s", reason)
	}
	seg, gpusPerPod, ok := m.claimPayer(pod)
	if !ok {
		t.Fatalf("claimPayer returned !ok for a fundable gang")
	}
	if seg.Owner != "org:ai:team" || seg.EnvelopeName != "west" {
		t.Errorf("payer = %s/%s, want org:ai:team/west", seg.Owner, seg.EnvelopeName)
	}
	if gpusPerPod != 4 {
		t.Errorf("gpusPerPod = %d, want 4", gpusPerPod)
	}
	// Idempotent per pod: re-claiming for the same pod returns the same payer
	// (a PreBind retry must not consume another).
	if seg2, _, ok := m.claimPayer(pod); !ok || seg2.EnvelopeName != seg.EnvelopeName {
		t.Errorf("expected idempotent re-claim for the same pod, got ok=%v seg=%s", ok, seg2.EnvelopeName)
	}
	// Only one pod's worth of funding: a different pod overflows the 1-pod gang.
	other := gangPod()
	other.Name = "train-pod-1"
	if _, _, ok := m.claimPayer(other); ok {
		t.Errorf("expected the 1-pod gang's funding to be exhausted for a second distinct pod")
	}
}

// R4 hot-path observability: a gang decision records its latency (labeled by
// outcome) and the size of the ledger it fed to the funding replay — the two
// signals that make the caching/compaction cost observable.
func TestDecideObservesHotPathMetrics(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)

	// One unrelated open lease so the ledger fed to the replay is non-empty: the
	// evaluate-input-size gauge must report it (the O(history) cost signal).
	sibling := &v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Name: "sibling-lease", Namespace: "default"},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:ai:team",
			RunRef:         v1.RunReference{Name: "sibling", Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-a#0", "node-a#1"}, Role: binder.RoleActive},
			PaidByBudget:   "team",
			PaidByEnvelope: "west",
		},
	}
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 8), sibling)

	if fundable, reason := m.decide(context.Background(), gangPod()); !fundable {
		t.Fatalf("expected fundable, got: %s", reason)
	}
	snap := metrics.Snapshot()
	if got := snap.DecideLatency["fundable"].Count; got != 1 {
		t.Errorf("expected one fundable decide observed, got count %d", got)
	}
	if snap.EvaluateInputSize != 1 {
		t.Errorf("expected evaluate-input-size gauge = 1 (the one seeded lease fed to the replay), got %v", snap.EvaluateInputSize)
	}

	// An unfundable decision is labeled distinctly.
	m2 := newManager(t, trainRun(), teamBudget(0), gpuNode("node-a", 8))
	if fundable, _ := m2.decide(context.Background(), gangPod()); fundable {
		t.Fatalf("expected not-fundable (zero-concurrency envelope)")
	}
	if got := metrics.Snapshot().DecideLatency["unfundable"].Count; got != 1 {
		t.Errorf("expected one unfundable decide observed, got count %d", got)
	}
}

// A gang whose flavor has no capacity anywhere cannot place: decide is not
// fundable (the plugin will reject → controller reserves).
func TestGangDecideUnfundableNoCapacity(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 1)) // only 1 GPU, run needs 4
	pod := gangPod()

	if fundable, _ := m.decide(context.Background(), pod); fundable {
		t.Fatalf("expected not-fundable (insufficient capacity)")
	}
	if _, _, ok := m.claimPayer(pod); ok {
		t.Errorf("claimPayer should return !ok for an unfundable gang")
	}
}

// A budget with zero concurrency funds nothing: decide is not fundable even
// though the topology fits.
func TestGangDecideUnfundableNoBudget(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(0), gpuNode("node-a", 4))
	if fundable, _ := m.decide(context.Background(), gangPod()); fundable {
		t.Fatalf("expected not-fundable (zero-concurrency envelope)")
	}
}

// A grow cohort is a distinct gang from the base and funds its DELTA against the
// live ledger (which already holds the base lease), so growing a run does not
// re-gate the whole run.
func TestGangKeyCohort(t *testing.T) {
	base := gangPod()
	grow := gangPod()
	grow.Name = "train-c1-pod-0"
	grow.Annotations[binder.AnnotationCohort] = "1"

	if gangKey(base) == gangKey(grow) {
		t.Errorf("base and grow cohort must have distinct gang keys, both %q", gangKey(base))
	}
	if isGrowCohort(base) {
		t.Errorf("base pod (no cohort) must not be a grow cohort")
	}
	if !isGrowCohort(grow) {
		t.Errorf("cohort=1 pod must be a grow cohort")
	}
	// cohort "0" is explicitly the base.
	base0 := gangPod()
	base0.Annotations[binder.AnnotationCohort] = "0"
	if gangKey(base0) != gangKey(base) || isGrowCohort(base0) {
		t.Errorf("cohort 0 must be treated as the base gang")
	}
}

// decide funds a grow cohort's delta incrementally: with the base run's lease
// already on an 8-GPU node, a +4 grow cohort funds against the remaining 4.
func TestGangDecideGrowCohortFundsDelta(t *testing.T) {
	baseLease := &v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Name: "train-base-lease", Namespace: "default"},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:ai:team",
			RunRef:         v1.RunReference{Name: "train", Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-a#0", "node-a#1", "node-a#2", "node-a#3"}, Role: binder.RoleActive},
			PaidByBudget:   "team",
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 8), baseLease)

	grow := gangPod()
	grow.Name = "train-c1-pod-0"
	grow.Annotations[binder.AnnotationCohort] = "1"

	fundable, reason := m.decide(context.Background(), grow)
	if !fundable {
		t.Fatalf("expected the +4 grow cohort to fund against the free 4 GPUs, got: %s", reason)
	}
	if _, _, ok := m.claimPayer(grow); !ok {
		t.Errorf("grow cohort should hand out a payer")
	}
}

// A held spare is funded by the base gang's cover (which already pays for
// active+spares) — decide hands out one payer per active pod AND one per spare,
// and verdict() reports the gang funded so a spare can allow itself without
// gating the active width.
func TestGangSpareFundedByBaseCover(t *testing.T) {
	spares := int32(1)
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:ai:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 2},
			Spares:    &spares,
		},
	}
	m := newManager(t, run, teamBudget(8), gpuNode("node-a", 3)) // 2 active + 1 spare fit

	onePod := func(name, role string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        name,
			Labels:      map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: role},
			Annotations: map[string]string{binder.AnnotationGPUs: "1", binder.AnnotationExpectedWidth: "2", binder.AnnotationFlavor: "H100-80GB"},
		}}
	}
	active0 := onePod("train-active-0", binder.RoleActive)

	fundable, reason := m.decide(context.Background(), active0)
	if !fundable {
		t.Fatalf("expected the base gang (2 active + 1 spare) to fund, got: %s", reason)
	}
	// Two active pods AND one spare each claim a payer — the cover funded all 3.
	for _, p := range []*corev1.Pod{active0, onePod("train-active-1", binder.RoleActive), onePod("train-spare-0", binder.RoleSpare)} {
		if _, _, ok := m.claimPayer(p); !ok {
			t.Errorf("pod %s (role %s) should claim a payer from the active+spares cover", p.Name, p.Labels[binder.LabelRunRole])
		}
	}
	// A 4th distinct pod overflows the funded active+spares footprint.
	if _, _, ok := m.claimPayer(onePod("train-spare-1", binder.RoleSpare)); ok {
		t.Errorf("funding should be exhausted after 2 active + 1 spare (cover was 3 GPUs)")
	}
	// verdict lets a spare allow itself: the gang is decided and fundable.
	if fundable, decided := m.verdict(onePod("train-spare-0", binder.RoleSpare)); !decided || !fundable {
		t.Errorf("verdict = (fundable=%v, decided=%v), want (true, true)", fundable, decided)
	}
}

// verdict does not trigger a decision: an undecided gang reports decided=false so
// a spare parks (Wait) rather than deciding the gang before its active width forms.
func TestGangVerdictUndecided(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 4))
	spare := gangPod()
	spare.Labels[binder.LabelRunRole] = binder.RoleSpare
	if _, decided := m.verdict(spare); decided {
		t.Errorf("verdict on an untouched gang must report decided=false")
	}
	if _, ok := m.gangs[gangKey(spare)]; ok {
		t.Errorf("verdict must not create gang state")
	}
}

// forget clears an undecided/unclaimed gang so a retry re-derives; it must not
// drop a gang that has already handed out a payer (its lease is being minted).
func TestGangForget(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 4))
	pod := gangPod()

	m.decide(context.Background(), pod)
	m.claimPayer(pod) // gang now has claimed==1
	m.forget(pod)
	if _, ok := m.gangs[gangKey(pod)]; !ok {
		t.Errorf("forget dropped a gang with an outstanding claim")
	}
}

func train2Run() *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train2", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
	}
}

// mintPodLease simulates what PreBind does for one pod: claim its payer, create
// the REAL lease in the API, and notify the gang so its phantom is retired.
func mintPodLease(t *testing.T, ctx context.Context, c client.Client, m *gangManager, pod *corev1.Pod, node string) {
	t.Helper()
	seg, gpus, ok := m.claimPayer(pod)
	if !ok {
		t.Fatalf("claimPayer !ok for %s", pod.Name)
	}
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Namespace: pod.Namespace, Name: pod.Labels[binder.LabelRunName]}}
	role := pod.Labels[binder.LabelRunRole]
	lease := admission.PodLeaseWithRole(run, seg, node, gpus, pod.Name+"-lease", m.clock(), "Start", role, pod.Labels[binder.LabelGroupIndex])
	admission.StampGangIdentity(&lease, cohortOf(pod), pod.Name)
	if err := c.Create(ctx, &lease); err != nil {
		t.Fatalf("create real lease for %s: %v", pod.Name, err)
	}
	m.notifyMinted(pod)
}

// R1: once a gang's real leases exist, its phantom pending leases must be retired
// so a later gang is not falsely rejected by the gang's own funding counted
// twice. Two 4-GPU runs share an 8-GPU node + concurrency-8 envelope: after A
// mints, B must fund against the free 4. Before R1, A counted 8 (real 4 +
// phantom 4) and B was rejected "insufficient capacity".
func TestGangNoDoubleCountAfterMint(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(trainRun(), train2Run(), teamBudget(8), gpuNode("node-a", 8)).Build()
	m := newGangManager(c, func() time.Time { return now })

	podA := gangPod()
	if fundable, reason := m.decide(ctx, podA); !fundable {
		t.Fatalf("gang A should fund: %s", reason)
	}
	mintPodLease(t, ctx, c, m, podA, "node-a")

	podB := gangPod()
	podB.Labels[binder.LabelRunName] = "train2"
	podB.Name = "train2-pod-0"
	if fundable, reason := m.decide(ctx, podB); !fundable {
		t.Fatalf("gang B should fund against the free 4 GPUs once A's phantom is retired; got: %q (R1 double-count regression)", reason)
	}
}

// R1 guard intact: the phantom fold must still prevent overspend BEFORE the real
// lease exists. With a concurrency-4 envelope (room for exactly one 4-GPU gang),
// A's decide reserves the whole envelope via its phantom; B, deciding before A
// mints, must be refused.
func TestGangPhantomGuardsUntilMint(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(trainRun(), train2Run(), teamBudget(4), gpuNode("node-a", 8)).Build()
	m := newGangManager(c, func() time.Time { return now })

	if fundable, reason := m.decide(ctx, gangPod()); !fundable {
		t.Fatalf("gang A should fund: %s", reason)
	}
	// A has NOT minted; its phantom must still occupy the whole concurrency-4
	// envelope so B cannot overspend it.
	podB := gangPod()
	podB.Labels[binder.LabelRunName] = "train2"
	podB.Name = "train2-pod-0"
	if fundable, _ := m.decide(ctx, podB); fundable {
		t.Fatalf("gang B must be refused while A's phantom still guards the envelope (decide→mint window)")
	}
}

// R1: PostBind GCs a gang only once all its pods have minted; until then (e.g. a
// member still unbound) the commit survives for recovery.
func TestGangPostBindGCsFullyMintedGang(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(trainRun(), teamBudget(8), gpuNode("node-a", 4)).Build()
	m := newGangManager(c, func() time.Time { return now })

	pod := gangPod()
	m.decide(ctx, pod)
	mintPodLease(t, ctx, c, m, pod, "node-a")

	// Minted but not yet PostBound: the commit must persist (R2 recovery needs it).
	if _, ok := m.gangs[gangKey(pod)]; !ok {
		t.Fatalf("gang dropped before PostBind")
	}
	m.postBind(pod)
	if _, ok := m.gangs[gangKey(pod)]; ok {
		t.Errorf("PostBind should GC a fully-minted 1-pod gang")
	}
}

// R2: committedCount reports how many pods have claimed a payer, so Permit can
// count already-committed siblings toward the gang width and let a lone survivor
// re-assemble after a transient failure instead of wedging. It is 0 before any
// pod commits (so the first funding decision still needs the full waiting set).
func TestGangCommittedCount(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, trainRun2Wide(), teamBudget(8), gpuNode("node-a", 4))
	key := gangKey(twoWidePod("train-pod-0"))

	if got := m.committedCount(key); got != 0 {
		t.Fatalf("committedCount before decide = %d, want 0", got)
	}
	m.decide(ctx, twoWidePod("train-pod-0"))
	if got := m.committedCount(key); got != 0 {
		t.Errorf("committedCount after decide but before any claim = %d, want 0", got)
	}
	m.claimPayer(twoWidePod("train-pod-0"))
	if got := m.committedCount(key); got != 1 {
		t.Errorf("committedCount after 1 claim = %d, want 1", got)
	}
	m.claimPayer(twoWidePod("train-pod-1"))
	if got := m.committedCount(key); got != 2 {
		t.Errorf("committedCount after 2 claims = %d, want 2 (Permit: waiting+committed>=width de-wedges a lost member)", got)
	}
}

// R5 defense-in-depth: a swap may only mint from provenance that matches a real
// Spare lease the run held; a forged swap pod carrying a victim envelope it never
// had a spare in is refused.
func TestSpareLeaseProvenanceValid(t *testing.T) {
	ctx := context.Background()
	spare := &v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Name: "train-spare-lease", Namespace: "default"},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:ai:team",
			RunRef:         v1.RunReference{Name: "train", Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-b#0"}, Role: binder.RoleSpare},
			PaidByBudget:   "team",
			PaidByEnvelope: "west",
		},
	}
	m := newManager(t, trainRun(), spare)

	good := cover.Segment{Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "west"}
	if !m.spareLeaseProvenanceValid(ctx, "default", "train", good) {
		t.Errorf("provenance matching the run's real spare lease should be accepted")
	}
	// A forged provenance charging a different (victim) envelope has no matching
	// spare for this run → refused.
	forged := cover.Segment{Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "victim-east"}
	if m.spareLeaseProvenanceValid(ctx, "default", "train", forged) {
		t.Errorf("provenance with no matching spare lease must be refused (forgery)")
	}
	// An ACTIVE lease with the right provenance does not count — only a held Spare
	// is a legitimate swap target.
	active := spare.DeepCopy()
	active.Name = "train-active-lease"
	active.Spec.Slice.Role = binder.RoleActive
	m2 := newManager(t, trainRun(), active)
	if m2.spareLeaseProvenanceValid(ctx, "default", "train", good) {
		t.Errorf("an Active lease must not satisfy a swap provenance check")
	}
}

// R3/R5 defense-in-depth (with the mandatory-scheduler VAP off): a promised
// opportunistic activation may only charge an envelope its own run's owner owns.
// The load-bearing fields are the CHARGED ones (payer-budget/payer-envelope),
// because funding.Evaluate resolves the charge by them and takes the owner from
// the real Budget — never from the lease's cosmetic Spec.Owner. So a forged
// promise pod that owns its own run but points payer-budget/envelope at a
// victim's budget must be refused, or it launders a gate-free charge onto the
// victim.
func TestPromiseProvenanceValid(t *testing.T) {
	ctx := context.Background()
	victim := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "victim"},
		Spec: v1.BudgetSpec{Owner: "org:ai:victim", Envelopes: []v1.BudgetEnvelope{{
			Name: "victim-west", Flavor: "H100-80GB", Concurrency: 8,
		}}},
	}
	m := newManager(t, trainRun(), teamBudget(8), victim)

	// The run's own envelope: accepted (opportunisticCoverPlan only ever
	// attributes a promise to an envelope the run's owner owns).
	good := cover.Segment{Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "west"}
	if !m.promiseProvenanceValid(ctx, "default", "train", good) {
		t.Errorf("provenance charging the run owner's own real envelope should be accepted")
	}
	// THE exploit: seg.Owner is set to the run's own owner (so a naive owner-only
	// check would pass), but payer-budget/envelope point at a DIFFERENT owner's
	// budget — the field that actually gets charged. Must be refused.
	stealCharge := cover.Segment{Owner: "org:ai:team", BudgetName: "victim", EnvelopeName: "victim-west"}
	if m.promiseProvenanceValid(ctx, "default", "train", stealCharge) {
		t.Errorf("a promise charging another owner's budget must be refused even when seg.Owner matches the run (gate-free cross-tenant charge)")
	}
	// seg.Owner inconsistent with the run: refused (keeps the minted lease's
	// Spec.Owner honest).
	wrongOwner := cover.Segment{Owner: "org:victim", BudgetName: "team", EnvelopeName: "west"}
	if m.promiseProvenanceValid(ctx, "default", "train", wrongOwner) {
		t.Errorf("provenance whose owner is not the run's owner must be refused")
	}
	// A budget owned by the run but WITHOUT the named envelope: refused (the
	// charge would land on a non-existent envelope).
	noEnvelope := cover.Segment{Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "east"}
	if m.promiseProvenanceValid(ctx, "default", "train", noEnvelope) {
		t.Errorf("provenance naming an envelope the budget does not carry must be refused")
	}
	// A budget that does not exist: refused.
	noBudget := cover.Segment{Owner: "org:ai:team", BudgetName: "ghost", EnvelopeName: "west"}
	if m.promiseProvenanceValid(ctx, "default", "train", noBudget) {
		t.Errorf("provenance naming a nonexistent budget must be refused")
	}
	// No such run → refused: there is nothing to authorize the mint against.
	if m.promiseProvenanceValid(ctx, "default", "ghost", good) {
		t.Errorf("a promise for a nonexistent run must be refused")
	}
}

// trainRun2Wide is a 2-GPU run whose gang is 2 pods of 1 GPU each — width 2, so a
// single lost member is a partial gang the committed-count must be able to heal.
func trainRun2Wide() *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 2}},
	}
}

func twoWidePod(name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace:   "default",
		Name:        name,
		Labels:      map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive},
		Annotations: map[string]string{binder.AnnotationGPUs: "1", binder.AnnotationExpectedWidth: "2", binder.AnnotationFlavor: "H100-80GB"},
	}}
}

// R1: a gang with an unbound member is NOT GC'd by PostBind of the bound members
// (it stays for recovery), and the TTL sweep is what eventually reclaims it if it
// is abandoned. A fresh gang is never swept.
func TestGangSweepDropsIdleGangOnly(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(trainRun(), teamBudget(8), gpuNode("node-a", 4)).Build()
	m := newGangManager(c, func() time.Time { return now })

	key := gangKey(gangPod())
	m.decide(ctx, gangPod())

	m.sweep(now.Add(gangTTL - time.Minute)) // still fresh
	if _, ok := m.gangs[key]; !ok {
		t.Fatalf("sweep reaped a gang inside its TTL")
	}
	m.sweep(now.Add(gangTTL + time.Minute)) // idle past TTL
	if _, ok := m.gangs[key]; ok {
		t.Errorf("sweep should reap a gang idle past gangTTL")
	}
}
