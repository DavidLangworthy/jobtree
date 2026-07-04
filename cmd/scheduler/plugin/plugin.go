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
	_ fwk.PostFilterPlugin = (*JobTree)(nil)
)

// New is the framework PluginFactory for the jobtree plugin. It builds a client
// for the jobtree CRDs (Run/Budget/Lease) plus core Nodes from the framework's
// kube config, and the gang manager that serializes funding commitment.
func New(_ context.Context, _ apiruntime.Object, h fwk.Handle) (fwk.Plugin, error) {
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
	return &JobTree{
		handle: h,
		client: c,
		gm:     newGangManager(c, func() time.Time { return time.Now().UTC() }),
	}, nil
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
	// controller reclaimed for it — it is not new demand, so it skips the gang
	// and funding gate entirely and is allowed immediately. Its Lease is minted
	// from the carried provenance at PreBind.
	if isSwapPod(pod) {
		return nil, 0
	}

	key := gangKey(pod)
	expected := podInt(pod, binder.AnnotationExpectedWidth, 1)

	waiting := 1 // this pod (not yet in the waiting map)
	j.handle.IterateOverWaitingPods(func(wp fwk.WaitingPod) {
		if gangKey(wp.GetPod()) == key {
			waiting++
		}
	})
	if waiting < expected {
		return fwk.NewStatus(fwk.Wait, fmt.Sprintf("jobtree: gang %s forming (%d/%d)", key, waiting, expected)), permitTimeout
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

// PreBind mints the pod's Lease: the envelope Permit committed pays for
// gpusPerPod GPUs on the node the scheduler bound the pod to. Idempotent by pod
// name so a PreBind retry converges rather than duplicating.
func (j *JobTree) PreBind(ctx context.Context, _ fwk.CycleState, pod *corev1.Pod, nodeName string) *fwk.Status {
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Namespace: pod.Namespace, Name: pod.Labels[binder.LabelRunName]}}

	var seg cover.Segment
	gpusPerPod := podInt(pod, binder.AnnotationGPUs, 1)
	if isSwapPod(pod) {
		// The swap's payer is carried on the pod (the spare's provenance), not
		// re-derived — so continued work keeps its original envelope.
		seg = cover.Segment{
			Owner:        pod.Annotations[binder.AnnotationPayerOwner],
			BudgetName:   pod.Annotations[binder.AnnotationPayerBudget],
			EnvelopeName: pod.Annotations[binder.AnnotationPayerEnvelope],
		}
		if seg.Owner == "" || seg.EnvelopeName == "" {
			return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: swap pod %s/%s missing funding provenance", pod.Namespace, pod.Name))
		}
	} else {
		var ok bool
		seg, gpusPerPod, ok = j.gm.claimPayer(pod)
		if !ok {
			return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: no funding claim for pod %s/%s", pod.Namespace, pod.Name))
		}
	}

	lease := admission.PodLease(run, seg, nodeName, gpusPerPod, pod.Name+"-lease", time.Now().UTC(), pod.Annotations[binder.AnnotationLeaseReason])
	if err := j.client.Create(ctx, &lease); err != nil && !apierrors.IsAlreadyExists(err) {
		return fwk.NewStatus(fwk.Error, fmt.Sprintf("jobtree: mint lease for %s: %v", pod.Name, err))
	}
	return nil
}

// isSwapPod reports whether a pod is a node-failure swap re-placement (minted
// from carried provenance, funding-gate-exempt).
func isSwapPod(pod *corev1.Pod) bool {
	return pod.Annotations[binder.AnnotationLeaseReason] == "Swap"
}

// PostFilter is a no-op: it reclaims nothing (reclaim stays a controller
// re-derivation per §9; wiring it through PostFilter is a later increment).
func (j *JobTree) PostFilter(_ context.Context, _ fwk.CycleState, _ *corev1.Pod, _ fwk.NodeToStatusReader) (*fwk.PostFilterResult, *fwk.Status) {
	return nil, fwk.NewStatus(fwk.Unschedulable, "jobtree: reclaim not wired (PLUGIN-6)")
}
