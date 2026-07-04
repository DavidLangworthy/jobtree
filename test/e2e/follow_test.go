//go:build e2e

package e2e

import "testing"

// TestFollowChainCompletesWithRealContainers is Track C (CASCADE-1)'s
// exit-criteria e2e: a downstream Run that follows an upstream Run must
// stay Waiting until the upstream's *real* container exits 0, then admit —
// proving `follow` end to end against a real kubelet rather than
// controllers/kube/scenario_test.go's hand-driven-to-Succeeded upstream pod
// (TestFollowGatesUntilUpstreamCompletes).
//
// Blocked on the same gap as completion_test.go: without a workload field
// on RunSpec there is no way to give the upstream Run a real container to
// exit, so there is nothing this test could honestly assert beyond what
// TestRunAdmitsAndBindsOnRealCluster (smoke_test.go) already covers.
func TestFollowChainCompletesWithRealContainers(t *testing.T) {
	t.Skip("blocked on Track JOBSET — no real workload yet, so no real container exit exists for a real " +
		"follow chain to gate on. See docs/project/make-it-real-plan.md Track B (JOBSET) and Track C " +
		"(CASCADE-1, which names this exact e2e as its exit criterion).")
}
