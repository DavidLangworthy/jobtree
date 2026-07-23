package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// R9 9A-1: when a node fails and a spare takes over, the replacement must inherit
// the dead member's rendezvous identity (its hostname), and the dead pod must be
// removed so its stale DNS record does not shadow the replacement. Otherwise a
// distributed-training job cannot re-rendezvous the swapped rank at its old address.
func TestSwapInheritsFailedMembersRendezvousHostname(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := nfRun("run", "org:ai:team", 2, now)

	active := prodLease("run-active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now)
	spare := prodLease("run-spare", "run", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now)

	activePod := tpPod("run-active-0", "run", "node-a") // member's name is its hostname
	sparePod := binder.PodManifest{
		Namespace: "default", Name: "run-spare-0", NodeName: "node-b", GPUs: 2,
		Labels: map[string]string{
			binder.LabelRunName: "run", binder.LabelRunRole: binder.RoleSpare, binder.LabelGroupIndex: "0",
		},
	}

	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/run": run},
		Leases:  []v1.GPULease{active, spare},
		Pods:    []binder.PodManifest{activePod, sparePod},
	}
	c := NewRunController(state, runClock{now: now})
	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	// The dead member's pod is gone (its DNS record must not shadow the replacement).
	for _, p := range state.Pods {
		if p.Name == "run-active-0" {
			t.Fatalf("the failed member's pod must be removed on swap, but it is still present")
		}
	}
	// The swap pod took over on the spare's node, carrying the dead member's hostname.
	var swap *binder.PodManifest
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Labels[binder.LabelRunRole] == binder.RoleActive && p.Annotations[binder.AnnotationLeaseReason] == "Swap" {
			swap = p
		}
	}
	if swap == nil {
		t.Fatalf("expected a swap pod after the node failure, pods=%+v", state.Pods)
	}
	if swap.Hostname != "run-active-0" {
		t.Errorf("swap pod must inherit the dead member's rendezvous hostname %q, got %q", "run-active-0", swap.Hostname)
	}
	if swap.NodeName != "node-b" {
		t.Errorf("swap pod must land on the reclaimed spare node node-b, got %q", swap.NodeName)
	}
	if swap.Name == "run-active-0" {
		t.Errorf("swap pod must keep a UNIQUE object name (apply diffs by name); reusing the dead name no-ops the swap")
	}
}
