package kube

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
)

// R26 — the ledger auditor, the runtime backstop.
//
// The point fixes (R8's failure edge, R25's spare-node close, eviction recovery)
// each close a KNOWN leak at its cause, immediately and causally. The auditor is
// the net UNDER them: a periodic sweep that reads the whole world and checks the
// two invariants in controllers/ledger_audit.go, so ledger integrity is a CHECKED
// property rather than the sum of the leaks we have thought of. It catches the ones
// we have not.
//
// It is deliberately slow and conservative. It closes a lease only after the same
// discrepancy has persisted across a GRACE window, so a healthy in-flight
// mint/swap — a failed lease closed one instant, its replacement pod minted the
// next — is never "repaired." The grace window must exceed that swap window, which
// is why it defaults to 2× the sweep interval and is clamped up if set smaller.
//
// The destructive direction is budget-safe only: it CLOSES leases (stops a charge),
// reason "Orphaned", via the sole closer. It never deletes a pod — a pod running
// without a lease is alarmed, not killed, because killing it is a policy call
// (R5/R6 territory) the auditor does not own.
type LedgerAuditor struct {
	Client    client.Client
	APIReader client.Reader
	Clock     controllers.Clock
	Recorder  record.EventRecorder

	// Interval is the sweep cadence (default 2m).
	Interval time.Duration
	// Grace is how long a violation must persist before the auditor acts. It must
	// exceed the swap window; it is clamped to at least 2× Interval.
	Grace time.Duration

	mu sync.Mutex
	// firstSeen records when each still-live violation was first observed, keyed by
	// violationKey. An entry is dropped the sweep a violation stops appearing, so a
	// blip that heals never matures — the map holds only sustained discrepancies.
	firstSeen map[string]time.Time
}

// SetupWithManager registers the auditor as a manager Runnable — it is a periodic
// sweep, not an object reconciler, so it drives itself off a ticker rather than a
// watch.
func (a *LedgerAuditor) SetupWithManager(mgr ctrl.Manager) error {
	a.defaults()
	return mgr.Add(a)
}

func (a *LedgerAuditor) defaults() {
	if a.Interval <= 0 {
		a.Interval = 2 * time.Minute
	}
	if a.Grace < 2*a.Interval {
		// The grace window MUST outlast the swap window (failed lease closed → swap
		// pod minted at PreBind) or the auditor races the recovery it is backstopping
		// and closes a lease a swap is about to reuse.
		a.Grace = 2 * a.Interval
	}
	if a.Clock == nil {
		a.Clock = controllers.RealClock{}
	}
	if a.firstSeen == nil {
		a.firstSeen = map[string]time.Time{}
	}
}

// Start runs the sweep loop until the context is cancelled. Implements
// manager.Runnable.
func (a *LedgerAuditor) Start(ctx context.Context) error {
	a.defaults()
	ticker := time.NewTicker(a.Interval)
	defer ticker.Stop()
	logger := log.FromContext(ctx).WithName("ledger-auditor")
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := a.Sweep(ctx); err != nil {
				// A sweep failure is transient (a List error); the next tick retries.
				// The auditor must never take the manager down — it is the safety net,
				// not a load-bearing path.
				logger.Error(err, "ledger audit sweep failed; will retry next interval")
			}
		}
	}
}

// Sweep runs one audit pass: load the world, judge it, mature the persistent
// violations against the grace window, then repair (close) the matured repairable
// ones and alarm the rest. Exposed so envtest drives a deterministic single pass
// with a controllable clock instead of waiting on the ticker.
func (a *LedgerAuditor) Sweep(ctx context.Context) error {
	world, runs, err := a.loadWorld(ctx)
	if err != nil {
		return err
	}
	now := a.Clock.Now()
	violations := controllers.AuditLedger(world)

	a.mu.Lock()
	// Age the tracker: stamp new violations, drop healed ones, and pick out those
	// that have now persisted past the grace window.
	seenThisSweep := make(map[string]bool, len(violations))
	matured := make([]controllers.LedgerViolation, 0, len(violations))
	for _, v := range violations {
		key := violationKey(v)
		seenThisSweep[key] = true
		first, ok := a.firstSeen[key]
		if !ok {
			a.firstSeen[key] = now
			first = now
		}
		if now.Sub(first) >= a.Grace {
			matured = append(matured, v)
		}
	}
	for key := range a.firstSeen {
		if !seenThisSweep[key] {
			delete(a.firstSeen, key)
		}
	}
	a.mu.Unlock()

	// The gauge reports SUSTAINED violations — the ones past grace — so a transient
	// mint/swap window never shows as drift. Enumerate all three kinds so a kind that
	// cleared is published as 0, not left at its last value.
	counts := map[string]float64{
		controllers.ViolationLeaseNoPod:    0,
		controllers.ViolationLeaseDeadNode: 0,
		controllers.ViolationPodNoLease:    0,
	}
	for _, v := range matured {
		counts[v.Kind]++
	}
	metrics.SetLedgerViolations(counts)

	// Act on the matured violations.
	for _, v := range matured {
		if v.Repairable {
			if err := a.repair(ctx, v, runs[v.RunKey], now); err != nil {
				log.FromContext(ctx).Error(err, "ledger audit could not close an orphaned lease",
					"lease", v.LeaseName, "namespace", v.LeaseNamespace)
			}
			continue
		}
		a.alarm(v, runs[v.RunKey])
	}
	return nil
}

// repair closes an orphaned lease through the sole closer and records it as both a
// repair metric and a Warning on the owning Run. It re-reads the lease first so the
// close is against the live object, and it is idempotent: a lease something else
// already closed in the meantime is left as it is.
func (a *LedgerAuditor) repair(ctx context.Context, v controllers.LedgerViolation, run *v1.Run, now time.Time) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var lease v1.GPULease
		if err := a.Client.Get(ctx, client.ObjectKey{Namespace: v.LeaseNamespace, Name: v.LeaseName}, &lease); err != nil {
			if apierrors.IsNotFound(err) {
				return nil // the lease is gone; nothing to close.
			}
			return err
		}
		if lease.Status.Closed {
			return nil // some causal path closed it first — the good outcome.
		}
		// The sole closer stamps Closed/Ended/ClosureReason and the mirrored
		// conditions; the auditor is not a second writer of those fields (that is what
		// keeps hack/antifake's sole-closer lint true).
		controllers.CloseLease(&lease, controllers.ClosureReasonOrphaned, now)
		return a.Client.Status().Update(ctx, &lease)
	})
	if err != nil {
		return err
	}
	metrics.IncLedgerRepair(v.Kind)
	if a.Recorder != nil && run != nil {
		a.Recorder.Eventf(run, corev1.EventTypeWarning, "LeaseOrphaned",
			"ledger auditor closed lease %s: %s", v.LeaseName, v.Detail)
	}
	return nil
}

// alarm reports a report-only violation (a run with live pods and no lease) without
// touching anything. The Event API aggregates repeats, so a persisting condition
// becomes one Event with a rising count rather than one per sweep.
func (a *LedgerAuditor) alarm(v controllers.LedgerViolation, run *v1.Run) {
	if a.Recorder == nil || run == nil {
		return
	}
	a.Recorder.Eventf(run, corev1.EventTypeWarning, "PodWithoutLease",
		"ledger auditor: %s", v.Detail)
}

// loadWorld lists the two planes for one sweep — the world is read ONCE, not per
// lease. Nodes are listed WITHOUT the usability filter the bridge applies: the
// dead-node rule keys off whether the Node OBJECT exists, and a NotReady node still
// exists (fencing — a swap is justified by a deleted node or the out-of-service
// taint, never by NotReady).
func (a *LedgerAuditor) loadWorld(ctx context.Context) (controllers.LedgerWorld, map[string]*v1.Run, error) {
	var leaseList v1.GPULeaseList
	if err := a.APIReader.List(ctx, &leaseList); err != nil {
		return controllers.LedgerWorld{}, nil, fmt.Errorf("list leases: %w", err)
	}
	var nodeList corev1.NodeList
	if err := a.APIReader.List(ctx, &nodeList); err != nil {
		return controllers.LedgerWorld{}, nil, fmt.Errorf("list nodes: %w", err)
	}
	var podList corev1.PodList
	if err := a.APIReader.List(ctx, &podList, client.HasLabels{binder.LabelRunName}); err != nil {
		return controllers.LedgerWorld{}, nil, fmt.Errorf("list pods: %w", err)
	}
	var runList v1.RunList
	if err := a.APIReader.List(ctx, &runList); err != nil {
		return controllers.LedgerWorld{}, nil, fmt.Errorf("list runs: %w", err)
	}

	nodeNames := make(map[string]bool, len(nodeList.Items))
	for i := range nodeList.Items {
		nodeNames[nodeList.Items[i].Name] = true
	}
	runs := make(map[string]*v1.Run, len(runList.Items))
	for i := range runList.Items {
		run := &runList.Items[i]
		runs[keys.NamespacedKey(run.Namespace, run.Name)] = run
	}
	pods := make([]controllers.LedgerPod, 0, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		pods = append(pods, controllers.LedgerPod{
			Namespace:   pod.Namespace,
			Name:        pod.Name,
			RunName:     pod.Labels[binder.LabelRunName],
			Phase:       string(pod.Status.Phase),
			Terminating: !pod.DeletionTimestamp.IsZero(),
		})
	}
	return controllers.LedgerWorld{
		Runs:      runs,
		Leases:    leaseList.Items,
		Pods:      pods,
		NodeNames: nodeNames,
	}, runs, nil
}

// violationKey is the stable identity of a violation across sweeps, so its age can
// be tracked. lease_* keys on the lease; pod_no_lease keys on the run (it is a
// run-level condition — a run with live pods and no lease).
func violationKey(v controllers.LedgerViolation) string {
	if v.Kind == controllers.ViolationPodNoLease {
		return v.Kind + "|" + v.RunKey
	}
	return v.Kind + "|" + keys.NamespacedKey(v.LeaseNamespace, v.LeaseName)
}
