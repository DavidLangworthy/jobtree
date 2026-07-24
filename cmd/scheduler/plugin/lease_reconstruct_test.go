package plugin

import (
	"context"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// mintedLease builds an OPEN active lease shaped like one the plugin mints for a
// base-gang member: run + role + group labels, the durable gang identity (cohort +
// pod name), and its own funding provenance.
func mintedLease(name, podName, node string) v1.GPULease {
	l := v1.GPULease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: "default", Name: name,
			Labels: map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive, binder.LabelGroupIndex: "0"},
		},
		Spec: v1.GPULeaseSpec{
			Owner:                 "org:ai:team",
			RunRef:                v1.RunReference{Name: "train", Namespace: "default"},
			Slice:                 v1.GPULeaseSlice{Nodes: []string{node}, Role: binder.RoleActive},
			PaidByBudgetNamespace: "default",
			PaidByBudget:          "team",
			PaidByEnvelope: "west",
		},
	}
	admission.StampGangIdentity(&l, "0", podName)
	return l
}

// R2 pt3: after a scheduler restart the in-memory gang state is empty, so
// committedCount returns 0 and Permit's (waiting + committed >= expected) degrades to
// (waiting >= expected): a lone surviving member of a partially-bound gang wedges.
// Reconstruct rebuilds the committed members from the open leases so the gate holds,
// and delta-funds the un-minted survivor without double-counting the already-charged
// leases.
func TestReconstructRestoresCommittedGangAfterRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// A 4-pod gang (trainRun is 4 GPUs, 1 GPU/pod) with 3 members already minted; the
	// 4th was still unbound when the scheduler restarted.
	l0 := mintedLease("train-pod-0-lease", "train-pod-0", "node-a#0")
	l1 := mintedLease("train-pod-1-lease", "train-pod-1", "node-a#1")
	l2 := mintedLease("train-pod-2-lease", "train-pod-2", "node-a#2")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(trainRun(), teamBudget(8), gpuNode("node-a", 8), &l0, &l1, &l2).Build()
	m := newGangManager(c, func() time.Time { return now })

	if err := m.Reconstruct(ctx); err != nil {
		t.Fatalf("reconstruct: %v", err)
	}

	key := keys.NamespacedKey("default", "train")
	if got := m.committedCount(key); got != 3 {
		t.Fatalf("committedCount after restart = %d, want 3 (the minted members); a lone survivor would otherwise wedge at permitTimeout", got)
	}

	// The lone survivor admits: Permit's waiting(1) + committed(3) >= expected(4), and
	// it claims its delta payer (funded against the live ledger, not the full width).
	survivor := gangPod()
	survivor.Name = "train-pod-3"
	seg, gpp, ok := m.claimPayer(survivor)
	if !ok {
		t.Fatalf("the un-minted survivor must claim its delta payer after reconstruction")
	}
	if gpp != 1 {
		t.Errorf("gpusPerPod = %d, want 1", gpp)
	}
	if seg.EnvelopeName != "west" {
		t.Errorf("survivor payer envelope = %q, want west", seg.EnvelopeName)
	}

	// No double-count: a PreBind retry of an already-minted pod returns its ORIGINAL
	// payer (from its lease) without consuming a delta payer...
	retry := gangPod()
	retry.Name = "train-pod-0"
	if _, _, ok := m.claimPayer(retry); !ok {
		t.Errorf("a PreBind retry of an already-minted pod must return its recovered payer")
	}
	// ...and a fifth pod overflows the 4-wide gang (the width is not re-funded).
	fifth := gangPod()
	fifth.Name = "train-pod-4"
	if _, _, ok := m.claimPayer(fifth); ok {
		t.Errorf("a 5th pod must overflow the 4-wide reconstructed gang (no double-mint)")
	}
}

// ABA: a prior incarnation's CLOSED leases must not be reconstructed into a live
// gang — a delete+resubmit of a same-named run starts a fresh gang.
func TestReconstructIgnoresClosedLeases(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	closed := mintedLease("train-pod-0-lease", "train-pod-0", "node-a#0")
	closed.Status.Closed = true
	closed.Status.ClosureReason = "Completed"
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(trainRun(), teamBudget(8), gpuNode("node-a", 8), &closed).Build()
	m := newGangManager(c, func() time.Time { return now })

	if err := m.Reconstruct(ctx); err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if got := m.committedCount(keys.NamespacedKey("default", "train")); got != 0 {
		t.Fatalf("a closed prior-incarnation lease must not reconstruct a committed gang, committedCount = %d", got)
	}
	// A fresh gang decides normally against the (empty-of-open-leases) world.
	if fundable, reason := m.decide(ctx, gangPod()); !fundable {
		t.Fatalf("a resubmitted run should fund fresh after its prior leases closed: %s", reason)
	}
}
