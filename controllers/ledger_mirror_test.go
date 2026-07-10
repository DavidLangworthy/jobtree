package controllers

import (
	"strings"

	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// mirrorPods gives a lease-only fixture the workload plane it would really have.
//
// A Lease exists because a pod existed first: the controller emits an intent pod,
// and the scheduler plugin — the sole committer — mints the lease at PreBind. So a
// fixture holding an open lease and no pods is not a simplification of production,
// it is a state production cannot reach. INV-LEASE-HAS-POD says so, and it named
// twelve such fixtures the moment it was installed.
//
// That matters beyond tidiness. Every one of those tests was written to pin the
// behaviour of a path that DELETES PODS — the swap, the reclaim, the terminal
// release. Against a fixture with no pods, the pod-deleting half of the code under
// test ran against an empty slice and proved nothing. This is playbook class 8 in
// its quietest form: not a test asserting the bug, but a test standing next to it.
//
// One pod per OPEN lease, on the lease's own node, carrying the lease's role and
// placement group. Exactly one, because activePods > openActiveLeases is what the
// oracle reads as "a mint is still in flight" — and a fixture that trips that gate
// silently disables the width invariant it was meant to be checked by.
func mirrorPods(state *ClusterState) {
	have := map[string]bool{}
	for _, pod := range state.Pods {
		have[pod.Namespace+"/"+pod.Name] = true
	}
	for i := range state.Leases {
		lease := &state.Leases[i]
		if lease.Status.Closed || len(lease.Spec.Slice.Nodes) == 0 {
			continue
		}
		ns := lease.Spec.RunRef.Namespace
		name := lease.Name + "-pod"
		if have[ns+"/"+name] {
			continue
		}
		role := lease.Spec.Slice.Role
		if role == "" {
			role = binder.RoleActive
		}
		node, _, _ := strings.Cut(lease.Spec.Slice.Nodes[0], "#")
		state.Pods = append(state.Pods, binder.PodManifest{
			Namespace: ns,
			Name:      name,
			NodeName:  node,
			GPUs:      len(lease.Spec.Slice.Nodes),
			Labels: map[string]string{
				binder.LabelRunName:    lease.Spec.RunRef.Name,
				binder.LabelRunRole:    role,
				binder.LabelGroupIndex: leaseGroupIndex(lease),
			},
		})
	}
}
