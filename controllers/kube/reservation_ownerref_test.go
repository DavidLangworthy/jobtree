package kube

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// R12 step 2b. A Reservation is a planning artifact with no funding to audit, so it
// is OWNED by its Run and cascade-GC'd — unlike a Lease, which is finalizer-closed.
// This pins the owner reference apply stamps onto a newly created Reservation.
func TestApplyStampsTheRunOwnerReferenceOntoANewReservation(t *testing.T) {
	_ = captureReport(t)

	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default", UID: "run-uid-123"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
		Status:     v1.RunStatus{Phase: controllers.RunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(healthyNode("node-a", 4), run).
		WithStatusSubresource(&v1.Run{}, &v1.Lease{}, &v1.Reservation{}).
		Build()
	bridge := &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}

	// Inject a reservation for run "r" during the pass; apply creates what is in
	// state.Reservations but absent from the API.
	err := bridge.WithWorld(context.Background(), func(state *controllers.ClusterState, now time.Time) error {
		state.Reservations[keys.NamespacedKey("default", "res")] = &v1.Reservation{
			ObjectMeta: metav1.ObjectMeta{Name: "res", Namespace: "default"},
			Spec:       v1.ReservationSpec{RunRef: v1.RunReference{Name: "r", Namespace: "default"}},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithWorld: %v", err)
	}

	var got v1.Reservation
	if err := c.Get(context.Background(), types.NamespacedName{Name: "res", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get reservation: %v", err)
	}
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("a reservation must be owned by its Run for cascade GC, got refs %v", got.OwnerReferences)
	}
	ref := got.OwnerReferences[0]
	if ref.Kind != "Run" || ref.Name != "r" || ref.UID != "run-uid-123" {
		t.Fatalf("owner reference does not name the Run: %+v", ref)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Errorf("the Run must be the controlling owner so kube GC deletes the reservation with it")
	}
}
