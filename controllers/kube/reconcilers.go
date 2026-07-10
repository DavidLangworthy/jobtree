package kube

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

// FundingClosureFinalizer holds a Run in the API until its open leases are closed
// (reason RunDeleted) and its pods dropped. Its whole purpose is to make one state
// unreachable: "an open lease whose Run is absent." Because finalizers are honored
// even by `kubectl delete --force --grace-period=0`, a deleted Run can NEVER escape
// its accounting — the funding fact is closed and audited, not silently cascade-lost
// (which is why a Lease is finalizer-closed here, never owner-ref'd to the Run).
//
// It also retires R27c's report-only orphan-run sweep rule: with the Run held until
// closure, the sweep can no longer observe a lease whose Run has vanished (R12
// verification item #5). See docs/project/remediation/R12-ownerrefs-finalizers.md.
const FundingClosureFinalizer = "rq.davidlangworthy.io/funding-closure"

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
	key := keys.NamespacedKey(req.Namespace, req.Name)

	// Finalizer lifecycle is a metadata concern, and WithWorld's apply writes only
	// status — so the add/remove are direct Updates that bracket the engine. Read
	// the object uncached (APIReader) to see its DeletionTimestamp and finalizers.
	var run v1.Run
	if err := r.Bridge.APIReader.Get(ctx, req.NamespacedName, &run); err != nil {
		if apierrors.IsNotFound(err) {
			// Truly gone — the finalizer already ran, or this is a pre-R12 Run with
			// no finalizer. Backstop: release anything left behind. Lease events
			// mapped to the missing run (and the watch replay after a restart) also
			// land here, so orphans converge to cleaned up.
			return ctrl.Result{}, r.Bridge.WithWorld(ctx, func(state *controllers.ClusterState, now time.Time) error {
				cleanupDeletedRun(state, key, req.Namespace, req.Name, now)
				return nil
			})
		}
		return ctrl.Result{}, err
	}

	if run.DeletionTimestamp != nil {
		// Being deleted, held by our finalizer. Close the accounting IN-STATE so
		// WithWorld's apply writes the closures to the API, and only THEN drop the
		// finalizer — so the leases are provably closed before the Run object can
		// vanish, even under --force --grace-period=0.
		if !controllerutil.ContainsFinalizer(&run, FundingClosureFinalizer) {
			return ctrl.Result{}, nil
		}
		if err := r.Bridge.WithWorld(ctx, func(state *controllers.ClusterState, now time.Time) error {
			cleanupDeletedRun(state, key, req.Namespace, req.Name, now)
			return nil
		}); err != nil {
			return ctrl.Result{}, err // keep the finalizer: accounting is not yet closed
		}
		controllerutil.RemoveFinalizer(&run, FundingClosureFinalizer)
		return ctrl.Result{}, r.Bridge.Client.Update(ctx, &run)
	}

	// A live Run without our finalizer: install it before admitting, so a delete
	// that races admission still gets funding-closure. Adding it bumps no
	// generation; fall through and admit in the same pass (WithWorld reloads).
	if !controllerutil.ContainsFinalizer(&run, FundingClosureFinalizer) {
		controllerutil.AddFinalizer(&run, FundingClosureFinalizer)
		if err := r.Bridge.Client.Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
	}

	var parked, running, waiting bool
	err := r.Bridge.WithWorld(ctx, func(state *controllers.ClusterState, now time.Time) error {
		run, ok := state.Runs[key]
		if !ok {
			cleanupDeletedRun(state, key, req.Namespace, req.Name, now)
			return nil
		}
		rc := controllers.NewRunController(state, staticClock{now})
		rc.Period = r.Bridge.Period
		rc.Recorder = r.Bridge.recorderFor()
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
	for i := range state.Leases {
		lease := &state.Leases[i]
		if lease.Status.Closed {
			continue
		}
		leaseRun := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		if leaseRun != runKey {
			continue
		}
		// The sole closer. This used to hand-roll the same three assignments,
		// which is how a closure could be half-stamped and why nothing could be
		// instrumented in one place. hack/antifake now forbids the clone.
		controllers.CloseLease(lease, "RunDeleted", now)
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
	// A workload pod re-triggers its run only when it reaches Succeeded (so the
	// gang can finalize — B0) or its reported ETA annotation changes (so status
	// mirrors it — A). Pod creates (Pending), phase steps below Succeeded, and
	// the controller's own deletes on completion do not match, so this adds no
	// churn under the single serial worker at any reasonable ETA cadence.
	podWatch := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pod, ok := e.Object.(*corev1.Pod)
			return ok && (pod.Status.Phase == corev1.PodSucceeded || pod.Annotations[binder.EtaAnnotation] != "")
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
			newPod, ok2 := e.ObjectNew.(*corev1.Pod)
			if !ok1 || !ok2 {
				return false
			}
			if newPod.Status.Phase == corev1.PodSucceeded && oldPod.Status.Phase != corev1.PodSucceeded {
				return true
			}
			return oldPod.Annotations[binder.EtaAnnotation] != newPod.Annotations[binder.EtaAnnotation]
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
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
	// The primary watch fires on spec changes (generation) — a status write rewrites
	// float GPU-hour fields every reconcile and must NOT re-trigger, or the single
	// serial worker churns — AND on the object ENTERING deletion. A finalizer turns
	// a delete into a metadata-only update that bumps no generation, so the
	// generation gate alone would strand the Run Terminating, its finalizer forever
	// unremoved (R12). The delete of a run WITHOUT the finalizer is a real Delete
	// event, which GenerationChangedPredicate already passes.
	runPrimary := predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.Funcs{
			CreateFunc:  func(event.CreateEvent) bool { return false },
			DeleteFunc:  func(event.DeleteEvent) bool { return false },
			GenericFunc: func(event.GenericEvent) bool { return false },
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectOld.GetDeletionTimestamp() == nil && e.ObjectNew.GetDeletionTimestamp() != nil
			},
		},
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named("run").
		For(&v1.Run{}, builder.WithPredicates(runPrimary)).
		Watches(&v1.Lease{}, handler.EnqueueRequestsFromMapFunc(leaseToRun)).
		Watches(&v1.Budget{}, handler.EnqueueRequestsFromMapFunc(r.budgetToRuns),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(podToRun),
			builder.WithPredicates(podWatch)).
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
		rc.Recorder = r.Bridge.recorderFor()
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

// fenced re-reads the Node from the UNCACHED APIReader and reports whether jobtree
// may move a rank off it. It answers only on a FENCING ASSERTION: the Node object is
// gone, or it carries the out-of-service taint. Both make Pod GC force-delete the
// containers, so the rank is provably stopped.
//
// A cordon is not a failure — it means "place nothing new here", which Filter and the
// default scheduler already honour. NotReady is not a failure either: it means we
// cannot HEAR the kubelet, not that its containers stopped. Acting on either starts a
// second live copy of a rank that is still running (R21).
func (r *NodeReconciler) fenced(ctx context.Context, name types.NamespacedName) (bool, error) {
	var node corev1.Node
	if err := r.Bridge.APIReader.Get(ctx, name, &node); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, err
		}
		return true, nil // a deleted node is a fenced node
	}
	if nodeFailed(&node) {
		return true, nil
	}
	if !nodeReady(&node) {
		// Visible, and nothing else. An operator (or a fencing agent) decides whether
		// the machine is dead by deleting the Node or tainting it out-of-service.
		log.FromContext(ctx).Info("node is NotReady; taking no action (a swap needs a fencing assertion: delete the Node or taint it "+taintOutOfService+")",
			"node", name.Name)
	}
	return false, nil
}

func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Cheap filter, outside the lock: most node events are cordons, NotReady flaps
	// and status heartbeats, and none of those is a fencing assertion. Skipping them
	// here keeps the bridge mutex — which serializes every engine decision in the
	// process — free for work that matters.
	//
	// This read decides NOTHING. It is a filter, not a verdict.
	if ok, err := r.fenced(ctx, req.NamespacedName); err != nil || !ok {
		return ctrl.Result{}, err
	}

	err := r.Bridge.WithWorld(ctx, func(state *controllers.ClusterState, now time.Time) error {
		// THE VERDICT, RE-TAKEN UNDER THE LOCK. This is the whole point.
		//
		// The filter above ran before WithWorld acquired the bridge mutex, and a node
		// reconcile can sit on that mutex for seconds behind a slow admission pass.
		// In that window the world moves: a Node deleted a moment ago is recreated
		// under the same name — a kubelet re-registering, an operator replacing a
		// machine, a test rebuilding its fixture — and the scheduler plugin mints
		// fresh leases on it.
		//
		// The stale verdict then closes the leases of a node that is alive and
		// carrying work, with reason NodeFailure. The gang loses a rank it never lost,
		// and if the spare has not been minted yet it is "without spare coverage" and
		// the run is failed or parked. That is a live-node data loss, and it is what
		// task #36 was. The old code re-read the node ABOVE this call and its comment
		// claimed that "closes #36 rather than narrowing it" — but the re-read was
		// outside the critical section, so it narrowed the window instead of closing
		// it. A check that races the thing it guards is a comment, not a guard.
		//
		// Re-reading here costs one uncached GET per genuine fencing event, which is
		// rare by construction, and it is the only read whose answer cannot change
		// before it is used.
		stillFenced, err := r.fenced(ctx, req.NamespacedName)
		if err != nil {
			return err
		}
		if !stillFenced {
			log.FromContext(ctx).Info("node was fenced when the event fired and is alive now; taking no action",
				"node", req.Name)
			return nil
		}

		rc := controllers.NewRunController(state, staticClock{now})
		rc.Period = r.Bridge.Period
		rc.Recorder = r.Bridge.recorderFor()
		if err := rc.HandleNodeFailure(req.Name, now); err != nil {
			// No lease of any role named the node: the failure needs no response.
			// This used to be a string match on the message, which is how R25's
			// leaked spare lease stayed invisible -- the spare-only node returned
			// "no active lease found" and the swallow believed it.
			if errors.Is(err, controllers.ErrNoLeaseOnNode) {
				return nil
			}
			return err
		}
		return nil
	})
	return ctrl.Result{}, err
}

// nodeNotReadyGrace is how long a node may report NotReady/Unknown before jobtree
// treats it as failed and starts closing its leases.
//
// taintOutOfService is Kubernetes' sanctioned "I assert this node is dead" channel
// (`node.kubernetes.io/out-of-service`, non-graceful node shutdown, GA in 1.28). It
// is applied by a human or a fencing agent, never by the control plane on its own,
// and Pod GC's gcTerminating force-deletes the pods of a node carrying it.
const taintOutOfService = "node.kubernetes.io/out-of-service"

// nodeReady reports whether the node's Ready condition is True. A node that has
// not reported any Ready condition yet -- freshly created, kubelet not registered
// -- is not Ready. It is also not failed. Those are different questions, and only
// this one is about scheduling.
func nodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SAFETY-CRITICAL SEMANTICS.
//
// This fencing decision is modeled in:
//   - specs/NodeFailure.tla
//   - specs/NodeFailure.md
//
// If you change what qualifies as node failure here, update that spec and rerun:
//   - make node-failure-spec-check
//   - make node-failure-spec-counterexamples
//
// The path-scoped CI rail is .github/workflows/node-failure-spec.yaml.
//
// nodeFailed reports whether a node has been FENCED: something that can actually
// know has asserted the machine is dead. It is deliberately neither the negation of
// nodeUsable nor the negation of nodeReady.
//
// The only safe trigger for a swap is a fencing assertion, because a swap starts a
// second copy of a rank and jobtree cannot un-start the first one:
//
//   - CORDONED is not failed. `kubectl cordon` says "place nothing new here", not
//     "the work here is dead". Acting on one swapped a healthy rank onto a spare
//     while its pod kept running -- two live copies of one distributed-training
//     rank, which is silent corruption rather than a crash (R21).
//
//   - NOTREADY IS NOT FAILED EITHER, for any duration. NotReady means the control
//     plane cannot hear the kubelet; it does not mean the containers stopped. A
//     partitioned kubelet keeps running them. Kubernetes marks a node NotReady
//     after --node-monitor-grace-period (50s), then taint-eviction issues an
//     ORDINARY GRACEFUL delete of its pods at tolerationSeconds (300s) -- a delete
//     the unreachable kubelet never acts on, so the pod sits Terminating while its
//     container runs. Upstream says so plainly, in "Force Delete StatefulSet Pods":
//     force deletion "does not wait for confirmation from the kubelet", and doing
//     it can "lead to the duplication of a still-running Pod". A NotReady timer,
//     of any length, is a guess about a machine we cannot see.
//
// Two signals are not guesses. Both cause Pod GC to FORCE-delete (grace period 0),
// in pkg/controller/podgc/gc_controller.go:
//
//   - The Node object is gone (gcOrphaned). Deletion is itself an assertion, by the
//     cloud-controller-manager -- the instance is terminated -- or by an operator.
//     The caller handles this case; a deleted node never reaches here.
//   - The node carries `node.kubernetes.io/out-of-service` (gcTerminating).
//
// The cost of this is that a genuinely dead on-prem node whose object is never
// deleted and never tainted will not swap: the run stalls rather than corrupting.
// In cloud the CCM deletes the object automatically, which is the common path. For
// a system whose worst outcome is two live copies of one rank, stalling is the
// correct failure mode.
//
// Taking no timestamp is the point. There is no clock here, so the engine clock and
// the wall clock never meet, and a compromised kubelet -- which writes its own node
// status -- cannot backdate a LastTransitionTime to manufacture a failure.
func nodeFailed(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Key == taintOutOfService {
			return true
		}
	}
	return false
}

// nodeUsable reports whether a node's GPUs may be counted as capacity and placed
// on. It is about SCHEDULABILITY, which is a different question from nodeFailed's
// (has something asserted the machine is dead) — but a fenced node answers both.
func nodeUsable(node *corev1.Node) bool {
	if node.Spec.Unschedulable {
		return false
	}
	// A fenced node's GPUs do not exist. Leaving them in the pool let the engine
	// admit and CHARGE a run for capacity on a machine jobtree had just declared
	// dead and closed the leases of: the ledger says the GPUs are there, the
	// NoExecute taint says nothing may run on them, and the next node event closes
	// whatever was minted. The fencing taint outlives the failure it reports, so
	// this is not self-correcting.
	if nodeFailed(node) {
		return false
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SetupWithManager watches for nodes that might need failure handling — including
// create events, which is how the watch replay after a manager restart reports what
// happened while the manager was down, and how a node born broken surfaces — plus
// every delete event, since deleting a node IS its fencing assertion (filtering
// deletes through the last-known state would drop exactly those).
//
// A node DELETED while the manager was down produces no event at all, so its leases
// are not closed by this watch. That is R26's job (the ledger auditor sweeps leases
// against live nodes); it is not something a predicate can fix.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// NOT `!nodeUsable` -- that enqueues on every cordon, and a cordon is not a
	// failure (R21). Enqueue on the fencing taint, and on NotReady so Reconcile can
	// log it; Reconcile re-reads and only a fencing assertion moves anything.
	interesting := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		node, ok := obj.(*corev1.Node)
		return ok && (!nodeReady(node) || nodeFailed(node))
	})
	anyDelete := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("node").
		For(&corev1.Node{}, builder.WithPredicates(predicate.Or(interesting, anyDelete))).
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
