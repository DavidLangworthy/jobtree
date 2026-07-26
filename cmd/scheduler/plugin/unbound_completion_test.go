package plugin

import (
	"context"
	"testing"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// rivalBudget is the second owner that makes namespace "default" CONFLICTED, so
// funding.OwnerOfNamespace derives "" — the same stimulus as
// TestPromiseProvenanceRefusedWhenNamespaceConflicted.
//
// It has to be a conflict rather than an empty namespace: with no Budgets at all
// the envelope walk at the end of promiseProvenanceValid refuses anyway, so a
// permit could never be observed and every assertion below would be vacuous for
// the wrong reason. Here the named budget/envelope really does exist; the ONLY
// thing standing between the promise and a mint is the derived-owner gate.
func rivalBudget() *v1.Budget {
	return &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "other", Namespace: "default"},
		Spec: v1.BudgetSpec{Owner: "org:ai:other", Envelopes: []v1.BudgetEnvelope{{
			Name: "east", Flavor: "H100-80GB", Concurrency: 8,
		}}},
	}
}

// heldLease mints an OPEN lease for default/train holding one GPU slot per entry
// in nodes, in the given role.
func heldLease(name, role string, nodes ...string) *v1.GPULease {
	return &v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.GPULeaseSpec{
			Owner:                 "org:ai:team",
			RunRef:                v1.RunReference{Name: "train", Namespace: "default"},
			Slice:                 v1.GPULeaseSlice{Nodes: nodes, Role: role},
			PaidByBudgetNamespace: "default",
			PaidByBudget:          "team",
			PaidByEnvelope:        "west",
			Reason:                "Promise",
		},
	}
}

func closedLease(name string, nodes ...string) *v1.GPULease {
	l := heldLease(name, binder.RoleActive, nodes...)
	l.Status.Closed = true
	return l
}

// MERGE-127.md pre-merge item 2, and the CRITICAL finding it answers.
//
// The blanket `derived == "" -> refuse` in promiseProvenanceValid stranded a
// PARTIALLY minted gang: 2 of 4 ranks committed, their leases open and holding
// real GPUs, Unfunded hours climbing, pods re-emitted forever, and a rank lost to
// node failure never replaceable — because the very refusal that was supposed to
// stop acquisition also stopped COMPLETION of a commitment the cluster had
// already authorized.
//
// The substitution's whole safety argument is the width bound, so that is what
// this test pins, from both sides:
//
//   - below the run's RECORDED width -> permitted (finishing);
//   - at or above it -> refused (growth), because one open lease must never
//     license unlimited minting in a namespace with no payer.
//
// Both directions matter. A test that only checked the permit would stay green
// against a fix that deleted the bound entirely, which is precisely the hole the
// bound exists to close.
func TestPromiseCompletionPermittedBelowRecordedWidthAndRefusedAbove(t *testing.T) {
	ctx := context.Background()
	seg := cover.Segment{Namespace: "default", Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "west"}

	// Sanity: with the namespace BOUND this provenance is accepted, so every
	// refusal below is attributable to the derived-owner gate and not to a
	// malformed fixture.
	if !newManager(t, trainRun(), teamBudget(8)).promiseProvenanceValid(ctx, "default", "train", seg) {
		t.Fatalf("fixture is wrong: a bound namespace must accept its own envelope's provenance")
	}

	cases := []struct {
		name   string
		leases []*v1.GPULease
		want   bool
		why    string
	}{{
		// The case the task calls out by name. Nothing was ever authorized here,
		// so there is nothing to complete and the R7 §4 fail-safe stands
		// unchanged. This is the difference between "finish what you started"
		// and "start something in a namespace with no payer".
		name:   "zero open leases refuses: nothing was authorized",
		leases: nil,
		want:   false,
		why:    "an unbound namespace with no open lease has authorized nothing; the fail-safe must still refuse",
	}, {
		name:   "one rank of four permits completion",
		leases: []*v1.GPULease{heldLease("l0", binder.RoleActive, "node-a#0")},
		want:   true,
		why:    "held 1 < recorded 4: this is completion of an already-authorized gang, not acquisition",
	}, {
		name: "two ranks of four permits completion",
		leases: []*v1.GPULease{
			heldLease("l0", binder.RoleActive, "node-a#0"),
			heldLease("l1", binder.RoleActive, "node-a#1"),
		},
		want: true,
		why:  "held 2 < recorded 4: the stranded-partial-gang case the panel reproduced",
	}, {
		name: "at the recorded width refuses growth",
		leases: []*v1.GPULease{
			heldLease("l0", binder.RoleActive, "node-a#0", "node-a#1"),
			heldLease("l1", binder.RoleActive, "node-a#2", "node-a#3"),
		},
		want: false,
		why:  "held 4 == recorded 4: the gang is whole, so any further mint is GROWTH in a namespace with no payer",
	}, {
		name: "beyond the recorded width refuses",
		leases: []*v1.GPULease{
			heldLease("l0", binder.RoleActive, "node-a#0", "node-a#1", "node-a#2"),
			heldLease("l1", binder.RoleActive, "node-a#3", "node-a#4", "node-a#5"),
		},
		want: false,
		why:  "held 6 > recorded 4: already over width; minting more cannot be completion of anything",
	}, {
		// Spares are excluded for the same reason Permit gates on ACTIVE width: a
		// spare is not gang-active membership. Counting them would let a run
		// holding 4 spares and no active rank present itself as a partial gang.
		name:   "a spare alone does not authorize completion",
		leases: []*v1.GPULease{heldLease("s0", binder.RoleSpare, "node-a#0")},
		want:   false,
		why:    "a spare is not gang-active membership, so it cannot stand in for an authorized commitment",
	}, {
		// A closed lease is a settled interval: it holds no GPUs and charges
		// nobody. It is evidence of a PAST authorization, not a live one.
		name:   "a closed lease does not authorize completion",
		leases: []*v1.GPULease{closedLease("l0", "node-a#0")},
		want:   false,
		why:    "a closed lease holds nothing; the run is not mid-gang and has nothing to finish",
	}, {
		// The width count is per-run. Another run's open lease says nothing about
		// what THIS run was authorized to hold.
		name: "another run's lease does not authorize completion",
		leases: []*v1.GPULease{func() *v1.GPULease {
			l := heldLease("other", binder.RoleActive, "node-a#0")
			l.Spec.RunRef.Name = "sibling"
			return l
		}()},
		want: false,
		why:  "leases are counted per-run; a sibling's commitment cannot authorize this run's mint",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{trainRun(), teamBudget(8), rivalBudget()}
			for _, l := range tc.leases {
				objs = append(objs, l)
			}
			m := newManager(t, objs...)

			// Sanity per case: the namespace really is conflicted, so this
			// exercises the completion exception and not some other refusal.
			if got := m.promiseProvenanceValid(ctx, "default", "train", cover.Segment{
				Namespace: "default", Owner: "org:ai:team", BudgetName: "nosuch", EnvelopeName: "west",
			}); got {
				t.Fatalf("fixture is wrong: provenance naming a nonexistent budget must never validate")
			}

			if got := m.promiseProvenanceValid(ctx, "default", "train", seg); got != tc.want {
				t.Errorf("promiseProvenanceValid = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}
