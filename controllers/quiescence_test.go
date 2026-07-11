package controllers

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/invariant"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// The quiescence driver (R27b).
//
// pkg/invariant is an oracle over states, and it can only judge the states
// something actually builds. Every test in this package builds the state its
// author had in mind — which is exactly the set of states its author already
// understood. Two of the defects on the node-failure path were invisible to the
// oracle for that reason alone: no test ever assembled a run holding both a
// funded and an unfunded lease, so nothing asked what the swap would do to it.
//
// This file builds states nobody had in mind. It generates sequences of legal
// external events over a deliberately tiny universe — two owners, three nodes,
// a handful of runs — drives them into the engine, settles to a fixpoint, and
// asserts the oracle after every single step.
//
// # What "legal" means here, and why it is the whole design
//
// A generator that pokes arbitrary values into ClusterState finds nothing but
// its own lies: it fabricates a state the engine cannot produce, the oracle
// (rightly) rejects it, and a day is spent proving the generator wrong. So the
// driver may only do what the real world does:
//
//   - kube creates and deletes Nodes, and an admin edits a Budget.
//   - the kubelet moves a pod to Succeeded.
//   - the SOLE COMMITTER — the scheduler plugin, at PreBind — is the only thing
//     that mints a Lease. mintPending below is that plugin, and it mints exactly
//     what cmd/scheduler/plugin does: one lease per unminted pod, named for the
//     pod, stamped with the pod's role and placement group.
//   - the engine decides everything else.
//
// Nothing in this file writes Lease.Status, sets a run Phase, or deletes a pod.
// Those are the engine's, and letting a test do them is how a fixture comes to
// be richer than reality.
//
// # Failure output
//
// The oracle panics. The driver recovers, prints the seed and the exact op log
// that reached the illegal state, and fails. Replay with:
//
//	JOBTREE_QUIESCENCE_SEED=<n> go test ./controllers -run Quiescence -v

const (
	qNamespace = "default"
	// The universe. Small on purpose: a bug that needs four nodes to appear needs
	// three, and the state space of three is one a human can still read.
	qOwnerAlpha = "org:ai:alpha"
	qOwnerBeta  = "org:ai:beta"
)

func qNodes() []topology.SourceNode {
	mk := func(name string) topology.SourceNode {
		return topology.SourceNode{Name: name, GPUs: 4, Labels: map[string]string{
			topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
			topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB",
		}}
	}
	return []topology.SourceNode{mk("node-a"), mk("node-b"), mk("node-c")}
}

func qBudget(name, owner string, concurrency int32) v1.Budget {
	return v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: name},
		Spec: v1.BudgetSpec{Owner: owner, Envelopes: []v1.BudgetEnvelope{{
			Name: "west", Flavor: "H100-80GB", Concurrency: concurrency,
			Selector: map[string]string{
				topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
				topology.LabelFabricDomain: "island-a",
			},
		}}},
	}
}

// qWorld is one generated history: a cluster, a clock, and the log of what was
// done to it.
type qWorld struct {
	t     *testing.T
	c     *RunController
	state *ClusterState
	now   time.Time
	rng   *rand.Rand
	ops   []string
	runs  int
}

func (w *qWorld) logf(format string, args ...any) {
	w.ops = append(w.ops, fmt.Sprintf(format, args...))
}

func (w *qWorld) tick(d time.Duration) {
	w.now = w.now.Add(d)
	w.c.Clock = runClock{now: w.now}
}

// check asserts the oracle outside an engine call. The engine entry points
// already assert it on return; this catches the steps in between — a mint, a
// node deletion, a pod succeeding — because a state that is illegal between two
// engine calls is a state some other controller can observe.
func (w *qWorld) check(site string) {
	invariant.Check("quiescence/"+site, invariant.World{}, w.c.snapshotWorld())
}

// --- the world's own events -------------------------------------------------

func (w *qWorld) submitRun() {
	if w.runs >= 4 {
		return
	}
	name := fmt.Sprintf("run-%d", w.runs)
	w.runs++
	owner := qOwnerAlpha
	if w.rng.Intn(2) == 0 {
		owner = qOwnerBeta
	}
	gpus := []int32{1, 2, 4}[w.rng.Intn(3)]
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: qNamespace,
			CreationTimestamp: v1.NewTime(w.now.Add(-time.Hour))},
		Spec: v1.RunSpec{Owner: owner, Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: gpus}},
	}
	// A malleable run may legally run below TotalGPUs, and it is the shape that
	// makes the width invariant subtle: the resolver may cut its base group while
	// its grow ranks still cover the minimum.
	if gpus == 4 && w.rng.Intn(2) == 0 {
		run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 2, MaxTotalGPUs: 4, StepGPUs: 2}
		w.logf("submit %s owner=%s gpus=%d malleable=[2,4]", name, owner, gpus)
	} else {
		w.logf("submit %s owner=%s gpus=%d", name, owner, gpus)
	}
	// Some runs ask for a checkpoint grace: that is the one legal half-plane state
	// (pods alive, group's lease closed, run parked Pending), and the driver must
	// walk through it rather than around it.
	if w.rng.Intn(3) == 0 {
		run.Spec.Runtime = &v1.RunRuntime{Checkpoint: metav1.Duration{Duration: 10 * time.Minute}}
		w.logf("  (checkpoint grace 10m)")
	}
	// SPARES AND GROUPS ARE NOT OPTIONAL DECORATION. A run with no spare cannot
	// swap, and a swap is what HandleNodeFailure does on the path that has shipped
	// a defect on each of its last three changes: findSpareLease, reclaimSquatter,
	// the phase fold. A run with one group cannot tell "this group" from "the whole
	// run", which is the entire content of R28b. The first version of this driver
	// generated neither, ran green in a quarter of a second, and proved nothing.
	if w.rng.Intn(2) == 0 {
		spares := int32(1)
		run.Spec.Spares = &spares
		w.logf("  (1 spare per group)")
	}
	if gpus == 4 && w.rng.Intn(2) == 0 {
		group := int32(2)
		run.Spec.Locality = &v1.RunLocality{GroupGPUs: &group}
		w.logf("  (groupGPUs 2 — two placement groups)")
	}
	w.state.Runs[keys.NamespacedKey(qNamespace, name)] = run
}

// mintPending is the scheduler plugin. It is the SOLE COMMITTER, and the only
// thing in this file that creates a Lease.
//
// It mints for a pod exactly once, named as PreBind names it, and it derives the
// payer the way PreBind does: from the pod's carried provenance when it has any
// (swap and promise pods), and otherwise from the pod's own run's owner, which is
// what the gang manager's funding gate resolves for a self-funded gang.
func (w *qWorld) mintPending() {
	minted := map[string]bool{}
	for i := range w.state.Leases {
		minted[w.state.Leases[i].Name] = true
	}
	for i := range w.state.Pods {
		pod := &w.state.Pods[i]
		name := pod.Name + "-lease"
		if minted[name] || pod.Phase == binder.PodPhaseSucceeded {
			continue
		}
		run := w.state.Runs[keys.NamespacedKey(pod.Namespace, pod.Labels[binder.LabelRunName])]
		if run == nil {
			continue
		}
		node := pod.NodeName
		if node == "" {
			// The real scheduler's NodeResourcesFit filter binds a pod only to a
			// node with the GPUs free. A uniformly random pick over the STATIC node
			// list fabricated the impossible — an oversubscribed node, or a node
			// already deleted — in a large fraction of states, and a generator that
			// lies finds nothing but its own lies. Pick a live node with room, or
			// leave the pod Pending (Unschedulable), exactly as PreBind would.
			node = w.pickNodeWithCapacity(pod.GPUs)
			if node == "" {
				continue
			}
		}
		seg := cover.Segment{
			Owner:        pod.Annotations[binder.AnnotationPayerOwner],
			BudgetName:   pod.Annotations[binder.AnnotationPayerBudget],
			EnvelopeName: pod.Annotations[binder.AnnotationPayerEnvelope],
		}
		if seg.Owner == "" {
			seg = cover.Segment{Owner: run.Spec.Owner, BudgetName: qBudgetFor(run.Spec.Owner), EnvelopeName: "west"}
		}
		// PreBind refuses to mint a lease for a pod carrying no placement group.
		// The driver holds it to the same refusal: a pod the plugin would reject
		// is a pod the controller should never have emitted.
		group := pod.Labels[binder.LabelGroupIndex]
		if group == "" {
			w.fail("the controller emitted pod %s with no %s label; the plugin refuses to mint for it (ErrNoPlacementGroup)",
				pod.Name, binder.LabelGroupIndex)
		}
		lease := admission.PodLeaseWithRole(run, seg, node, pod.GPUs, name, w.now,
			pod.Annotations[binder.AnnotationLeaseReason], pod.Labels[binder.LabelRunRole], group)
		w.state.Leases = append(w.state.Leases, lease)
		w.logf("mint %s -> %s on %s (role=%s group=%s)", pod.Name, name, node, lease.Spec.Slice.Role, group)
	}
}

// pickNodeWithCapacity chooses a live node whose free GPUs (capacity minus the
// GPUs already claimed by open leases on it) can hold `gpus`, or "" when none can
// — the scheduler's fit filter, modelled. Occupancy is counted per node from open
// leases' slots, which is faithful to how the plugin lays chunk-local slots down.
func (w *qWorld) pickNodeWithCapacity(gpus int) string {
	used := map[string]int{}
	for i := range w.state.Leases {
		l := &w.state.Leases[i]
		if l.Status.Closed {
			continue
		}
		for _, slot := range l.Spec.Slice.Nodes {
			used[nodeFromSlot(slot)]++
		}
	}
	var free []string
	for _, n := range w.state.Nodes {
		if int(n.GPUs)-used[n.Name] >= gpus {
			free = append(free, n.Name)
		}
	}
	if len(free) == 0 {
		return ""
	}
	return free[w.rng.Intn(len(free))]
}

// NOTE: an EXTERNAL pod-deletion event (drain / eviction / preemption / GC) is a
// legal thing the real world does and this driver deliberately does NOT yet do —
// wiring it in refutes INV-LEASE-HAS-POD today, because a Running run whose active
// pod is evicted on a healthy node is never repaired (topUpActiveGang runs only on
// pre-Running assembly, 9A-3 Retry of a *Failed* pod, and reservation activation —
// never on a Running run's externally-lost pod). The lease then bills a budget for
// a pod that no longer exists. That reconciliation gap is tracked as its own task;
// the eviction event lands together with the engine fix that closes it, so the
// driver goes from "cannot reach the state" straight to "reaches it and it is
// legal because the engine now repairs it".
func qBudgetFor(owner string) string {
	if owner == qOwnerAlpha {
		return "alpha"
	}
	return "beta"
}

// deleteNode is the only fencing signal the engine honours: a Node object that is
// gone. A cordon is NOT a failure and cannot be modelled here, which is the point
// — see R21.
func (w *qWorld) deleteNode() {
	if len(w.state.Nodes) == 0 {
		return
	}
	idx := w.rng.Intn(len(w.state.Nodes))
	name := w.state.Nodes[idx].Name
	w.state.Nodes = append(w.state.Nodes[:idx], w.state.Nodes[idx+1:]...)
	w.logf("delete node %s", name)
	w.check("after-node-delete")
	if err := w.c.HandleNodeFailure(name, w.now); err != nil {
		w.logf("  HandleNodeFailure(%s) -> %v", name, err)
	}
}

// succeedPods moves one run's active pods to Succeeded, as a kubelet does. The
// next Reconcile of that run completes it.
func (w *qWorld) succeedPods() {
	keysOf := w.runKeys()
	if len(keysOf) == 0 {
		return
	}
	key := keysOf[w.rng.Intn(len(keysOf))]
	run := w.state.Runs[key]
	touched := 0
	for i := range w.state.Pods {
		pod := &w.state.Pods[i]
		if pod.Namespace == run.Namespace && pod.Labels[binder.LabelRunName] == run.Name &&
			pod.Labels[binder.LabelRunRole] != binder.RoleSpare {
			pod.Phase = binder.PodPhaseSucceeded
			touched++
		}
	}
	if touched > 0 {
		w.logf("kubelet: %d active pods of %s succeeded", touched, key)
	}
}

// tightenBudget is an admin shrinking an envelope. It is how a run that was Owned
// becomes Unfunded without anything else in the world moving — the transition the
// resolver exists to handle, and one no fixture ever builds by accident.
func (w *qWorld) tightenBudget() {
	idx := w.rng.Intn(len(w.state.Budgets))
	env := &w.state.Budgets[idx].Spec.Envelopes[0]
	if env.Concurrency <= 0 {
		return
	}
	env.Concurrency -= int32(1 + w.rng.Intn(2))
	if env.Concurrency < 0 {
		env.Concurrency = 0
	}
	w.logf("admin: budget %s envelope west concurrency -> %d", w.state.Budgets[idx].Name, env.Concurrency)
}

// --- driving the engine -----------------------------------------------------

func (w *qWorld) runKeys() []string {
	out := make([]string, 0, len(w.state.Runs))
	for key := range w.state.Runs {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (w *qWorld) reconcileOne() {
	keysOf := w.runKeys()
	if len(keysOf) == 0 {
		return
	}
	key := keysOf[w.rng.Intn(len(keysOf))]
	ns, name, _ := strings.Cut(key, "/")
	w.logf("reconcile %s", key)
	if err := w.c.Reconcile(ns, name); err != nil {
		w.logf("  -> %v", err)
	}
}

// settle drives every run, and the reservation activator, until the whole world
// stops changing. A fixpoint is where an invariant must hold unconditionally:
// there is no "mid-flight" excuse left, nothing is about to be minted, and
// anything still broken is broken forever.
func (w *qWorld) settle() {
	const maxRounds = 12
	for round := 0; round < maxRounds; round++ {
		before := outcome(w.state)
		for _, key := range w.runKeys() {
			ns, name, _ := strings.Cut(key, "/")
			_ = w.c.Reconcile(ns, name)
		}
		_ = w.c.ActivateReservations(w.now)
		w.mintPending()
		w.check("settle")
		if outcome(w.state) == before {
			return
		}
	}
	w.logf("settle: did not reach a fixpoint in %d rounds", maxRounds)
}

func (w *qWorld) fail(format string, args ...any) {
	w.t.Helper()
	w.t.Fatalf("%s\n\nop log:\n  %s\n", fmt.Sprintf(format, args...), strings.Join(w.ops, "\n  "))
}

// step applies one randomly chosen legal event and asserts the oracle after it.
func (w *qWorld) step() {
	switch w.rng.Intn(11) {
	case 0, 1:
		w.submitRun()
	case 2, 3, 4:
		w.reconcileOne()
	case 5:
		w.mintPending()
	case 6:
		w.deleteNode()
	case 7:
		w.succeedPods()
	case 8:
		w.tightenBudget()
	case 9, 10:
		d := []time.Duration{time.Minute, 5 * time.Minute, 20 * time.Minute}[w.rng.Intn(3)]
		w.tick(d)
		w.logf("clock +%s", d)
	}
	w.check("step")
}

// A generated history must never reach a state the oracle rejects — not after any
// single event, and not at the fixpoint the world settles into afterwards.
//
// This test asserts nothing about outcomes. That is deliberate and it is the whole
// value: an assertion is a statement of what its author expected, and the states
// worth finding are the ones nobody expected. The oracle is the only judge here.
func TestQuiescenceDriverReachesNoIllegalState(t *testing.T) {
	if raw := os.Getenv("JOBTREE_QUIESCENCE_SEED"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("JOBTREE_QUIESCENCE_SEED=%q: %v", raw, err)
		}
		t.Logf("replaying seed %d", n)
		runQuiescenceSeed(t, int64(n))
		return
	}
	// Seeds are enumerated, not sampled from a clock: CI must run the same 800
	// histories every time. A generator that picks a fresh seed per run is a test
	// that fails on somebody else's machine and passes on yours.
	seeds := 800
	if testing.Short() {
		seeds = 40
	}
	for seed := 0; seed < seeds; seed++ {
		runQuiescenceSeed(t, int64(seed))
	}
}

func runQuiescenceSeed(t *testing.T, seed int64) {
	t.Helper()
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes: qNodes(),
		Budgets: []v1.Budget{
			qBudget("alpha", qOwnerAlpha, 8),
			qBudget("beta", qOwnerBeta, 8),
		},
		Runs:   map[string]*v1.Run{},
		Leases: []v1.Lease{},
	}
	w := &qWorld{
		t: t, state: state, now: now,
		c:   NewRunController(state, runClock{now: now}),
		rng: rand.New(rand.NewSource(seed)),
	}

	// The oracle panics; turn that into a report naming the seed and the exact
	// history, so the failure is replayable rather than merely alarming.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("seed %d reached an illegal state\n\n%v\n\nop log:\n  %s\n"+
				"\nreplay with: JOBTREE_QUIESCENCE_SEED=%d go test ./controllers -run Quiescence -v\n",
				seed, r, strings.Join(w.ops, "\n  "), seed)
		}
	}()

	for i := 0; i < 45; i++ {
		w.step()
	}
	// Quiescence. Whatever the history did, the world must come to rest somewhere
	// legal — and the oracle is asserted inside settle(), every round.
	w.settle()
}
