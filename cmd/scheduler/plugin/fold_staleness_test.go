package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// R4 pt1b: the cross-gang fold must be STALENESS-ROBUST — it must reserve another
// gang's committed capacity based on whether that gang's real lease is actually in the
// SNAPSHOT it replayed, not on the in-memory minted flag. The flag flips the instant
// the lease is Created, but a read (a direct read racing the write, or an
// eventually-consistent cache) may not see that lease yet. Skipping the phantom on the
// flag while the real lease is absent from the snapshot leaves the capacity counted by
// NEITHER — and two gangs fund against the same free GPUs. This is the exact hazard
// that reverted the first cached-reader attempt.
//
// Here gang A is committed and its member is marked minted IN MEMORY, but its real
// lease is NOT in the fake client (the snapshot A's mint has not yet propagated to).
// The envelope has room for exactly one 4-GPU gang. Gang B must be UNFUNDABLE: the fold
// reserves A's capacity via A's phantom because A's real lease is not in the snapshot.
// With the old minted-flag fold, B would double-fund.
func TestFoldReservesCommittedCapacityWhenTheRealLeaseIsNotYetInTheSnapshot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// Concurrency 4 = room for exactly one 4-GPU gang. No leases seeded: A's real lease
	// is intentionally absent from the snapshot (simulating an unpropagated mint).
	m := newManager(t, trainRun(), train2Run(), teamBudget(4), gpuNode("node-a", 8))

	// Gang A: decided + fundable, its single 4-GPU member claimed and MINTED in memory,
	// but with no real lease in the client.
	segA := cover.Segment{Namespace: "default", Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "west"}
	aKey := keys.NamespacedKey("default", "train")
	m.gangs[aKey] = &gangCommit{
		decided:     true,
		fundable:    true,
		gpusPerPod:  4,
		payers:      []cover.Segment{segA},
		claimed:     1,
		assigned:    map[string]int{"train-pod-0": 0},
		minted:      []bool{true},
		pending:     pendingLeases(trainRun(), []cover.Segment{segA}, 4, now),
		lastTouched: now,
	}

	// Gang B (a different 4-GPU run) decides against the same envelope.
	podB := gangPod()
	podB.Labels[binder.LabelRunName] = "train2"
	podB.Name = "train2-pod-0"

	fundable, reason := m.decide(ctx, podB)
	if fundable {
		t.Fatalf("DOUBLE-FUND: gang B funded against capacity gang A holds. The fold skipped A's "+
			"phantom on the in-memory minted flag while A's real lease was absent from the snapshot. "+
			"reason=%q", reason)
	}
}

// The complement: once A's real lease IS in the snapshot, the fold must NOT also fold
// A's phantom (that would double-count A and wrongly refuse B). B still can't fund here
// (A's real lease consumes the envelope), but the ledger fed to B must contain A's
// lease exactly ONCE — the real one, not the real one plus the phantom.
func TestFoldSkipsThePhantomWhenTheRealLeaseIsInTheSnapshot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// A's real lease present, concurrency 8 (room for A's 4 + B's 4). If the fold
	// double-counted A (real + phantom = 8), B would see 0 and wrongly fail.
	aLease := mintedLease("train-pod-0-lease", "train-pod-0", "node-a#0")
	aLease.Spec.Slice.Nodes = []string{"node-a#0", "node-a#1", "node-a#2", "node-a#3"} // 4 GPUs
	m := newManager(t, trainRun(), train2Run(), teamBudget(8), gpuNode("node-a", 16), &aLease)

	segA := cover.Segment{Namespace: "default", Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "west"}
	aKey := keys.NamespacedKey("default", "train")
	m.gangs[aKey] = &gangCommit{
		decided: true, fundable: true, gpusPerPod: 4,
		payers: []cover.Segment{segA}, claimed: 1,
		assigned: map[string]int{"train-pod-0": 0}, minted: []bool{true},
		pending: pendingLeases(trainRun(), []cover.Segment{segA}, 4, now), lastTouched: now,
	}

	podB := gangPod()
	podB.Labels[binder.LabelRunName] = "train2"
	podB.Name = "train2-pod-0"
	if fundable, reason := m.decide(ctx, podB); !fundable {
		t.Fatalf("gang B should fund against the free 4 (A's real lease is in the snapshot and must be "+
			"counted ONCE, not real+phantom): %s", reason)
	}
}
