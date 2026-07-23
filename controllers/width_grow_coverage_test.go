package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/invariant"
)

// The generator-honesty lens found that mutating the oracle projection's
// RunnableGPUs from runnableGPUsForRun (TOTAL live width) to baseGangGPUsForRun
// (base gang only, grow leases excluded) was caught by NOTHING — not the generator,
// not the hand-written suite. That mutation is a reaper: an elastic run that
// assembled its declared minimum partly on grow leases would read as under-width
// and INV-WIDTH-ASSEMBLED would fire on healthy work.
//
// This closes the gap: a Running malleable run at exactly its minimum, where the
// base gang alone is below it and grow leases make up the difference. With the
// correct projection the oracle is silent; the baseGangGPUsForRun mutation makes it
// fire, so this test now fails on that mutation.
func TestWidthInvariantCountsGrowLeasesNotJustTheBaseGang(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	run := nfRun("elastic", "org:ai:team", 4, now) // Running by default
	run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 4, MaxTotalGPUs: 8, StepGPUs: 2}

	base := prodLease("elastic-base", "elastic", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now)
	grow := prodLeaseGroup("elastic-grow", "elastic", "org:ai:team", "team", "1", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now)
	grow.Spec.Reason = binder.LeaseReasonGrow // width added ON TOP of the base gang

	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/elastic": run},
		Leases:  []v1.GPULease{base, grow},
	}
	mirrorPods(state) // one pod per open lease so the run is not AwaitingMint
	c := NewRunController(state, runClock{now: now})

	// runnableGPUsForRun = 2 (base) + 2 (grow) = 4 >= min 4  -> silent (correct).
	// baseGangGPUsForRun = 2 (grow excluded)      = 2 <  4  -> would fire (mutation).
	for _, v := range invariant.CheckSteady(c.snapshotWorld()) {
		if v.ID == invariant.WidthAssembled {
			t.Fatalf("INV-WIDTH-ASSEMBLED fired on a run at full width via grow leases: "+
				"the projection is counting the base gang, not total runnable width — %s", v.Detail)
		}
	}
}
