package controllers

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// R7 §4: an UNBOUND or CONFLICTED namespace derives no funding principal. The
// reservation-activation path builds a cover.Request from that owner, and
// cover.Plan rejects an empty owner as FailureReasonInvalidRequest — which
// activateReservation returns as a HARD ERROR, every tick, phrased as "owner and
// flavor must be set" about a Run that has no such field.
//
// That path was unreachable before this change: the owner came from
// Run.Spec.Owner and the CRD's minLength kept it non-empty. Deriving the owner
// from the namespace makes it reachable by an ordinary admin action.
//
// The reservation must be HELD, not failed: the conflict is a misconfiguration
// somebody is about to correct, and cancelling a legitimate reservation over it
// is the reaper move.
func TestActivateReservationHoldsWhenNamespaceHasNoOwner(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	past := v1.NewTime(now.Add(-time.Hour))

	// Two Budgets, two owners, one namespace: ConflictMultipleOwners → owner "".
	state := &ClusterState{
		Nodes: []topology.SourceNode{h100Node("node-a", 8)},
		Budgets: []v1.Budget{
			h100Budget("team", "org:team", 16),
			{
				ObjectMeta: v1.ObjectMeta{Name: "rival", Namespace: "default"},
				Spec: v1.BudgetSpec{
					Owner: "org:rival",
					Envelopes: []v1.BudgetEnvelope{{
						Name: "east", Flavor: "H100-80GB", Concurrency: 16,
						Selector: map[string]string{
							topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
							topology.LabelFabricDomain: "island-a",
						},
					}},
				},
			},
		},
	}
	state.Runs = map[string]*v1.Run{"default/train": h100Run("train", "org:team", 4)}
	state.Reservations = map[string]*v1.Reservation{
		"default/res": {
			ObjectMeta: v1.ObjectMeta{Name: "res", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:        v1.RunReference{Name: "train", Namespace: "default"},
				EarliestStart: past,
			},
			Status: v1.ReservationStatus{State: "Pending"},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.ActivateReservations(now); err != nil {
		t.Fatalf("an unbound namespace is an admin error, not an activation failure; "+
			"activation must refuse quietly and hold the reservation, got: %v", err)
	}

	res := state.Reservations["default/res"]
	if res.Status.State != "Pending" {
		t.Errorf("the reservation must be HELD while the binding is broken, got state %q", res.Status.State)
	}
	if strings.Contains(res.Status.Reason, "owner and flavor must be set") {
		t.Errorf("the operator must not be told about fields the Run does not have, got reason %q", res.Status.Reason)
	}

	run := state.Runs["default/train"]
	if run.Status.Phase != RunPhasePending {
		t.Errorf("expected the run to stay Pending, got %s", run.Status.Phase)
	}
	if !strings.Contains(run.Status.Message, "no funding principal") {
		t.Errorf("the message must name the real cause (the namespace binding), got %q", run.Status.Message)
	}

	// And nothing was minted for a namespace that cannot pay.
	if len(state.Leases) != 0 {
		t.Errorf("expected no leases minted for an unbound namespace, got %d", len(state.Leases))
	}
}
