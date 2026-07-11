// Package plugin implements the out-of-tree kube-scheduler-framework plugin
// named "jobtree" — the sole committer of GPU funding.
//
// The controller emits real, unscheduled workload pods (schedulerName: jobtree,
// a real nvidia.com/gpu request, gang labels, an advisory nodeAffinity toward
// pack's chosen nodes). This plugin then:
//   - Filter: rejects wrong-flavor nodes (GPU fit is left to the default
//     NodeResourcesFit plugin; contiguity comes from the controller's advisory
//     nodeAffinity).
//   - Permit: gang gate — parks each member (Wait) until the whole Active set is
//     simultaneously waiting, then runs the atomic funding check and allows the
//     gang, or rejects it (→ pods pending → controller forecasts a reservation).
//   - PreBind: mints one immutable Lease per pod, funded by the envelope Permit
//     committed, on the node the scheduler actually chose. This is the one place
//     funding becomes a fact (borrow-vs-build.md §9).
//   - Reserve/Unreserve: releases gang bookkeeping for a member that is rejected
//     before it commits.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	fwk "k8s.io/kube-scheduler/framework"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// Name is the plugin name registered with the framework and referenced by the
// KubeSchedulerConfiguration profile and every extension-point enable list.
const Name = "jobtree"

// permitTimeout bounds how long a gang member parks waiting for its siblings
// and for funding. Kept well under the framework's 15m Permit cap; on timeout
// the framework rejects the member and the gang re-forms on the next attempt.
const permitTimeout = 2 * time.Minute

// JobTree is the funding-committer plugin.
type JobTree struct {
	handle fwk.Handle
	client client.Client
	gm     *gangManager
}

// Compile-time assertions that JobTree implements every extension point it
// enables. If a framework interface shifts under us, these fail at build time.
var (
	_ fwk.FilterPlugin     = (*JobTree)(nil)
	_ fwk.ScorePlugin      = (*JobTree)(nil)
	_ fwk.ReservePlugin    = (*JobTree)(nil)
	_ fwk.PermitPlugin     = (*JobTree)(nil)
	_ fwk.PreBindPlugin    = (*JobTree)(nil)
	_ fwk.PostBindPlugin   = (*JobTree)(nil)
	_ fwk.PostFilterPlugin = (*JobTree)(nil)
)

// New is the framework PluginFactory for the jobtree plugin. It builds a client
// for the jobtree CRDs (Run/Budget/Lease) plus core Nodes from the framework's
// kube config, and the gang manager that serializes funding commitment.
func New(ctx context.Context, _ apiruntime.Object, h fwk.Handle) (fwk.Plugin, error) {
	scheme := apiruntime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1.AddToScheme(scheme))
	// The scheduler configures its clients for protobuf, but the jobtree CRDs
	// (Run/Budget/Lease) are JSON-only — encoding a Lease as protobuf fails. Use
	// a JSON copy of the config for our client.
	cfg := rest.CopyConfig(h.KubeConfig())
	cfg.ContentType = "application/json"
	cfg.AcceptContentTypes = "application/json"
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("jobtree plugin: build client: %w", err)
	}
	// Reads use the same direct, read-your-write client as writes. R4 pt1 landed
	// only the hot-path observability metrics; the informer-cached reads it also
	// proposed are deferred (R4 pt1b) because an eventually-consistent reader
	// breaks the decide→mint fold's read-your-write invariant and can double-fund
	// a gang — the fold and PostBind must be made staleness-robust first.
	j := &JobTree{
		handle: h,
		client: c,
		gm:     newGangManager(c, func() time.Time { return time.Now().UTC() }),
	}
	// Rebuild in-memory gang commitments from the open leases (R2 pt3): a scheduler
	// restart otherwise resets committedCount to 0, and a lone surviving member of a
	// partially-bound gang wedges. Non-fatal — the plugin still serves fresh gangs.
	if err := j.gm.Reconstruct(ctx); err != nil {
		utilruntime.HandleError(fmt.Errorf("jobtree plugin: gang reconstruction failed; partially-bound gangs may wedge until re-admit: %w", err))
	}
	// Backstop the PostBind fast path: periodically drop gang commits abandoned
	// mid-flight so their phantom pending leases cannot leak into future funding
	// decisions and m.gangs cannot grow without bound (R1). Stops with the
	// scheduler's context.
	go j.gm.runSweep(ctx)
	return j, nil
}

// Name returns the plugin name.
func (j *JobTree) Name() string { return Name }

// Filter rejects nodes whose GPU flavor does not match the pod's run flavor.
// Whether the node has enough free GPUs is left to the default NodeResourcesFit
// plugin (the pod carries a real nvidia.com/gpu request); topology contiguity
// comes from the controller's advisory nodeAffinity honored by NodeAffinity.
func (j *JobTree) Filter(_ context.Context, _ fwk.CycleState, pod *corev1.Pod, nodeInfo fwk.NodeInfo) *fwk.Status {
	want := pod.Annotations[binder.AnnotationFlavor]
	if want == "" {
		return nil // legacy pod with no declared flavor: do not constrain
	}
	node := nodeInfo.Node()
	if node == nil {
		return fwk.NewStatus(fwk.Error, "jobtree: nil node in Filter")
	}
	if node.Labels[topology.LabelGPUFlavor] != want {
		return fwk.NewStatus(fwk.UnschedulableAndUnresolvable,
			fmt.Sprintf("jobtree: node flavor %q != run flavor %q", node.Labels[topology.LabelGPUFlavor], want))
	}
	return nil
}

// Score is neutral: node ranking within a flavor is left to the default score
// plugins, steered by the controller's advisory nodeAffinity.
func (j *JobTree) Score(_ context.Context, _ fwk.CycleState, _ *corev1.Pod, _ fwk.NodeInfo) (int64, *fwk.Status) {
	return 0, nil
}

// ScoreExtensions returns nil: no score normalization.
func (j *JobTree) ScoreExtensions() fwk.ScoreExtensions { return nil }

// Reserve is a no-op; the funding commit happens at PreBind. Its sibling
// Unreserve is where gang bookkeeping is released.
func (j *JobTree) Reserve(_ context.Context, _ fwk.CycleState, _ *corev1.Pod, _ string) *fwk.Status {
	return nil
}

// Unreserve releases a rejected member's gang state (a no-op once any member
// has begun minting, so an in-flight commit is never re-derived).
func (j *JobTree) Unreserve(_ context.Context, _ fwk.CycleState, pod *corev1.Pod, _ string) {
	j.gm.forget(pod)
}

// Permit is the gang + funding gate. Each member parks (Wait) until the whole
// Active set is simultaneously waiting; the member that completes the set runs
// the atomic funding check and either allows the gang or rejects it.
func (j *JobTree) Permit(ctx context.Context, _ fwk.CycleState, pod *corev1.Pod, _ string) (*fwk.Status, time.Duration) {
	// A node-failure swap re-places already-funded work onto capacity the
	// controller reclaimed for it, and a Promise pod is a promised-but-unfunded
	// activation the controller pre-authorized when its reservation came due
	// against an exhausted envelope (R3) — neither is new demand for the gate
	// to judge (the gate would refuse a Promise gang; that refusal is why the
	// marker exists), so both skip the gang and funding gate and are allowed
	// immediately. Their Leases are minted from the carried provenance at
	// PreBind; the evaluation classes a Promise lease — typically Unfunded,
	// re-funded by arithmetic when quota returns (R14 demote-not-kill).
	if isSwapPod(pod) || isPromisePod(pod) {
		return nil, 0
	}

	key := gangKey(pod)

	// A held spare is funded by its gang's base cover (which already funds
	// active+spares) but is NOT a gang member: it must not gate the active
	// width, and it is allowed as soon as the gang is funded. If the gang has
	// not decided yet it parks (Wait) and is released by the active completer's
	// Allow loop below, or re-checks here on the next Permit attempt — so a
	// late-arriving spare (gang already decided) does not deadlock.
	if isSparePod(pod) {
		fundable, decided := j.gm.verdict(pod)
		if !decided {
			return fwk.NewStatus(fwk.Wait, fmt.Sprintf("jobtree: spare awaiting gang %s funding", key)), permitTimeout
		}
		if !fundable {
			return fwk.NewStatus(fwk.Unschedulable, fmt.Sprintf("jobtree: gang %s not fundable, no spare", key)), 0
		}
		return nil, 0
	}

	expected := podInt(pod, binder.AnnotationExpectedWidth, 1)

	waiting := 1 // this pod (not yet in the waiting map)
	j.handle.IterateOverWaitingPods(func(wp fwk.WaitingPod) {
		// Spares share the base gangKey but are not gang members — they do not
		// count toward the active width the gate assembles.
		if gangKey(wp.GetPod()) == key && !isSparePod(wp.GetPod()) {
			waiting++
		}
	})
	// Members that already committed (claimed a payer / minted) count toward the
	// width even though they are no longer waiting: otherwise a lone member that
	// re-enters Permit after a transient PreBind/bind failure — its siblings
	// already bound and gone from the waiting set — could never re-assemble the
	// full width, and would park→timeout→loop forever, wedging the gang at N-1
	// while the run charges budget (R2). Once the gang has decided, decide()
	// below returns the cached verdict, so this only relaxes re-assembly, never
	// the first funding decision (committed is 0 until a gang funds).
	committed := j.gm.committedCount(key)
	if waiting+committed < expected {
		return fwk.NewStatus(fwk.Wait, fmt.Sprintf("jobtree: gang %s forming (%d waiting + %d committed / %d)", key, waiting, committed, expected)), permitTimeout
	}

	fundable, reason := j.gm.decide(ctx, pod)
	if !fundable {
		msg := fmt.Sprintf("jobtree: gang %s not fundable: %s", key, reason)
		j.handle.IterateOverWaitingPods(func(wp fwk.WaitingPod) {
			if gangKey(wp.GetPod()) == key {
				wp.Reject(Name, msg)
			}
		})
		return fwk.NewStatus(fwk.Unschedulable, msg), 0
	}
	// Release every parked sibling; this pod is allowed by returning Success.
	j.handle.IterateOverWaitingPods(func(wp fwk.WaitingPod) {
		if gangKey(wp.GetPod()) == key {
			wp.Allow(Name)
		}
	})
	return nil, 0
}

// PreBindPreFlight returns Success so PreBind runs (the scaffold skipped it).
func (j *JobTree) PreBindPreFlight(_ context.Context, _ fwk.CycleState, _ *corev1.Pod, _ string) (*fwk.PreBindPreFlightResult, *fwk.Status) {
	return &fwk.PreBindPreFlightResult{}, nil
}

// SAFETY-CRITICAL SEMANTICS.
//
// The swap-window / bind-time mint contract is modeled in:
//   - specs/NodeFailure.tla
//   - specs/NodeFailure.md
//
// If you change PreBind's mint semantics for swap pods or the timing of when a
// replacement lease appears, update that spec and rerun:
//   - make node-failure-spec-check
//   - make node-failure-spec-counterexamples
//
// The path-scoped CI rail is .github/workflows/node-failure-spec.yaml.
//
// PreBind mints the pod's Lease: the envelope Permit committed pays for
// gpusPerPod GPUs on the node the scheduler bound the pod to. Idempotent by pod
// name so a PreBind retry converges rather than duplicating.
func (j *JobTree) PreBind(ctx context.Context, _ fwk.CycleState, pod *corev1.Pod, nodeName string) *fwk.Status {
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Namespace: pod.Namespace, Name: pod.Labels[binder.LabelRunName]}}

	var seg cover.Segment
	gpusPerPod := podInt(pod, binder.AnnotationGPUs, 1)
	if isSwapPod(pod) || isPromisePod(pod) {
		// The pod's payer is carried on the pod (the consumed spare's provenance
		// for a swap; the envelope its activation attributed the demand to for a
		// Promise), not re-derived — continued/promised work keeps its
		// attributed envelope.
		seg = cover.Segment{
			Owner:        pod.Annotations[binder.AnnotationPayerOwner],
			Namespace:    pod.Annotations[binder.AnnotationPayerNamespace],
			BudgetName:   pod.Annotations[binder.AnnotationPayerBudget],
			EnvelopeName: pod.Annotations[binder.AnnotationPayerEnvelope],
		}
		if seg.Owner == "" || seg.EnvelopeName == "" {
			return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: %s pod %s/%s missing funding provenance", pod.Annotations[binder.AnnotationLeaseReason], pod.Namespace, pod.Name))
		}
		if isSwapPod(pod) {
			// Defense-in-depth (R5): the swap path trusts pod-carried provenance and
			// skips the funding gate, so require that provenance to match a Spare lease
			// the run actually held. A forged swap pod cannot then mint against an
			// arbitrary victim envelope — only one for which a real spare exists.
			if !j.gm.spareLeaseProvenanceValid(ctx, pod.Namespace, pod.Labels[binder.LabelRunName], seg) {
				return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: swap pod %s/%s provenance (%s/%s/%s) matches no spare lease for its run", pod.Namespace, pod.Name, seg.Owner, seg.BudgetName, seg.EnvelopeName))
			}
		} else {
			// Defense-in-depth (R3, mirrors R5): a Promise pod also skips the
			// funding gate on carried provenance, so require the CHARGED envelope
			// (payer-budget/envelope — the fields funding.Evaluate actually bills)
			// to belong to the pod's own Run's owner, the only party the
			// controller's opportunisticCoverPlan ever attributes a promise to. A
			// forged Promise pod cannot then charge a victim's budget. The R5/R6
			// policy already restricts these annotations to the controller
			// ServiceAccount; this holds even where that policy is not enabled.
			if !j.gm.promiseProvenanceValid(ctx, pod.Namespace, pod.Labels[binder.LabelRunName], seg) {
				return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: promise pod %s/%s provenance (%s/%s/%s) does not name an envelope its run's owner owns", pod.Namespace, pod.Name, seg.Owner, seg.BudgetName, seg.EnvelopeName))
			}
		}
	} else {
		var ok bool
		seg, gpusPerPod, ok = j.gm.claimPayer(pod)
		if !ok {
			return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: no funding claim for pod %s/%s", pod.Namespace, pod.Name))
		}
	}

	role := pod.Labels[binder.LabelRunRole]
	if role == "" {
		role = binder.RoleActive
	}
	groupIndex, err := mintGroupIndex(pod)
	if err != nil {
		return fwk.NewStatus(fwk.Error, "jobtree: "+err.Error())
	}
	// Include the run's per-incarnation nonce in the lease name so a delete +
	// resubmit of a same-named Run mints a fresh OPEN lease rather than colliding
	// with the prior incarnation's closed lease (the ABA hazard, R2). A same-
	// incarnation PreBind retry uses the same nonce, so it stays idempotent
	// (IsAlreadyExists on its own open lease).
	leaseName := pod.Name + "-lease"
	if nonce := pod.Annotations[binder.AnnotationRunNonce]; nonce != "" {
		leaseName = pod.Name + "-" + nonce + "-lease"
	}
	lease := admission.PodLeaseWithRole(run, seg, nodeName, gpusPerPod, leaseName, time.Now().UTC(), pod.Annotations[binder.AnnotationLeaseReason], role, groupIndex)
	// Durable gang identity on the lease (R2 pt3 / R4 pt1b foundation): the cohort it
	// belongs to and the exact pod it funds, so a scheduler restart can rebuild gang
	// membership from the leases alone rather than string-parsing the lease name.
	admission.StampGangIdentity(&lease, cohortOf(pod), pod.Name)
	if err := j.client.Create(ctx, &lease); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: mint lease for %s: %v", pod.Name, err))
		}
		// IsAlreadyExists is success ONLY for a same-incarnation PreBind retry of THIS
		// gang's own OPEN lease. The run nonce (R2) makes the name unique per
		// incarnation, so a collision with a CLOSED lease (a prior incarnation's, the
		// ABA hazard) or a lease of another run means the mint did not happen for us —
		// swallowing it would leave this gang running with no open lease of its own,
		// unfunded work the controller never adopts. Reject so the pod retries.
		var existing v1.Lease
		if gerr := j.client.Get(ctx, client.ObjectKey{Namespace: lease.Namespace, Name: leaseName}, &existing); gerr != nil {
			return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: lease %s exists but cannot be read: %v", leaseName, gerr))
		}
		if existing.Status.Closed || existing.Spec.RunRef.Name != run.Name || existing.Spec.RunRef.Namespace != run.Namespace {
			return fwk.NewStatus(fwk.Error, fmt.Sprintf(
				"jobtree: lease %s already exists but is not this gang's open lease (closed=%v run=%s/%s); refusing to treat as minted",
				leaseName, existing.Status.Closed, existing.Spec.RunRef.Namespace, existing.Spec.RunRef.Name))
		}
	}
	// The real lease now exists in the API (created here, or already present on a
	// retry). Retire this pod's phantom pending lease so the gang stops folding it
	// into other funding decisions — the real lease already counts (R1). A swap
	// pod is not gang-tracked, so this is a no-op for it.
	j.gm.notifyMinted(pod)
	return nil
}

// PostBind releases a gang's in-memory commit once all its pods have minted and
// bound: the API's real leases are then the sole source of truth, so keeping the
// commit (and its phantom pending-lease guards) would double-count the gang's
// funding forever and leak the gang map (R1). A gang with an unbound member is
// left intact for recovery + the TTL sweep.
func (j *JobTree) PostBind(_ context.Context, _ fwk.CycleState, pod *corev1.Pod, _ string) {
	j.gm.postBind(pod)
}

// isSwapPod reports whether a pod is a node-failure swap re-placement (minted
// from carried provenance, funding-gate-exempt).
func isSwapPod(pod *corev1.Pod) bool {
	return pod.Annotations[binder.AnnotationLeaseReason] == "Swap"
}

// isPromisePod reports whether a pod is a promised-but-unfunded activation
// (R3): pre-authorized by the controller when its reservation came due against
// an exhausted envelope, minted from carried provenance, funding-gate-exempt.
// The evaluation classes the minted lease — typically Unfunded until quota
// returns.
func isPromisePod(pod *corev1.Pod) bool {
	return pod.Annotations[binder.AnnotationLeaseReason] == binder.LeaseReasonPromise
}

// isSparePod reports whether a pod is a held spare — funded by its gang's base
// cover but not a gang member (it does not gate the active width).
func isSparePod(pod *corev1.Pod) bool {
	return pod.Labels[binder.LabelRunRole] == binder.RoleSpare
}

// PostFilter is a no-op: it reclaims nothing (reclaim stays a controller
// re-derivation per §9; wiring it through PostFilter is a later increment).
func (j *JobTree) PostFilter(_ context.Context, _ fwk.CycleState, _ *corev1.Pod, _ fwk.NodeToStatusReader) (*fwk.PostFilterResult, *fwk.Status) {
	return nil, fwk.NewStatus(fwk.Unschedulable, "jobtree: reclaim not wired (PLUGIN-6)")
}

// ErrNoPlacementGroup is returned when a pod reaching PreBind carries no placement
// group. It is a typed sentinel because the caller must not distinguish it by message
// text — that is how R25's leaked spare lease stayed invisible.
var ErrNoPlacementGroup = errors.New("pod carries no placement group")

// mintGroupIndex reads the placement group a lease must be stamped with, and FAILS
// CLOSED when the pod carries none (R28b).
//
// Every pod jobtree emits carries one. A pod that does not is either not ours, or the
// relic of a mint path that forgot. Minting a lease without it silently merges the
// run's groups into one: pkg/resolver then cuts whole runs instead of groups, the
// elastic loop shrinks in whole-run units, and a reclaim asking for "the pods of this
// group" gets the pods of the entire run. The ledger cannot detect that lie, so refuse
// it here — the way the R5/R6 provenance gates refuse forged funding.
func mintGroupIndex(pod *corev1.Pod) (string, error) {
	group := pod.Labels[binder.LabelGroupIndex]
	if group == "" {
		return "", fmt.Errorf("%w: %s/%s has no %s label; refusing to mint a lease with no placement group",
			ErrNoPlacementGroup, pod.Namespace, pod.Name, binder.LabelGroupIndex)
	}
	return group, nil
}
