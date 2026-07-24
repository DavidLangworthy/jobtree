package controllers

// R14/R15 acceptance scenarios (docs/project/quota-semantics.md), driven
// through the engine: each test narrates one done-when from the decision
// record. The Tier-1 simulator the remediation plan referenced does not
// exist; these deterministic scenarios and the pkg/funding property suite
// stand in for it.

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

var qsBase = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

const qsFlavor = "H100-80GB"

type qsClock struct{ now time.Time }

func (c *qsClock) Now() time.Time { return c.now }

func qsNode(name string, gpus int) topology.SourceNode {
	return topology.SourceNode{
		Name: name,
		Labels: map[string]string{
			topology.LabelRegion:       "r1",
			topology.LabelCluster:      "c1",
			topology.LabelFabricDomain: "f1",
			topology.LabelGPUFlavor:    qsFlavor,
		},
		GPUs: gpus,
	}
}

func qsEnvelope(name string, concurrency int32) v1.BudgetEnvelope {
	return v1.BudgetEnvelope{
		Name:        name,
		Flavor:      qsFlavor,
		Selector:    map[string]string{topology.LabelRegion: "r1"},
		Concurrency: concurrency,
	}
}

// qsBudget places the budget in the DEFAULT namespace by default (R7: the run's
// funding owner is derived from its namespace, so a run in "default" needs its
// owner's budget there too). Tests with a SECOND distinct owner (family parent,
// sponsor) override ObjectMeta.Namespace to keep the two owners in separate
// namespaces — the engine treats co-located distinct owners as a conflict.
func qsBudget(name, owner string, envelopes ...v1.BudgetEnvelope) v1.Budget {
	return v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: keys.DefaultNamespace},
		Spec:       v1.BudgetSpec{Owner: owner, Envelopes: envelopes},
	}
}

// qsChildBudget declares the family edge: hierarchy comes from
// Spec.Parents, not from owner naming conventions.
func qsChildBudget(name, owner, parent string, envelopes ...v1.BudgetEnvelope) v1.Budget {
	b := qsBudget(name, owner, envelopes...)
	b.Spec.Parents = []string{parent}
	return b
}

// inNS relocates a budget to a namespace so a SECOND distinct owner (a family
// parent, a sponsor) never shares the run's namespace — the funding engine
// treats two owners in one namespace as a binding conflict (R7 §4).
func inNS(b v1.Budget, ns string) v1.Budget {
	b.Namespace = ns
	return b
}

func qsRun(name, owner string, gpus int32, created time.Time) *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{
			Name:              name,
			Namespace:         keys.DefaultNamespace,
			CreationTimestamp: v1.NewTime(created),
		},
		Spec: v1.RunSpec{
			Resources: v1.RunResources{GPUType: qsFlavor, TotalGPUs: gpus},
		},
	}
}

func qsState(nodes []topology.SourceNode, budgets []v1.Budget, runs ...*v1.Run) *ClusterState {
	state := &ClusterState{
		Nodes:        nodes,
		Budgets:      budgets,
		Runs:         map[string]*v1.Run{},
		Reservations: map[string]*v1.Reservation{},
	}
	for _, run := range runs {
		state.Runs[keys.NamespacedKey(run.Namespace, run.Name)] = run
	}
	return state
}

func qsReconcile(t *testing.T, state *ClusterState, clock *qsClock, name string) *v1.Run {
	t.Helper()
	c := NewRunController(state, clock)
	if err := c.Reconcile(keys.DefaultNamespace, name); err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return state.Runs[keys.NamespacedKey(keys.DefaultNamespace, name)]
}

// qsReconcileNS reconciles a run that lives OUTSIDE the default namespace (an
// owner tier whose budget the family/lending fixtures place in its own
// namespace so OwnerOf derives it).
func qsReconcileNS(t *testing.T, state *ClusterState, clock *qsClock, ns, name string) *v1.Run {
	t.Helper()
	c := NewRunController(state, clock)
	if err := c.Reconcile(ns, name); err != nil {
		t.Fatalf("reconcile %s/%s: %v", ns, name, err)
	}
	return state.Runs[keys.NamespacedKey(ns, name)]
}

func qsInt64(v int64) *int64 { return &v }

// Decision 1, admission lookahead: an envelope whose remaining integral
// cannot cover width x period admits nothing new — work is never born
// opportunistic. The run parks with a reservation instead of binding.
func TestScenarioZeroHourEnvelopeAdmitsNothing(t *testing.T) {
	env := qsEnvelope("zero-hours", 8)
	env.MaxGPUHours = qsInt64(0)
	state := qsState(
		[]topology.SourceNode{qsNode("n1", 8)},
		[]v1.Budget{qsBudget("team-budget", "team", env)},
		qsRun("starved", "team", 4, qsBase),
	)
	clock := &qsClock{now: qsBase}

	run := qsReconcile(t, state, clock, "starved")

	if run.Status.Phase != RunPhasePending {
		t.Fatalf("expected Pending (no born-opportunistic admission), got %s: %s", run.Status.Phase, run.Status.Message)
	}
	if run.Status.PendingReservation == nil {
		t.Fatalf("expected a reservation for the parked run, got none (message: %s)", run.Status.Message)
	}
	if n := len(state.Leases); n != 0 {
		t.Fatalf("expected no leases, got %d", n)
	}
}

// Decision 1, exhaustion demotes: a running job whose envelope integral
// runs out keeps its GPUs and keeps running — it reclassifies to unfunded
// (visible in status), and the envelope never overdrafts.
func TestScenarioExhaustionDemotesWithoutKilling(t *testing.T) {
	// Admission needs width x period (4 x 24h = 96) of remaining integral,
	// so the envelope is sized to fund the run at exactly one period; it
	// exhausts after 24h of running.
	env := qsEnvelope("metered", 8)
	env.MaxGPUHours = qsInt64(96)
	state := qsState(
		[]topology.SourceNode{qsNode("n1", 8)},
		[]v1.Budget{qsBudget("team-budget", "team", env)},
		qsRun("coaster", "team", 4, qsBase),
	)
	clock := &qsClock{now: qsBase}

	// Single-committer cutover: the scheduler plugin schedules and funds the
	// run (4x24h at start). seedRunning stands in for its mint, and the
	// reconcile derives the funding status the lifecycle assertions read.
	seedRunning(t, state, keys.NamespacedKey(keys.DefaultNamespace, "coaster"), qsBase)
	run := qsReconcile(t, state, clock, "coaster")
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("seeded run should be Running and funded at start, got %s: %s", run.Status.Phase, run.Status.Message)
	}
	if run.Status.Funding == nil || run.Status.Funding.OwnedGPUs != 4 {
		t.Fatalf("expected 4 owned GPUs at start, got %+v", run.Status.Funding)
	}

	// Coast past the integral: 4 GPUs drain 96 GPU-hours in 24h.
	clock.now = qsBase.Add(25 * time.Hour)
	run = qsReconcile(t, state, clock, "coaster")

	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("exhaustion must demote, not kill: got %s: %s", run.Status.Phase, run.Status.Message)
	}
	for i := range state.Leases {
		if state.Leases[i].Status.Closed {
			t.Fatalf("lease %s closed by exhaustion", state.Leases[i].Name)
		}
	}
	if run.Status.Funding == nil || run.Status.Funding.UnfundedGPUs != 4 || run.Status.Funding.OwnedGPUs != 0 {
		t.Fatalf("expected all 4 GPUs unfunded after exhaustion, got %+v", run.Status.Funding)
	}

	// No overdraft: the envelope's consumed hours are clamped at its cap.
	bc := NewBudgetController(&qsClock{now: clock.now}, NewBudgetMetrics())
	status := bc.ReconcileBudget(&state.Budgets[0], funding.Evaluate(funding.Input{
		Budgets: state.Budgets, Leases: state.Leases, Runs: state.Runs, Now: clock.now,
	}))
	if len(status.Usage) != 1 || status.Usage[0].ConsumedGPUHours > 96+1e-6 {
		t.Fatalf("overdraft must be unrepresentable: %+v", status.Usage)
	}
	if head := status.Headroom[0].GPUHours; head == nil || *head != 0 {
		t.Fatalf("expected zero GPU-hour headroom, got %v", head)
	}
}

// Decision 1, recovery is automatic: a run coasting unfunded after its
// window closed re-evaluates as funded when a new window opens. Nothing is
// resubmitted; the arithmetic changes.
func TestScenarioWindowReopenRefunds(t *testing.T) {
	env := qsEnvelope("windowed", 8)
	start := v1.NewTime(qsBase.Add(-time.Hour))
	end := v1.NewTime(qsBase.Add(2 * time.Hour))
	env.Start = &start
	env.End = &end
	state := qsState(
		[]topology.SourceNode{qsNode("n1", 8)},
		[]v1.Budget{qsBudget("team-budget", "team", env)},
		qsRun("seasonal", "team", 4, qsBase),
	)
	clock := &qsClock{now: qsBase}

	// Single-committer cutover: the plugin schedules and funds the run inside
	// the open window. seedRunning stands in for its mint; the reconcile derives
	// the funding status the coast/refund lifecycle below re-evaluates.
	seedRunning(t, state, keys.NamespacedKey(keys.DefaultNamespace, "seasonal"), qsBase)
	run := qsReconcile(t, state, clock, "seasonal")
	if run.Status.Phase != RunPhaseRunning || run.Status.Funding.OwnedGPUs != 4 {
		t.Fatalf("expected funded start, got %s %+v", run.Status.Phase, run.Status.Funding)
	}

	// Window closes: the run coasts opportunistically.
	clock.now = qsBase.Add(3 * time.Hour)
	run = qsReconcile(t, state, clock, "seasonal")
	if run.Status.Phase != RunPhaseRunning || run.Status.Funding.UnfundedGPUs != 4 {
		t.Fatalf("window expiry must coast, got %s %+v", run.Status.Phase, run.Status.Funding)
	}

	// A renewed window re-funds by evaluation, no demotion protocol.
	newStart := v1.NewTime(qsBase.Add(3 * time.Hour))
	newEnd := v1.NewTime(qsBase.Add(6 * time.Hour))
	state.Budgets[0].Spec.Envelopes[0].Start = &newStart
	state.Budgets[0].Spec.Envelopes[0].End = &newEnd
	clock.now = qsBase.Add(4 * time.Hour)
	run = qsReconcile(t, state, clock, "seasonal")
	if run.Status.Funding.OwnedGPUs != 4 || run.Status.Funding.UnfundedGPUs != 0 {
		t.Fatalf("reopened window must re-fund, got %+v", run.Status.Funding)
	}
}

// Decision 2: family excess funds without any lending policy, in proximity
// order, and classes as shared — visible to the lender — not borrowed.
func TestScenarioFamilySharesExcessWithoutLending(t *testing.T) {
	state := qsState(
		[]topology.SourceNode{qsNode("n1", 8)},
		[]v1.Budget{
			inNS(qsBudget("parent-budget", "org", qsEnvelope("parent-env", 8)), "org"),
			qsChildBudget("child-budget", "org/team", "org", qsEnvelope("child-env", 2)),
		},
		qsRun("hungry", "org/team", 6, qsBase),
	)
	clock := &qsClock{now: qsBase}

	// Single-committer cutover: the plugin schedules and funds the run from the
	// family's excess. seedRunning stands in for its mint; the reconcile derives
	// the classification the status surfaces (the substance this test asserts).
	seedRunning(t, state, keys.NamespacedKey(keys.DefaultNamespace, "hungry"), qsBase)
	run := qsReconcile(t, state, clock, "hungry")

	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("family excess should fund the run: %s: %s", run.Status.Phase, run.Status.Message)
	}
	f := run.Status.Funding
	if f.OwnedGPUs != 2 || f.SharedGPUs != 4 || f.BorrowedGPUs != 0 {
		t.Fatalf("expected 2 owned + 4 shared (never borrowed), got %+v", f)
	}
	if len(f.Lenders) != 1 || f.Lenders[0].Owner != "org" || f.Lenders[0].GPUs != 4 {
		t.Fatalf("lender attribution must name the family owner: %+v", f.Lenders)
	}
}

// Decision 2, owner recall: when the owner's admission needs headroom that
// family currently consumes on a full cluster, the family claim re-ranks
// unfunded and is reclaimed as the first cut — requeued as Pending, never
// Failed — and the owner binds. No funded stranger is lotteried.
func TestScenarioOwnerRecallReclaimsFamilyBorrower(t *testing.T) {
	// The child's own envelope is a different flavor, so it never funds this
	// H100 run: the child funds entirely from the parent's excess, and is
	// therefore fully recallable when the owner shows up. (The child budget
	// still exists, so the family edge is present.)
	childEnv := qsEnvelope("child-env", 8)
	childEnv.Flavor = "A100-40GB"
	state := qsState(
		[]topology.SourceNode{qsNode("n1", 8)},
		[]v1.Budget{
			inNS(qsBudget("parent-budget", "org", qsEnvelope("parent-env", 8)), "org"),
			qsChildBudget("child-budget", "org/team", "org", childEnv),
		},
		qsRun("squatter", "org/team", 6, qsBase),
	)
	clock := &qsClock{now: qsBase}
	// The family borrower is already scheduled and funded from the parent's
	// excess (the plugin's mint, stood in for by seedRunning); the reconcile
	// derives its 6 shared GPUs.
	seedRunning(t, state, keys.NamespacedKey(keys.DefaultNamespace, "squatter"), qsBase)
	squatterRun := qsReconcile(t, state, clock, "squatter")
	if squatterRun.Status.Phase != RunPhaseRunning || squatterRun.Status.Funding.SharedGPUs != 6 {
		t.Fatalf("setup: family run should start with 6 shared, got %s %+v", squatterRun.Status.Phase, squatterRun.Status.Funding)
	}

	// The owner arrives needing its whole envelope back.
	clock.now = qsBase.Add(time.Hour)
	owner := qsRun("landlord", "org", 8, clock.now)
	owner.Namespace = "org" // R7: the owner "org" derives from the parent budget's namespace
	state.Runs[keys.NamespacedKey(owner.Namespace, owner.Name)] = owner
	ownerRun := qsReconcileNS(t, state, clock, "org", "landlord")

	// The controller still runs the recall (reclaimForAdmission) on the owner's
	// fundable admission — the reclaim of the family borrower is what this
	// scenario asserts, and it still happens here. Under the single-committer
	// cutover the owner no longer binds in the controller: it emits its full
	// width of intent pods for the plugin and stays Pending, minting nothing.
	if ownerRun.Status.Phase != RunPhasePending {
		t.Fatalf("owner admission now emits intent and stays Pending, got %s: %s", ownerRun.Status.Phase, ownerRun.Status.Message)
	}
	if got := activeIntentPods(state, "org", "landlord"); got != 8 {
		t.Fatalf("owner should request its full 8-GPU envelope as intent pods, got %d", got)
	}
	if n := openLeaseCountForRun(state.Leases, keys.NamespacedKey("org", "landlord")); n != 0 {
		t.Fatalf("controller must mint nothing for the owner, got %d open leases", n)
	}
	squatter := state.Runs[keys.NamespacedKey(keys.DefaultNamespace, "squatter")]
	if squatter.Status.Phase != RunPhasePending {
		t.Fatalf("recalled family run must requeue as Pending, got %s: %s", squatter.Status.Phase, squatter.Status.Message)
	}
	if !strings.Contains(squatter.Status.Message, "reclaimed by funded demand") {
		t.Fatalf("victim message should say it was reclaimed by funded demand: %q", squatter.Status.Message)
	}
}

// Decision 2, opt-out: sharing "none" excludes an envelope from family
// excess while the owner still uses it freely.
func TestScenarioSharingNoneExcludesFamily(t *testing.T) {
	sealed := qsEnvelope("sealed", 8)
	sealed.Sharing = v1.SharingNone
	state := qsState(
		[]topology.SourceNode{qsNode("n1", 8)},
		[]v1.Budget{
			inNS(qsBudget("parent-budget", "org", sealed), "org"),
			qsChildBudget("child-budget", "org/team", "org", qsEnvelope("child-env", 2)),
		},
		qsRun("rebuffed", "org/team", 6, qsBase),
	)
	clock := &qsClock{now: qsBase}

	run := qsReconcile(t, state, clock, "rebuffed")

	if run.Status.Phase != RunPhasePending || run.Status.PendingReservation == nil {
		t.Fatalf("sharing none must exclude family excess: got %s %+v", run.Status.Phase, run.Status.Funding)
	}
}

// Decision 2, lending governs strangers only: family usage of an envelope
// leaves the sponsor lending caps untouched, and sponsor width classes
// borrowed under those caps.
func TestScenarioLendingCapsUnaffectedByFamilyUsage(t *testing.T) {
	lender := qsEnvelope("lender-env", 8)
	maxLend := int32(2)
	lender.Lending = &v1.LendingPolicy{Allow: true, To: []string{"guest*"}, MaxConcurrency: &maxLend}
	state := qsState(
		[]topology.SourceNode{qsNode("n1", 16)},
		[]v1.Budget{
			inNS(qsBudget("lender-budget", "org", lender), "org"),
			qsChildBudget("child-budget", "org/team", "org", qsEnvelope("child-env", 1)),
		},
		qsRun("family-user", "org/team", 5, qsBase),
	)
	clock := &qsClock{now: qsBase}
	// Single-committer cutover: the plugin schedules and funds the family run.
	// seedRunning stands in for its mint; the reconcile derives its 4 shared.
	seedRunning(t, state, keys.NamespacedKey(keys.DefaultNamespace, "family-user"), qsBase)
	if run := qsReconcile(t, state, clock, "family-user"); run.Status.Funding.SharedGPUs != 4 {
		t.Fatalf("setup: family should take 4 shared GPUs, got %+v", run.Status.Funding)
	}

	// A stranger (no budget, not family) borrows under the lending contract:
	// family's 4 shared GPUs must not have consumed the MaxConcurrency=2
	// lending cap.
	guest := qsRun("guest-run", "guest-lab", 2, qsBase.Add(time.Minute))
	guest.Namespace = "guest-lab" // R7: the stranger's namespace derives its own owner
	guest.Spec.Funding = &v1.RunFunding{AllowBorrow: true, Sponsors: []string{"org"}}
	state.Runs[keys.NamespacedKey(guest.Namespace, guest.Name)] = guest
	// A pure borrower still needs a bound namespace to derive an owner from (the
	// empty-borrower guard, R7 §4). A nominal off-flavor envelope binds
	// "guest-lab" to owner "guest-lab" without funding any of this H100 run, so
	// every GPU it gets is borrowed from the sponsor.
	guestBudget := inNS(qsBudget("guest-budget", "guest-lab", qsEnvelope("guest-nominal", 1)), "guest-lab")
	guestBudget.Spec.Envelopes[0].Flavor = "A100-40GB"
	state.Budgets = append(state.Budgets, guestBudget)

	clock.now = qsBase.Add(2 * time.Minute)
	// The stranger is likewise scheduled and funded by the plugin; seedRunning
	// mints its borrow (2 GPUs, unblocked by family usage) so the reconcile can
	// classify it. The borrowed-class number is the substance this test asserts.
	seedRunning(t, state, keys.NamespacedKey("guest-lab", "guest-run"), clock.now)
	guestRun := qsReconcileNS(t, state, clock, "guest-lab", "guest-run")

	if guestRun.Status.Phase != RunPhaseRunning {
		t.Fatalf("lending caps are for strangers and must still admit 2: %s: %s", guestRun.Status.Phase, guestRun.Status.Message)
	}
	if guestRun.Status.Funding.BorrowedGPUs != 2 {
		t.Fatalf("sponsor width must class borrowed, got %+v", guestRun.Status.Funding)
	}
}

// The one derivation feeds every surface: the budget status usage block,
// the run funding status, and the headroom agree about the same instant.
func TestScenarioStatusSurfacesAgree(t *testing.T) {
	state := qsState(
		[]topology.SourceNode{qsNode("n1", 8)},
		[]v1.Budget{
			inNS(qsBudget("parent-budget", "org", qsEnvelope("parent-env", 8)), "org"),
			qsChildBudget("child-budget", "org/team", "org", qsEnvelope("child-env", 2)),
		},
		qsRun("hungry", "org/team", 6, qsBase),
	)
	clock := &qsClock{now: qsBase}
	// Single-committer cutover: the plugin schedules and funds the run.
	// seedRunning stands in for its mint so the leases exist; the reconcile
	// derives the run funding status, and the same evaluation feeds the budget
	// status below — the one derivation every surface must agree on.
	seedRunning(t, state, keys.NamespacedKey(keys.DefaultNamespace, "hungry"), qsBase)
	run := qsReconcile(t, state, clock, "hungry")

	ev := funding.Evaluate(funding.Input{
		Budgets: state.Budgets, Leases: state.Leases, Runs: state.Runs, Now: clock.now,
	})
	bc := NewBudgetController(clock, NewBudgetMetrics())
	parentStatus := bc.ReconcileBudget(&state.Budgets[0], ev)

	if len(parentStatus.Usage) != 1 || parentStatus.Usage[0].SharedGPUs != run.Status.Funding.SharedGPUs {
		t.Fatalf("lender's usage block must show the shared width the run reports: %+v vs %+v",
			parentStatus.Usage, run.Status.Funding)
	}
	if parentStatus.Headroom[0].Concurrency != 8-4 {
		t.Fatalf("funded family width consumes the lender's headroom: %+v", parentStatus.Headroom)
	}
}
