package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/resolver"
)

// R6: resolver actions are counted when applied, once per lease actually
// closed — actions targeting already-closed leases do not count.
func TestApplyResolutionCountsOnlyAppliedActions(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	open := v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "lease-open"},
		Spec: v1.GPULeaseSpec{
			RunRef: v1.RunReference{Namespace: "default", Name: "run-a"},
			Slice:  v1.GPULeaseSlice{Nodes: []string{"node-a#0"}, Role: "Active"},
		},
	}
	closed := v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "lease-closed"},
		Spec: v1.GPULeaseSpec{
			RunRef: v1.RunReference{Namespace: "default", Name: "run-a"},
			Slice:  v1.GPULeaseSlice{Nodes: []string{"node-a#1"}, Role: "Active"},
		},
		Status: v1.GPULeaseStatus{Closed: true},
	}
	state := &ClusterState{
		Runs:   map[string]*v1.Run{},
		Leases: []v1.GPULease{open, closed},
	}
	controller := &RunController{State: state}

	result := resolver.Result{Actions: []resolver.Action{
		{Kind: resolver.ActionLottery, Lease: &state.Leases[0], GroupIndex: "0", GPUs: 1, Reason: "RandomPreempt(seed)"},
		{Kind: resolver.ActionShrink, Lease: &state.Leases[1], GroupIndex: "0", GPUs: 1, Reason: "Shrink"},
	}}
	controller.applyResolution(result, now)

	actions := metrics.Snapshot().ResolverActions
	if actions[string(resolver.ActionLottery)] != 1 {
		t.Errorf("expected 1 lottery action counted, got %v", actions[string(resolver.ActionLottery)])
	}
	if actions[string(resolver.ActionShrink)] != 0 {
		t.Errorf("expected no shrink actions counted for an already-closed lease, got %v", actions[string(resolver.ActionShrink)])
	}
}
