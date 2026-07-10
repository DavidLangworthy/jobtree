// Package kube wires the pure in-memory engine (controllers.RunController
// and friends) to a real Kubernetes API server via controller-runtime. Each
// reconcile loads a consistent world snapshot, runs the engine against it,
// and applies the resulting state diff back through the API.
package kube

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/invariant"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

const (
	// GPUCapacityResource is the node capacity key the engine reads GPU
	// counts from (the fake or real device plugin advertises it).
	GPUCapacityResource = "nvidia.com/gpu"
	// PodGPUAnnotation records how many GPUs of its node a workload pod
	// claims. Aliased to the shared binder constant so the plugin reads the
	// same key.
	PodGPUAnnotation = binder.AnnotationGPUs
	// schedulerName routes emitted workload pods to the jobtree scheduler
	// plugin (the sole committer) instead of the default scheduler.
	schedulerName = "jobtree"
	// defaultWorkloadImage is the container for a legacy Roles-less Run (no
	// template). It runs a real, terminating command so completion is real —
	// unlike the old pause mannequin that never exited.
	defaultWorkloadImage = "busybox:1.36"
)

// Bridge loads ClusterState snapshots from the API server and applies the
// engine's mutations back.
//
// All world access is serialized through one mutex and reads bypass the
// informer cache: specs/BudgetConservation.tla shows that concurrent
// admissions deciding from stale snapshots overspend envelopes, so exactly
// one engine evaluation runs at a time, reading its own writes.
type Bridge struct {
	Client    client.Client
	APIReader client.Reader
	Clock     controllers.Clock
	// Period is the accounting horizon for the funding derivation's
	// admission lookahead (<= 0 uses funding.DefaultPeriod).
	Period time.Duration
	// Recorder emits real corev1.Events for the engine's admit/reserve/
	// activate/resolver-action/swap/complete transitions. Nil is safe (no
	// events are emitted); cmd/manager wires mgr.GetEventRecorderFor.
	Recorder record.EventRecorder

	mu sync.Mutex
}

// engineRecorder adapts a real client-go/controller-runtime EventRecorder to
// the pure engine's minimal controllers.EventRecorder interface, so
// pkg/controllers never has to import k8s.io/client-go itself.
type engineRecorder struct{ rec record.EventRecorder }

func (e engineRecorder) Event(run *v1.Run, eventType, reason, message string) {
	if e.rec == nil || run == nil {
		return
	}
	e.rec.Event(run, eventType, reason, message)
}

// recorderFor returns the engine-facing recorder adapter, or nil when the
// Bridge has none configured (nil is a valid, safe controllers.EventRecorder
// value — RunController.emit checks for it).
func (b *Bridge) recorderFor() controllers.EventRecorder {
	if b.Recorder == nil {
		return nil
	}
	return engineRecorder{rec: b.Recorder}
}

type worldSnapshot struct {
	state        *controllers.ClusterState
	runs         map[string]*v1.Run
	leases       map[string]*v1.Lease
	reservations map[string]*v1.Reservation
	pods         map[string]*corev1.Pod
}

// WithWorld runs fn against a freshly loaded world snapshot and applies the
// state diff. The engine error is returned after the diff is applied — a
// failed admission may still have recorded status changes worth persisting.
func (b *Bridge) WithWorld(ctx context.Context, fn func(state *controllers.ClusterState, now time.Time) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	snap, err := b.load(ctx)
	if err != nil {
		return err
	}
	now := b.Clock.Now()
	fnErr := fn(snap.state, now)
	// After the engine — it is allowed to pass THROUGH illegal states, and only its
	// return is a postcondition. Before apply — a lease this closes and a pod this
	// drops must both reach the API in this same pass. Under the mutex, because a
	// verdict taken outside it is a verdict about a world that has already moved.
	b.reportSweep(ctx, controllers.SettleLeases(snap.state, now), snap)
	if applyErr := b.apply(ctx, snap); applyErr != nil {
		return applyErr
	}
	return fnErr
}

// reportSweep turns the sweep's repairs into the loudest signal each environment
// can carry: a test failure in CI, a Warning event and a counter in production.
//
// The asymmetry is the point. In a test binary a terminal-run closure means a path
// under test shirked a duty it owned, and the test that exercised it passed anyway
// — that is precisely the failure mode pkg/invariant exists to end, so it must not
// be survivable. In a cluster the same closure must never take the scheduler down:
// it heals, it complains, and an operator alerts on jobtree_swept_leases_total.
func (b *Bridge) reportSweep(ctx context.Context, sweep controllers.Sweep, snap *worldSnapshot) {
	if sweep.Empty() {
		return
	}
	log := ctrllog.FromContext(ctx)
	for _, lease := range sweep.Leases {
		metrics.IncSweptLease(lease.Rule)
		log.Info("settle sweep closed a lease no path closed",
			"lease", lease.Name, "namespace", lease.Namespace, "run", lease.RunKey, "rule", lease.Rule)
		if run := snap.state.Runs[lease.RunKey]; run != nil && b.Recorder != nil {
			b.Recorder.Eventf(run, corev1.EventTypeWarning, "LeaseSwept",
				"closed lease %s left open by a %s run; it was charging its budget and holding its GPUs",
				lease.Name, run.Status.Phase)
		}
	}
	if sweep.Pods > 0 {
		log.Info("settle sweep dropped a terminal run's pods", "count", sweep.Pods)
	}

	shirked := sweep.Shirked()
	if len(shirked) == 0 || !invariant.UnderTest() {
		return
	}
	violations := make([]invariant.Violation, 0, len(shirked))
	for _, lease := range shirked {
		violations = append(violations, invariant.Violation{
			ID:      invariant.TerminalPresent,
			Subject: "lease " + lease.Name,
			Detail: fmt.Sprintf("the settle sweep had to close it: run %s is terminal and still held it open. "+
				"Some path made that run terminal without calling releaseRun, and its own test passed", lease.RunKey),
		})
	}
	invariant.Report("Bridge.SettleLeases", violations)
}

func (b *Bridge) load(ctx context.Context) (*worldSnapshot, error) {
	var runList v1.RunList
	if err := b.APIReader.List(ctx, &runList); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	var budgetList v1.BudgetList
	if err := b.APIReader.List(ctx, &budgetList); err != nil {
		return nil, fmt.Errorf("list budgets: %w", err)
	}
	var leaseList v1.LeaseList
	if err := b.APIReader.List(ctx, &leaseList); err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	var reservationList v1.ReservationList
	if err := b.APIReader.List(ctx, &reservationList); err != nil {
		return nil, fmt.Errorf("list reservations: %w", err)
	}
	var nodeList corev1.NodeList
	if err := b.APIReader.List(ctx, &nodeList); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var podList corev1.PodList
	if err := b.APIReader.List(ctx, &podList, client.HasLabels{binder.LabelRunName}); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	state := &controllers.ClusterState{
		Runs:         make(map[string]*v1.Run, len(runList.Items)),
		Budgets:      budgetList.Items,
		Leases:       leaseList.Items,
		Reservations: make(map[string]*v1.Reservation, len(reservationList.Items)),
	}
	snap := &worldSnapshot{
		state:        state,
		runs:         make(map[string]*v1.Run, len(runList.Items)),
		leases:       make(map[string]*v1.Lease, len(leaseList.Items)),
		reservations: make(map[string]*v1.Reservation, len(reservationList.Items)),
		pods:         make(map[string]*corev1.Pod, len(podList.Items)),
	}

	for i := range runList.Items {
		run := &runList.Items[i]
		key := keys.NamespacedKey(run.Namespace, run.Name)
		state.Runs[key] = run
		snap.runs[key] = run.DeepCopy()
	}
	for i := range leaseList.Items {
		lease := &leaseList.Items[i]
		snap.leases[keys.NamespacedKey(lease.Namespace, lease.Name)] = lease.DeepCopy()
	}
	for i := range reservationList.Items {
		res := &reservationList.Items[i]
		key := keys.NamespacedKey(res.Namespace, res.Name)
		state.Reservations[key] = res
		snap.reservations[key] = res.DeepCopy()
	}
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		// An unusable node's GPUs must not look schedulable: the node
		// reconciler evacuates such nodes by closing their leases, which
		// would otherwise make the dead capacity immediately re-admittable.
		if !nodeUsable(node) {
			continue
		}
		gpus := 0
		if qty, ok := node.Status.Capacity[GPUCapacityResource]; ok {
			gpus = int(qty.Value())
		}
		state.Nodes = append(state.Nodes, topology.SourceNode{
			Name:   node.Name,
			Labels: node.Labels,
			GPUs:   gpus,
		})
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		gpus := 0
		if v, ok := pod.Annotations[PodGPUAnnotation]; ok {
			if parsed, err := strconv.Atoi(v); err == nil {
				gpus = parsed
			}
		}
		state.Pods = append(state.Pods, binder.PodManifest{
			Namespace:   pod.Namespace,
			Name:        pod.Name,
			NodeName:    pod.Spec.NodeName,
			GPUs:        gpus,
			Labels:      pod.Labels,
			Annotations: pod.Annotations,
			Phase:       string(pod.Status.Phase),
			// A bound pod that has been Delete()'d lingers with a
			// DeletionTimestamp until the kubelet finalizes it. List still
			// returns it every pass; the oracle must not read it as a live
			// container (see PodManifest.Terminating and snapshotWorld).
			Terminating: !pod.DeletionTimestamp.IsZero(),
		})
		snap.pods[keys.NamespacedKey(pod.Namespace, pod.Name)] = pod
	}
	return snap, nil
}

func (b *Bridge) apply(ctx context.Context, snap *worldSnapshot) error {
	state := snap.state

	// Leases: the engine creates new ones and closes existing ones; it
	// never deletes and never mutates an existing spec (immutability).
	for i := range state.Leases {
		lease := &state.Leases[i]
		key := keys.NamespacedKey(lease.Namespace, lease.Name)
		before, existed := snap.leases[key]
		if !existed {
			created := lease.DeepCopy()
			created.ResourceVersion = ""
			// Status is a subresource: Create silently drops it, and a
			// lease can be created and closed within one engine pass
			// (e.g. a just-materialized lease lost to the resolver
			// lottery in the same ActivateReservations call).
			status := lease.Status
			if err := b.Client.Create(ctx, created); err != nil {
				return fmt.Errorf("create lease %s: %w", key, err)
			}
			if !reflect.DeepEqual(status, v1.LeaseStatus{}) {
				created.Status = status
				if err := b.Client.Status().Update(ctx, created); err != nil {
					return fmt.Errorf("update lease status %s: %w", key, err)
				}
			}
			continue
		}
		if !reflect.DeepEqual(before.Status, lease.Status) {
			if err := b.Client.Status().Update(ctx, lease); err != nil {
				return fmt.Errorf("update lease status %s: %w", key, err)
			}
		}
	}

	// Pods: created for new slices, deleted when their groups close.
	current := make(map[string]binder.PodManifest, len(state.Pods))
	for _, pod := range state.Pods {
		current[keys.NamespacedKey(pod.Namespace, pod.Name)] = pod
	}
	for key, manifest := range current {
		if _, existed := snap.pods[key]; !existed {
			run := state.Runs[keys.NamespacedKey(manifest.Namespace, manifest.Labels[binder.LabelRunName])]
			if err := b.Client.Create(ctx, buildPod(manifest, run)); err != nil {
				return fmt.Errorf("create pod %s: %w", key, err)
			}
		}
	}
	for key, pod := range snap.pods {
		if _, still := current[key]; !still {
			if err := b.Client.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete pod %s: %w", key, err)
			}
		}
	}

	// Reservations: created by planning, deleted when superseded, status
	// updated on release/failure/reschedule.
	for key, res := range state.Reservations {
		before, existed := snap.reservations[key]
		if !existed {
			created := res.DeepCopy()
			created.ResourceVersion = ""
			// Own the Reservation to its Run so a deleted Run's reservation is
			// garbage-collected by the apiserver, not left for cleanupDeletedRun to
			// find (R12). Unlike a Lease, a Reservation is a planning artifact with
			// no funding to audit, so cascade-deletion is exactly right for it.
			owner := state.Runs[keys.NamespacedKey(res.Spec.RunRef.Namespace, res.Spec.RunRef.Name)]
			created.OwnerReferences = runOwnerReferences(owner)
			status := res.Status
			if err := b.Client.Create(ctx, created); err != nil {
				return fmt.Errorf("create reservation %s: %w", key, err)
			}
			if !reflect.DeepEqual(status, v1.ReservationStatus{}) {
				created.Status = status
				if err := b.Client.Status().Update(ctx, created); err != nil {
					return fmt.Errorf("update reservation status %s: %w", key, err)
				}
			}
			continue
		}
		if !reflect.DeepEqual(before.Status, res.Status) {
			if err := b.Client.Status().Update(ctx, res); err != nil {
				return fmt.Errorf("update reservation status %s: %w", key, err)
			}
		}
	}
	for key, res := range snap.reservations {
		if _, still := state.Reservations[key]; !still {
			if err := b.Client.Delete(ctx, res); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete reservation %s: %w", key, err)
			}
		}
	}

	// Runs: the engine only mutates status.
	for key, run := range state.Runs {
		before, existed := snap.runs[key]
		if !existed {
			continue
		}
		if !reflect.DeepEqual(before.Status, run.Status) {
			if err := b.Client.Status().Update(ctx, run); err != nil {
				return fmt.Errorf("update run status %s: %w", key, err)
			}
		}
	}
	return nil
}

// buildPod renders an engine PodManifest into a real, UNSCHEDULED workload pod
// for the jobtree scheduler plugin to place and fund. It never sets
// spec.nodeName (the plugin/scheduler owns placement); it overlays only the
// scheduling-owned fields onto the researcher's role Template — schedulerName,
// the nvidia.com/gpu request==limit on the GPU-target container, gang/flavor
// annotations the plugin reads, an advisory nodeAffinity toward pack's chosen
// node, and RestartPolicy=Never so a Succeeded pod is a reliable completion
// signal. Everything else in the template (image, command, env, volumes) is the
// researcher's, preserved verbatim. A Roles-less legacy Run gets a real
// terminating default container instead of the old pause mannequin.
//
// # THE ROLE→POD MAPPING CONTRACT
//
// jobtree owns its pods. JobSet was evaluated as the substrate and rejected: it
// cannot express the spare swap (a rank moves to a pre-funded slot on another
// node, preserving funding provenance) or delta-funded elastic width (funding
// needs a distinct admission unit per delta, which resizing parallelism cannot
// express). See docs/project/remediation/R9-jobset-amendment.md. We keep JobSet's
// SHAPE as a reference contract so researcher workloads behave as if it were the
// substrate, and this function is where that contract is honoured. It was carried
// here verbatim from pkg/lowering, which is now deleted (R9 phase 9A-0); the
// seam it described never existed, and a skeleton that returns ErrNotImplemented
// is a claim, not a contract.
//
// Each RunRole becomes one cohort of pods:
//
//   - pod count      = role.Width (the gang)
//   - pod template   = deep copy of role.Template, with only these overlaid:
//   - spec.schedulerName  = "jobtree"     (the plugin places and funds it)
//   - spec.nodeName       left UNSET       (never pin; the plugin binds)
//   - spec.restartPolicy  = Never          (Succeeded is the gang signal)
//   - labels: LabelRunName / LabelGroupIndex / LabelRunRole merged in
//     (see pkg/binder); researcher labels preserved
//   - resources.limits["nvidia.com/gpu"] == requests == role.GPUsPerPod, on
//     the GPU-target container (v1.GPUTargetContainerName, else index 0)
//
// Not yet honoured, and deliberately not claimed anywhere a user can read:
//
//   - stable rendezvous identity (headless Service, hostname/subdomain, and a
//     swap pod inheriting the replaced member's ordinal) — R9 phase 9A-1
//   - rendezvous env (MASTER_ADDR via that DNS name, MASTER_PORT, WORLD_SIZE,
//     NNODES, NODE_RANK), injected only when role.Width > 1; never RANK or
//     LOCAL_RANK, which are per-process and torchrun's job — R9 phase 9A-2
//   - the failure edge: JobSet's successPolicy{All}/failurePolicy become a
//     per-role FailurePolicy, gang co-termination, and lease closure with
//     reason WorkloadFailed, so a Failed pod is terminal rather than hanging
//     the Run forever and charging its budget — R9 phase 9A-3 (absorbs R8)
//
// v1 admits exactly one role (webhook-validated), so today this renders a
// single-role Run; multi-role RL gangs extend it additively.
func buildPod(manifest binder.PodManifest, run *v1.Run) *corev1.Pod {
	var spec corev1.PodSpec
	targetIdx := 0
	switch {
	case manifest.Labels[binder.LabelRunRole] == binder.RoleSpare:
		// A hot spare reserves its GPU but runs no work: it holds the slice until
		// a node-failure swap promotes it (the swap pod runs the real workload).
		// So it never runs the researcher's container — a long-lived holder that
		// simply occupies the GPU, terminated by the controller when reclaimed.
		spec = corev1.PodSpec{Containers: []corev1.Container{{
			Name:    v1.GPUTargetContainerName,
			Image:   defaultWorkloadImage,
			Command: []string{"sh", "-c", "echo jobtree-hot-spare; sleep 2147483647"},
		}}}
	case run != nil && len(run.Spec.Roles) > 0:
		role := &run.Spec.Roles[0]
		spec = *role.Template.Spec.DeepCopy()
		if idx := role.GPUTargetContainerIndex(); idx >= 0 {
			targetIdx = idx
		}
	default:
		spec = corev1.PodSpec{Containers: []corev1.Container{{
			Name:    v1.GPUTargetContainerName,
			Image:   defaultWorkloadImage,
			Command: []string{"sh", "-c", "echo jobtree-placeholder; true"},
		}}}
	}

	// Scheduling-owned overlay — never touch researcher fields.
	spec.SchedulerName = schedulerName
	spec.NodeName = "" // the plugin/scheduler places the pod; never pin here
	spec.RestartPolicy = corev1.RestartPolicyNever

	if manifest.GPUs > 0 && len(spec.Containers) > 0 {
		if targetIdx >= len(spec.Containers) {
			targetIdx = 0
		}
		q := resource.NewQuantity(int64(manifest.GPUs), resource.DecimalSI)
		c := &spec.Containers[targetIdx]
		if c.Resources.Requests == nil {
			c.Resources.Requests = corev1.ResourceList{}
		}
		if c.Resources.Limits == nil {
			c.Resources.Limits = corev1.ResourceList{}
		}
		c.Resources.Requests[GPUCapacityResource] = *q
		c.Resources.Limits[GPUCapacityResource] = *q
	}

	// Advisory placement toward pack's chosen node: a preference the plugin's
	// Filter/Score honor, NOT a pin.
	if manifest.NodeName != "" {
		preferNode(&spec, manifest.NodeName)
	}

	annotations := map[string]string{PodGPUAnnotation: strconv.Itoa(manifest.GPUs)}
	if run != nil && run.Spec.Resources.GPUType != "" {
		annotations[binder.AnnotationFlavor] = run.Spec.Resources.GPUType
	}
	// Carry the run's per-incarnation nonce (a prefix of its UID) so the plugin's
	// minted lease name is unique per incarnation — a delete+resubmit of a same-
	// named Run cannot then alias the prior incarnation's closed lease (R2).
	if run != nil && run.UID != "" {
		uid := string(run.UID)
		if len(uid) > 12 {
			uid = uid[:12]
		}
		annotations[binder.AnnotationRunNonce] = uid
	}
	for _, k := range []string{
		binder.AnnotationExpectedWidth, binder.AnnotationLeaseReason, binder.AnnotationCohort,
		binder.AnnotationSwapNode, binder.AnnotationPayerOwner, binder.AnnotationPayerBudget, binder.AnnotationPayerEnvelope,
	} {
		if v, ok := manifest.Annotations[k]; ok {
			annotations[k] = v
		}
	}
	// A swap pod must land on the specific reclaimed spare node — a REQUIRED
	// affinity, overriding the soft advisory above.
	if swapNode := manifest.Annotations[binder.AnnotationSwapNode]; swapNode != "" {
		requireNode(&spec, swapNode)
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       manifest.Namespace,
			Name:            manifest.Name,
			Labels:          manifest.Labels,
			Annotations:     annotations,
			OwnerReferences: runOwnerReferences(run),
		},
		Spec: spec,
	}
}

// runOwnerReferences ties an emitted workload pod to its owning Run: it makes the
// Run the provenance anchor for the pod (a hint the plugin can require, R5) and
// lets the apiserver garbage-collect the pods when the Run is deleted instead of
// the controller sweeping them by hand (R12). Requires the Run's UID, which the
// real API path always has; pure-engine Runs without a UID get no reference.
func runOwnerReferences(run *v1.Run) []metav1.OwnerReference {
	if run == nil || run.UID == "" {
		return nil
	}
	yes := true
	return []metav1.OwnerReference{{
		APIVersion:         v1.GroupVersion.String(),
		Kind:               "Run",
		Name:               run.Name,
		UID:                run.UID,
		Controller:         &yes,
		BlockOwnerDeletion: &yes,
	}}
}

// preferNode appends a soft (preferred) node-affinity toward node, an advisory
// hint from pkg/pack's placement that the scheduler honors when it can.
func preferNode(spec *corev1.PodSpec, node string) {
	term := corev1.PreferredSchedulingTerm{
		Weight: 100,
		Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key:      "kubernetes.io/hostname",
			Operator: corev1.NodeSelectorOpIn,
			Values:   []string{node},
		}}},
	}
	if spec.Affinity == nil {
		spec.Affinity = &corev1.Affinity{}
	}
	if spec.Affinity.NodeAffinity == nil {
		spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	na := spec.Affinity.NodeAffinity
	na.PreferredDuringSchedulingIgnoredDuringExecution = append(na.PreferredDuringSchedulingIgnoredDuringExecution, term)
}

// requireNode adds a REQUIRED node-affinity pinning the pod to node — used for a
// node-failure swap, which must land on the specific reclaimed spare node.
func requireNode(spec *corev1.PodSpec, node string) {
	sel := corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
		Key:      "kubernetes.io/hostname",
		Operator: corev1.NodeSelectorOpIn,
		Values:   []string{node},
	}}}
	if spec.Affinity == nil {
		spec.Affinity = &corev1.Affinity{}
	}
	if spec.Affinity.NodeAffinity == nil {
		spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	na := spec.Affinity.NodeAffinity
	if na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		na.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
	}
	na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
		na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms, sel)
}
