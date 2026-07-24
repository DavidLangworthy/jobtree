package resolver

import (
	"fmt"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// R7 §4: an unbound or conflicted namespace derives the EMPTY owner. Every such
// namespace derives the same empty string, so keying the reclaim/lottery buckets
// on it directly would pool unrelated tenants into one bucket — identity
// coarsening (playbook class 5). The lottery draws a victim OWNER first and a
// token second, so a shared bucket is a shared ticket.

func conflictedNamespaceEvaluation(t *testing.T, namespaces ...string) *funding.Evaluation {
	t.Helper()
	var budgets []v1.Budget
	for _, ns := range namespaces {
		// Two owners in one namespace: ConflictMultipleOwners, fail-safe to "".
		budgets = append(budgets,
			v1.Budget{
				ObjectMeta: v1.ObjectMeta{Name: "one", Namespace: ns},
				Spec:       v1.BudgetSpec{Owner: "org:" + ns + ":one"},
			},
			v1.Budget{
				ObjectMeta: v1.ObjectMeta{Name: "two", Namespace: ns},
				Spec:       v1.BudgetSpec{Owner: "org:" + ns + ":two"},
			})
	}
	ev := funding.Evaluate(funding.Input{Budgets: budgets, Now: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)})
	for _, ns := range namespaces {
		if got := ev.OwnerOf(ns); got != "" {
			t.Fatalf("fixture is wrong: OwnerOf(%s) = %q, want the empty fail-safe owner", ns, got)
		}
	}
	return ev
}

// The load-bearing line, tested directly: two DIFFERENT conflicted namespaces
// must not produce the same bucket key.
func TestUnboundNamespacesGetDistinctBucketKeys(t *testing.T) {
	ev := conflictedNamespaceEvaluation(t, "tenant-a", "tenant-b")
	runA := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "a", Namespace: "tenant-a"}}
	runB := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "b", Namespace: "tenant-b"}}
	in := Input{Evaluation: ev}

	keyA, keyB := ownerOf(in, runA), ownerOf(in, runB)
	if keyA == keyB {
		t.Fatalf("two conflicted namespaces share the bucket key %q; they are separate tenants", keyA)
	}
	if keyA == "" || keyB == "" {
		t.Fatalf("the empty owner is a sentinel, not a bucket key: got %q and %q", keyA, keyB)
	}
	// And the fallback may never collide with a real owner string, which is
	// free-form (Budget.Spec.Owner is checked only for non-emptiness).
	if got := ownerOf(Input{Evaluation: ev}, &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "x", Namespace: "org:tenant-a:one"}}); got == "org:tenant-a:one" {
		t.Fatalf("bucket key %q is indistinguishable from a declarable owner string", got)
	}
}

// And the consequence: the eviction draw must treat two conflicted namespaces as
// two tenants. tenant-a holds ONE unfunded group, tenant-b holds NINE. Pooled
// into one bucket, tenant-a is drawn ~1 time in 10; bucketed per tenant it is
// drawn ~1 in 2. Seeds are enumerated, not sampled, so this is deterministic.
func TestUnboundNamespacesDoNotShareOneLotteryBucket(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := conflictedNamespaceEvaluation(t, "tenant-a", "tenant-b")

	runs := map[string]*v1.Run{}
	var leases []*v1.GPULease
	var nodes []topology.SourceNode
	mk := func(ns, name, node string) {
		run := &v1.Run{
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       v1.RunSpec{Resources: v1.RunResources{GPUType: "H100", TotalGPUs: 4}},
		}
		runs[keys.NamespacedKey(ns, name)] = run
		leases = append(leases, buildLease(run, "0", "Active",
			[]string{node + "#0", node + "#1", node + "#2", node + "#3"}, now))
		nodes = append(nodes, sourceNode(node, "us-west", "cluster-a", "island-a", "H100", 4))
	}
	mk("tenant-a", "solo", "node-solo")
	for i := 0; i < 9; i++ {
		mk("tenant-b", fmt.Sprintf("crowd-%d", i), fmt.Sprintf("node-crowd-%d", i))
	}

	const rounds = 60
	hitsA := 0
	for seed := 0; seed < rounds; seed++ {
		in := Input{
			Deficit:    4,
			Flavor:     "H100",
			Scope:      map[string]string{},
			SeedSource: fmt.Sprintf("reservation-%d", seed),
			Now:        now,
			Nodes:      nodes,
			Leases:     leases,
			Runs:       runs,
			Evaluation: ev,
		}
		res, err := Resolve(in)
		if err != nil {
			t.Fatalf("seed %d: resolve failed: %v", seed, err)
		}
		if len(res.Actions) == 0 {
			t.Fatalf("seed %d: expected a victim", seed)
		}
		if res.Actions[0].Lease.Spec.RunRef.Namespace == "tenant-a" {
			hitsA++
		}
	}

	// Per-tenant buckets ⇒ ~30/60. One pooled bucket ⇒ ~6/60. The threshold sits
	// far from both so it is a statement about the bucketing, not about the RNG.
	if hitsA < rounds/4 {
		t.Fatalf("tenant-a (1 group) was drawn %d/%d times against tenant-b (9 groups); "+
			"that is the one-group-one-ticket rate of a SHARED bucket, not the per-tenant rate", hitsA, rounds)
	}
}
