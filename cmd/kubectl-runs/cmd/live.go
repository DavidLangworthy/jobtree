package cmd

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// The functions in this file are the live-cluster counterpart to the
// StateStore-backed helpers in state.go/helpers.go. Every one does a real
// client.Client call (Get/List/Create/Update) against whatever the
// controller manager itself wrote; none of them re-run pkg/funding or
// pkg/resolver client-side — the CLI must never race the manager's own
// reconcile with a second, client-side scheduling/funding brain (see Track G
// in docs/project/make-it-real-plan.md).

// liveNotFoundf renders a "not found" error in the same shape the local
// simulator uses (helpers.go's ensureRunExists), so callers get one
// consistent message regardless of backend.
func liveNotFoundf(kind, namespace, name string) error {
	return fmt.Errorf("%s %s not found", kind, keys.NamespacedKey(namespace, name))
}

// liveGetRun fetches a Run directly from the API server.
func liveGetRun(ctx context.Context, c client.Client, namespace, name string) (*v1.Run, error) {
	run := &v1.Run{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, run); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, liveNotFoundf("run", namespace, name)
		}
		return nil, fmt.Errorf("get run: %w", err)
	}
	return run, nil
}

// liveGetReservation fetches a Reservation directly from the API server. A
// missing reservation is not an error here — plan/explain/watch treat it as
// "no pending reservation" (it may have just activated and been cleaned up
// between the Run read and this read).
func liveGetReservation(ctx context.Context, c client.Client, namespace, name string) (*v1.Reservation, error) {
	if name == "" {
		return nil, nil
	}
	res := &v1.Reservation{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, res); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get reservation: %w", err)
	}
	return res, nil
}

// reservationLookupState wraps a single already-fetched Reservation in a
// throwaway controllers.ClusterState so plan.go/explain.go's payload
// builders can be reused unmodified across both backends: those builders
// only ever index state.Reservations by key and never call the engine, so
// this is a pure display-time lookup table, not a client-side recompute.
func reservationLookupState(namespace string, res *v1.Reservation) *controllers.ClusterState {
	state := &controllers.ClusterState{Reservations: map[string]*v1.Reservation{}}
	if res != nil {
		state.Reservations[keys.NamespacedKey(namespace, res.Name)] = res
	}
	return state
}

// liveSubmitRun creates a Run on the API server and returns whatever the
// server accepted — including a possibly still-empty Status, since the
// manager reconciles asynchronously. It never fabricates a synchronous
// "bound" outcome the way the old CLI-only local reconcile did.
func liveSubmitRun(ctx context.Context, c client.Client, run *v1.Run) (*v1.Run, error) {
	if err := c.Create(ctx, run); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("run %s already exists; use `shrink`/`sponsors add` to mutate it, or delete it first", keys.NamespacedKey(run.Namespace, run.Name))
		}
		return nil, fmt.Errorf("create run: %w", err)
	}
	return run, nil
}

// liveListBudgets lists Budgets in a namespace, in name order so output is
// stable across invocations.
func liveListBudgets(ctx context.Context, c client.Client, namespace string) ([]v1.Budget, error) {
	var list v1.BudgetList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	items := append([]v1.Budget(nil), list.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// liveListLeases lists Leases in a namespace belonging to the named Run. No
// label index scopes Leases to a Run yet, so this filters client-side after
// a namespaced List — the same filter the local simulator's filterLeases
// applies to its in-memory snapshot.
func liveListLeases(ctx context.Context, c client.Client, namespace, runName string) ([]v1.GPULease, error) {
	var list v1.GPULeaseList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	leases := make([]v1.GPULease, 0, len(list.Items))
	for _, lease := range list.Items {
		if lease.Spec.RunRef.Name == runName {
			leases = append(leases, lease)
		}
	}
	return leases, nil
}

// liveMutateRun does a get-mutate-update cycle with retry-on-conflict, the
// standard client-go pattern for a CLI that must not clobber a concurrent
// write from the manager. The CLI's job ends at Update; the manager
// reconciles the new spec asynchronously — the CLI never predicts or
// fabricates the outcome.
func liveMutateRun(ctx context.Context, c client.Client, namespace, name string, mutate func(*v1.Run) error) (*v1.Run, error) {
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		run, err := liveGetRun(ctx, c, namespace, name)
		if err != nil {
			return nil, err
		}
		if err := mutate(run); err != nil {
			return nil, err
		}
		if err := c.Update(ctx, run); err != nil {
			if apierrors.IsConflict(err) {
				lastErr = err
				continue
			}
			return nil, fmt.Errorf("update run: %w", err)
		}
		return run, nil
	}
	return nil, fmt.Errorf("update run %s: too many conflicting writes: %w", keys.NamespacedKey(namespace, name), lastErr)
}
