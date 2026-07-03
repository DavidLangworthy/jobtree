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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

const (
	// GPUCapacityResource is the node capacity key the engine reads GPU
	// counts from (the fake or real device plugin advertises it).
	GPUCapacityResource = "nvidia.com/gpu"
	// PodGPUAnnotation records how many GPUs of its node a workload pod
	// claims.
	PodGPUAnnotation = "rq.davidlangworthy.io/gpus"
	// pauseImage is the placeholder workload container.
	pauseImage = "registry.k8s.io/pause:3.10"
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

	mu sync.Mutex
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
	if applyErr := b.apply(ctx, snap); applyErr != nil {
		return applyErr
	}
	return fnErr
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
			if err := b.Client.Create(ctx, buildPod(manifest)); err != nil {
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

func buildPod(manifest binder.PodManifest) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   manifest.Namespace,
			Name:        manifest.Name,
			Labels:      manifest.Labels,
			Annotations: map[string]string{PodGPUAnnotation: strconv.Itoa(manifest.GPUs)},
		},
		Spec: corev1.PodSpec{
			NodeName:      manifest.NodeName,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:  "workload",
				Image: pauseImage,
			}},
		},
	}
}
