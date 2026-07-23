package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// These tests exercise the live-cluster path (client.go/live.go) against a
// real controller-runtime fake client — no kubeconfig, no local
// cluster-state.json simulator. They assert effects via a *separate* read
// (Get after Create/Update) rather than trusting a nil error, and — for the
// retry path — via a recording interceptor that counts real Update calls.

func newTestRun(namespace, name string) *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1.RunSpec{
			Owner:     "org:team-a",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
}

func TestLiveSubmitRunCreatesOnServer(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(liveScheme).Build()
	run := newTestRun("default", "train-1")

	created, err := liveSubmitRun(context.Background(), c, run)
	if err != nil {
		t.Fatalf("liveSubmitRun: %v", err)
	}
	if created.Name != "train-1" {
		t.Fatalf("expected created run name train-1, got %q", created.Name)
	}

	// Prove the Create really happened server-side via a separate read,
	// rather than trusting the input pointer got mutated.
	fetched := &v1.Run{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "train-1"}, fetched); err != nil {
		t.Fatalf("expected run to be persisted, get failed: %v", err)
	}
	if fetched.Spec.Resources.TotalGPUs != 4 {
		t.Fatalf("expected persisted spec to round-trip, got %+v", fetched.Spec)
	}
}

func TestLiveSubmitRunAlreadyExists(t *testing.T) {
	existing := newTestRun("default", "train-1")
	c := fake.NewClientBuilder().WithScheme(liveScheme).WithObjects(existing).Build()

	_, err := liveSubmitRun(context.Background(), c, newTestRun("default", "train-1"))
	if err == nil {
		t.Fatalf("expected an error creating a Run that already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected an 'already exists' error, got %v", err)
	}
}

func TestLiveGetRunNotFound(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(liveScheme).Build()

	_, err := liveGetRun(context.Background(), c, "default", "missing")
	if err == nil {
		t.Fatalf("expected an error for a missing run")
	}
	want := "run default/missing not found"
	if err.Error() != want {
		t.Fatalf("expected error %q, got %q", want, err.Error())
	}
}

func TestLiveGetReservationMissingIsNotAnError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(liveScheme).Build()

	res, err := liveGetReservation(context.Background(), c, "default", "some-reservation")
	if err != nil {
		t.Fatalf("a missing reservation should not be an error, got %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil reservation, got %+v", res)
	}

	// An empty name (no pending reservation) must also short-circuit
	// without ever calling the API.
	res, err = liveGetReservation(context.Background(), c, "default", "")
	if err != nil || res != nil {
		t.Fatalf("expected (nil, nil) for an empty reservation name, got (%v, %v)", res, err)
	}
}

func TestLiveListBudgetsSortedByName(t *testing.T) {
	zebra := &v1.Budget{ObjectMeta: v1.ObjectMeta{Name: "zebra", Namespace: "default"}, Spec: v1.BudgetSpec{Owner: "org:z"}}
	alpha := &v1.Budget{ObjectMeta: v1.ObjectMeta{Name: "alpha", Namespace: "default"}, Spec: v1.BudgetSpec{Owner: "org:a"}}
	other := &v1.Budget{ObjectMeta: v1.ObjectMeta{Name: "other-ns", Namespace: "other"}, Spec: v1.BudgetSpec{Owner: "org:o"}}
	c := fake.NewClientBuilder().WithScheme(liveScheme).WithObjects(zebra, alpha, other).Build()

	budgets, err := liveListBudgets(context.Background(), c, "default")
	if err != nil {
		t.Fatalf("liveListBudgets: %v", err)
	}
	if len(budgets) != 2 {
		t.Fatalf("expected budgets scoped to the requested namespace only, got %d", len(budgets))
	}
	if budgets[0].Name != "alpha" || budgets[1].Name != "zebra" {
		t.Fatalf("expected budgets sorted by name, got %s, %s", budgets[0].Name, budgets[1].Name)
	}
}

func TestLiveListLeasesFiltersByRunRef(t *testing.T) {
	mine := &v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Name: "lease-mine", Namespace: "default"},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:team-a",
			RunRef:         v1.RunReference{Name: "train-1", Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-a1"}, Role: "Active"},
			PaidByEnvelope: "west-h100",
		},
	}
	other := &v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Name: "lease-other", Namespace: "default"},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:team-a",
			RunRef:         v1.RunReference{Name: "train-2", Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{"node-a2"}, Role: "Active"},
			PaidByEnvelope: "west-h100",
		},
	}
	c := fake.NewClientBuilder().WithScheme(liveScheme).WithObjects(mine, other).Build()

	leases, err := liveListLeases(context.Background(), c, "default", "train-1")
	if err != nil {
		t.Fatalf("liveListLeases: %v", err)
	}
	if len(leases) != 1 || leases[0].Name != "lease-mine" {
		t.Fatalf("expected exactly the lease for train-1, got %+v", leases)
	}
}

// TestLiveMutateRunRetriesOnConflict proves the get-mutate-update cycle
// really issues a second real Update after a conflict, using a recording
// interceptor rather than trusting the final state alone.
func TestLiveMutateRunRetriesOnConflict(t *testing.T) {
	run := newTestRun("default", "elastic")
	min, max, step, desired := int32(4), int32(8), int32(4), int32(8)
	run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: min, MaxTotalGPUs: max, StepGPUs: step, DesiredTotalGPUs: &desired}

	updateCalls := 0
	c := fake.NewClientBuilder().WithScheme(liveScheme).WithObjects(run).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, wc client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			updateCalls++
			if updateCalls == 1 {
				return apierrors.NewConflict(schema.GroupResource{Group: "rq.davidlangworthy.io", Resource: "runs"}, "elastic", fmt.Errorf("stale resourceVersion"))
			}
			return wc.Update(ctx, obj, opts...)
		},
	}).Build()

	updated, err := liveMutateRun(context.Background(), c, "default", "elastic", func(run *v1.Run) error {
		_, mutateErr := applyShrink(run, 4)
		return mutateErr
	})
	if err != nil {
		t.Fatalf("liveMutateRun: %v", err)
	}
	if updateCalls != 2 {
		t.Fatalf("expected exactly one retry (2 Update calls), got %d", updateCalls)
	}
	if updated.Spec.Malleable.DesiredTotalGPUs == nil || *updated.Spec.Malleable.DesiredTotalGPUs != 4 {
		t.Fatalf("expected desired GPUs to shrink to 4, got %+v", updated.Spec.Malleable.DesiredTotalGPUs)
	}

	fetched := &v1.Run{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "elastic"}, fetched); err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if fetched.Spec.Malleable.DesiredTotalGPUs == nil || *fetched.Spec.Malleable.DesiredTotalGPUs != 4 {
		t.Fatalf("expected the server-side object to reflect the shrink, got %+v", fetched.Spec.Malleable.DesiredTotalGPUs)
	}
}

func TestReservationLookupStateWrapsSingleReservation(t *testing.T) {
	empty := reservationLookupState("default", nil)
	if len(empty.Reservations) != 0 {
		t.Fatalf("expected no reservations for a nil input, got %d", len(empty.Reservations))
	}

	res := &v1.Reservation{ObjectMeta: v1.ObjectMeta{Name: "res-1", Namespace: "default"}}
	state := reservationLookupState("default", res)
	if len(state.Reservations) != 1 {
		t.Fatalf("expected exactly one reservation, got %d", len(state.Reservations))
	}
	if state.Reservations["default/res-1"] != res {
		t.Fatalf("expected the reservation to be keyed by namespace/name")
	}
}
