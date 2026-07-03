package kube

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// serialWorker pins every engine-driving controller to one worker: the
// admission decision must be serialized (specs/BudgetConservation.tla) and
// the Bridge mutex makes cross-controller access safe; a single worker per
// controller avoids queue-side churn on top of it.
var serialWorker = controller.Options{MaxConcurrentReconciles: 1}

// RunReconciler drives admission, elasticity, and completion for Runs.
type RunReconciler struct {
	Bridge *Bridge
}

// pendingRunResync re-drives runs parked as Pending without a reservation
// (invalid request, no matching domain, planning failure): no watch event
// announces that their blocker went away, so they poll.
const pendingRunResync = time.Minute

// runningRunResync re-derives funding for Running runs: classification is a
// function of the clock (integrals accrue and exhaust, windows open and
// close), so status drifts stale with no watch event to announce it.
const runningRunResync = 5 * time.Minute

// waitingRunResync re-drives runs blocked on follow dependencies: the Run→Run
// watch handles upstream transitions, but this backstops missed events and
// fires the "wait" grace deadline on a failed upstream.
const waitingRunResync = 30 * time.Second

func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var parked, running, waiting bool
	err := r.Bridge.WithWorld(ctx, func(state *controllers.ClusterState, now time.Time) error {
		key := keys.NamespacedKey(req.Namespace, req.Name)
		run, ok := state.Runs[key]
		if !ok {
			// The run is gone: release what it left behind. Both the
			// deletion event itself and lease events mapped to the
			// missing run (including the watch replay after a manager
			// restart) land here, so orphans converge to cleaned up.
			cleanupDeletedRun(state, key, req.Namespace, req.Name, now)
			return nil
		}
		rc := controllers.NewRunController(state, staticClock{now})
		rc.Period = r.Bridge.Period
		err := rc.Reconcile(req.Namespace, req.Name)
		parked = run.Status.Phase == controllers.RunPhasePending && run.Status.PendingReservation == nil
		running = run.Status.Phase == controllers.RunPhaseRunning
		waiting = run.Status.Phase == controllers.RunPhaseWaiting
		return err
	})
	switch {
	case err != nil:
		return ctrl.Result{}, err
	case waiting:
		return ctrl.Result{RequeueAfter: waitingRunResync}, nil
	case parked:
		return ctrl.Result{RequeueAfter: pendingRunResync}, nil
	case running:
		return ctrl.Result{RequeueAfter: runningRunResync}, nil
	}
	return ctrl.Result{}, nil
}

// cleanupDeletedRun closes the open leases, drops the pods, and removes the
// reservations that belonged to a Run that no longer exists; otherwise the
// leases keep charging the budget and occupying nodes forever.
func cleanupDeletedRun(state *controllers.ClusterState, runKey, namespace, name string, now time.Time) {
	ended := v1.NewTime(now)
	for i := range state.Leases {
		lease := &state.Leases[i]
		if lease.Status.Closed {
			continue
		}
		leaseRun := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		if leaseRun != runKey {
			continue
		}
		lease.Status.Closed = true
		lease.Status.Ended = &ended
		lease.Status.ClosureReason = "RunDeleted"
	}
	kept := state.Pods[:0]
	for _, pod := range state.Pods {
		if pod.Namespace == namespace && pod.Labels[binder.LabelRunName] == name {
			continue
		}
		kept = append(kept, pod)
	}
	state.Pods = kept
	for key, res := range state.Reservations {
		refNamespace := res.Spec.RunRef.Namespace
		if keys.NamespacedKey(refNamespace, res.Spec.RunRef.Name) == runKey {
			delete(state.Reservations, key)
		}
	}
}

// SetupWithManager registers the reconciler. Lease events re-trigger their
// owning run so closures (shrink, preemption, completion) refresh status.
// Budget spec changes re-trigger every run: quota is the input to the
// funding derivation, so any envelope edit can reclassify any run. The
// generation gates matter with a live clock: reconciles rewrite the float
// GPU-hour status fields (runs) and updatedAt (budgets), and without them
// each status write would re-trigger the reconciler in a self-sustaining
// loop.
func (r *RunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// A workload pod reaching Succeeded re-triggers its run so the gang can
	// finalize. The predicate is Succeeded-only: pod creates (Pending) and the
	// controller's own deletes on completion do not match, so this adds no
	// reconcile churn under the single serial worker.
	podSucceeded := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		pod, ok := obj.(*corev1.Pod)
		return ok && pod.Status.Phase == corev1.PodSucceeded
	})
	// A run reaching a terminal phase (or being deleted) re-triggers its
	// followers so a blocked chain advances. This must NOT reuse the primary
	// For() generation gate: phase lives in status, so a completion bumps no
	// generation — a naïve "any Run update" watch would fire on every status
	// write and churn under the single serial worker.
	runTerminalOrDelete := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			run, ok := e.Object.(*v1.Run)
			return ok && isTerminalPhase(run.Status.Phase)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRun, ok1 := e.ObjectOld.(*v1.Run)
			newRun, ok2 := e.ObjectNew.(*v1.Run)
			return ok1 && ok2 && !isTerminalPhase(oldRun.Status.Phase) && isTerminalPhase(newRun.Status.Phase)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("run").
		For(&v1.Run{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1.Lease{}, handler.EnqueueRequestsFromMapFunc(leaseToRun)).
		Watches(&v1.Budget{}, handler.EnqueueRequestsFromMapFunc(r.budgetToRuns),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(podToRun),
			builder.WithPredicates(podSucceeded)).
		Watches(&v1.Run{}, handler.EnqueueRequestsFromMapFunc(r.runToFollowers),
			builder.WithPredicates(runTerminalOrDelete)).
		WithOptions(serialWorker).
		Complete(r)
}

// isTerminalPhase reports whether a run has reached a state that unblocks or
// permanently blocks its followers.
func isTerminalPhase(phase string) bool {
	return phase == controllers.RunPhaseComplete || phase == controllers.RunPhaseFailed
}

// runToFollowers maps a finished (or deleted) upstream run to every run in the
// same namespace that follows it, so blocked followers re-evaluate.
func (r *RunReconciler) runToFollowers(ctx context.Context, obj client.Object) []reconcile.Request {
	upstream, ok := obj.(*v1.Run)
	if !ok {
		return nil
	}
	var runList v1.RunList
	if err := r.Bridge.APIReader.List(ctx, &runList); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range runList.Items {
		run := &runList.Items[i]
		if run.Namespace != upstream.Namespace || run.Spec.Follow == nil {
			continue
		}
		for _, name := range run.Spec.Follow.After {
			if name == upstream.Name {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name},
				})
				break
			}
		}
	}
	return requests
}

// podToRun maps a workload pod event to its owning run via the run-name label.
func podToRun(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	runName := pod.Labels[binder.LabelRunName]
	if runName == "" {
		return nil
	}
	namespace := pod.Namespace
	if namespace == "" {
		namespace = keys.DefaultNamespace
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: runName},
	}}
}

// budgetToRuns fans a budget change out to every run: family sharing and
// lending mean there is no per-run scoping of a quota edit.
func (r *RunReconciler) budgetToRuns(ctx context.Context, _ client.Object) []reconcile.Request {
	var runList v1.RunList
	if err := r.Bridge.APIReader.List(ctx, &runList); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(runList.Items))
	for i := range runList.Items {
		run := &runList.Items[i]
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: run.Namespace, Name: run.Name},
		})
	}
	return requests
}

func leaseToRun(ctx context.Context, obj client.Object) []reconcile.Request {
	lease, ok := obj.(*v1.Lease)
	if !ok || lease.Spec.RunRef.Name == "" {
		return nil
	}
	namespace := lease.Spec.RunRef.Namespace
	if namespace == "" {
		namespace = keys.DefaultNamespace
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: lease.Spec.RunRef.Name},
	}}
}

// ReservationReconciler activates due reservations and requeues future ones
// at their EarliestStart.
type ReservationReconciler struct {
	Bridge *Bridge
}

func (r *ReservationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var requeueAfter time.Duration
	err := r.Bridge.WithWorld(ctx, func(state *controllers.ClusterState, now time.Time) error {
		res, ok := state.Reservations[keys.NamespacedKey(req.Namespace, req.Name)]
		if !ok {
			return nil
		}
		if res.Status.State != "Pending" && res.Status.State != "" {
			return nil
		}
		if res.Spec.EarliestStart.Time.After(now) {
			requeueAfter = res.Spec.EarliestStart.Time.Sub(now)
			return nil
		}
		rc := controllers.NewRunController(state, staticClock{now})
		rc.Period = r.Bridge.Period
		err := rc.ActivateReservations(now)
		if res.Status.State == "Pending" || res.Status.State == "" {
			// A due reservation can park at activation (an unfunded promise
			// waiting for physical capacity reclaims nothing — R7). No watch
			// event announces freed GPUs, so it polls.
			requeueAfter = pendingRunResync
		}
		return err
	})
	return ctrl.Result{RequeueAfter: requeueAfter}, err
}

func (r *ReservationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("reservation").
		For(&v1.Reservation{}).
		WithOptions(serialWorker).
		Complete(r)
}

// NodeReconciler performs spare swaps when a node stops being usable.
type NodeReconciler struct {
	Bridge *Bridge
}

func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var node corev1.Node
	if err := r.Bridge.APIReader.Get(ctx, req.NamespacedName, &node); err != nil {
		if apierrors.IsNotFound(err) {
			node.Name = req.Name // a deleted node is a failed node
		} else {
			return ctrl.Result{}, err
		}
	} else if nodeUsable(&node) {
		return ctrl.Result{}, nil
	}

	err := r.Bridge.WithWorld(ctx, func(state *controllers.ClusterState, now time.Time) error {
		rc := controllers.NewRunController(state, staticClock{now})
		rc.Period = r.Bridge.Period
		if err := rc.HandleNodeFailure(req.Name, now); err != nil {
			// Nothing was running there: the failure needs no response.
			if strings.Contains(err.Error(), "no active lease found") {
				return nil
			}
			return err
		}
		return nil
	})
	return ctrl.Result{}, err
}

func nodeUsable(node *corev1.Node) bool {
	if node.Spec.Unschedulable {
		return false
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SetupWithManager watches for nodes that need failure handling: any event
// where the node is unusable — including create events, which is how the
// watch replay after a manager restart reports failures that happened while
// the manager was down, and how a node born broken surfaces — plus every
// delete event, since deleting a healthy node IS its failure (filtering
// deletes through the last-known state would drop exactly those).
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	unusable := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		node, ok := obj.(*corev1.Node)
		return ok && !nodeUsable(node)
	})
	anyDelete := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("node").
		For(&corev1.Node{}, builder.WithPredicates(predicate.Or(unusable, anyDelete))).
		WithOptions(serialWorker).
		Complete(r)
}

// BudgetReconciler refreshes budget status from the funding derivation.
type BudgetReconciler struct {
	Client    client.Client
	APIReader client.Reader
	Clock     controllers.Clock
	// Period is the accounting horizon for the funding derivation.
	Period time.Duration
	// ResyncPeriod re-reconciles budgets so GPU-hour accrual stays fresh.
	ResyncPeriod time.Duration
}

func (r *BudgetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var budget v1.Budget
	if err := r.APIReader.Get(ctx, req.NamespacedName, &budget); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// The evaluation is global: family sharing and lending mean other
	// budgets' leases and runs decide what this budget's envelopes fund.
	var budgetList v1.BudgetList
	if err := r.APIReader.List(ctx, &budgetList); err != nil {
		return ctrl.Result{}, err
	}
	var leaseList v1.LeaseList
	if err := r.APIReader.List(ctx, &leaseList); err != nil {
		return ctrl.Result{}, err
	}
	var runList v1.RunList
	if err := r.APIReader.List(ctx, &runList); err != nil {
		return ctrl.Result{}, err
	}
	runs := make(map[string]*v1.Run, len(runList.Items))
	for i := range runList.Items {
		run := &runList.Items[i]
		runs[keys.NamespacedKey(run.Namespace, run.Name)] = run
	}
	ev := funding.Evaluate(funding.Input{
		Budgets: budgetList.Items,
		Leases:  leaseList.Items,
		Runs:    runs,
		Now:     r.Clock.Now(),
		Period:  r.Period,
	})
	bc := controllers.NewBudgetController(r.Clock, controllers.NewBudgetMetrics())
	status := bc.ReconcileBudget(&budget, ev)
	budget.Status = status
	if err := r.Client.Status().Update(ctx, &budget); err != nil {
		return ctrl.Result{}, err
	}
	resync := r.ResyncPeriod
	if resync <= 0 {
		resync = 5 * time.Minute
	}
	return ctrl.Result{RequeueAfter: resync}, nil
}

// The generation gate keeps the reconciler's own status writes (updatedAt
// moves on every pass under a live clock) from re-triggering it; periodic
// freshness comes from the resync requeue instead.
func (r *BudgetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("budget").
		For(&v1.Budget{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(serialWorker).
		Complete(r)
}

type staticClock struct{ now time.Time }

func (c staticClock) Now() time.Time { return c.now }
