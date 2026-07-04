package kube

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// End-to-end scenarios through the real manager: API server, webhooks,
// watches, bridge, and engine together. The clock is frozen at baseTime so
// engine-generated names and times are deterministic and status writes
// converge instead of self-triggering.

func createH100Node(t *testing.T, name string, gpus int) {
	t.Helper()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"region":        "us-west",
				"cluster":       "cluster-a",
				"fabric.domain": "island-a",
				"gpu.flavor":    "H100-80GB",
			},
		},
	}
	if err := kubeClient.Create(suiteCtx, node); err != nil {
		t.Fatalf("create node %s: %v", name, err)
	}
	// Capacity lives in status; a Ready=True condition keeps the node
	// reconciler from treating the fixture as a failed node.
	node.Status = corev1.NodeStatus{
		Capacity: corev1.ResourceList{
			GPUCapacityResource: *resource.NewQuantity(int64(gpus), resource.DecimalSI),
		},
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
	}
	if err := kubeClient.Status().Update(suiteCtx, node); err != nil {
		t.Fatalf("update node %s status: %v", name, err)
	}
}

func createBudget(t *testing.T, name, owner string, concurrency int32) {
	t.Helper()
	budget := &v1.Budget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.BudgetSpec{
			Owner: owner,
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "west",
				Flavor:      "H100-80GB",
				Selector:    map[string]string{"region": "us-west", "cluster": "cluster-a", "fabric.domain": "island-a"},
				Concurrency: concurrency,
			}},
		},
	}
	if err := kubeClient.Create(suiteCtx, budget); err != nil {
		t.Fatalf("create budget %s: %v", name, err)
	}
}

func getRun(t *testing.T, name string) *v1.Run {
	t.Helper()
	var run v1.Run
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: name}, &run); err != nil {
		t.Fatalf("get run %s: %v", name, err)
	}
	return &run
}

func waitForRunPhase(t *testing.T, name, phase string) *v1.Run {
	t.Helper()
	var run v1.Run
	eventually(t, 30*time.Second, func() error {
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: name}, &run); err != nil {
			return err
		}
		if run.Status.Phase != phase {
			return fmt.Errorf("run %s phase %q (message %q), want %q", name, run.Status.Phase, run.Status.Message, phase)
		}
		return nil
	})
	return &run
}

func listRunLeases(t *testing.T, runName string) []v1.Lease {
	t.Helper()
	var list v1.LeaseList
	if err := kubeClient.List(suiteCtx, &list, client.MatchingLabels{binder.LabelRunName: runName}); err != nil {
		t.Fatalf("list leases: %v", err)
	}
	return list.Items
}

// waitForRunLeases polls until the run has exactly want leases: the cached
// client's lease list can lag the run-status event that ended the previous
// wait, so a bare list right after waitForRunPhase is a flake.
func waitForRunLeases(t *testing.T, runName string, want int) []v1.Lease {
	t.Helper()
	var leases []v1.Lease
	eventually(t, 15*time.Second, func() error {
		leases = listRunLeases(t, runName)
		if len(leases) != want {
			return fmt.Errorf("%d leases, want %d", len(leases), want)
		}
		return nil
	})
	return leases
}

func listRunPods(t *testing.T, runName string) []corev1.Pod {
	t.Helper()
	var list corev1.PodList
	if err := kubeClient.List(suiteCtx, &list, client.MatchingLabels{binder.LabelRunName: runName}); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	return list.Items
}

func waitForRunPods(t *testing.T, runName string, want int) []corev1.Pod {
	t.Helper()
	var pods []corev1.Pod
	eventually(t, 15*time.Second, func() error {
		pods = listRunPods(t, runName)
		if len(pods) != want {
			return fmt.Errorf("%d pods, want %d", len(pods), want)
		}
		return nil
	})
	return pods
}

// seedPluginLeases stands in for the out-of-process scheduler plugin: it
// computes the leases admission.Plan would mint for the run against the live
// world the test built (budgets, nodes, runs, existing leases) and CREATEs them
// through the API. The running manager's Lease watch then re-triggers the run,
// whose adoption path (run_controller.go: "adopted N open leases") flips it
// Running — the very transition the plugin's PreBind mint drives on a real
// cluster (proven end-to-end by hack/e2e/plugin-smoke.sh). It is the envtest,
// client-side twin of the pure-engine seedRunning in controllers/cutover_test.go,
// writing real Lease CRs instead of appending to an in-memory ClusterState.
func seedPluginLeases(t *testing.T, runName string) []v1.Lease {
	t.Helper()
	now := clock.Now()

	var budgetList v1.BudgetList
	if err := kubeClient.List(suiteCtx, &budgetList); err != nil {
		t.Fatalf("seedPluginLeases list budgets: %v", err)
	}
	var runList v1.RunList
	if err := kubeClient.List(suiteCtx, &runList); err != nil {
		t.Fatalf("seedPluginLeases list runs: %v", err)
	}
	var leaseList v1.LeaseList
	if err := kubeClient.List(suiteCtx, &leaseList); err != nil {
		t.Fatalf("seedPluginLeases list leases: %v", err)
	}
	var nodeList corev1.NodeList
	if err := kubeClient.List(suiteCtx, &nodeList); err != nil {
		t.Fatalf("seedPluginLeases list nodes: %v", err)
	}

	runs := make(map[string]*v1.Run, len(runList.Items))
	for i := range runList.Items {
		r := &runList.Items[i]
		runs[keys.NamespacedKey(r.Namespace, r.Name)] = r
	}
	run := runs[keys.NamespacedKey("default", runName)]
	if run == nil {
		t.Fatalf("seedPluginLeases: run %s not found", runName)
	}

	// Mirror bridge.load's node projection: only schedulable, Ready nodes
	// contribute capacity, and GPU counts come from status capacity.
	var nodes []topology.SourceNode
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		if node.Spec.Unschedulable {
			continue
		}
		ready := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady {
				ready = cond.Status == corev1.ConditionTrue
			}
		}
		if !ready {
			continue
		}
		gpus := 0
		if qty, ok := node.Status.Capacity[GPUCapacityResource]; ok {
			gpus = int(qty.Value())
		}
		nodes = append(nodes, topology.SourceNode{Name: node.Name, Labels: node.Labels, GPUs: gpus})
	}

	res, err := admission.Plan(admission.Input{
		Run:     run,
		Budgets: budgetList.Items,
		Runs:    runs,
		Leases:  leaseList.Items,
		Nodes:   nodes,
		Now:     now,
	})
	if err != nil {
		t.Fatalf("seedPluginLeases plan %s: %v", runName, err)
	}
	// Freshly planned leases carry no status, so a bare Create is faithful to
	// the plugin's PreBind mint (an open lease).
	for i := range res.Leases {
		lease := res.Leases[i].DeepCopy()
		lease.ResourceVersion = ""
		if err := kubeClient.Create(suiteCtx, lease); err != nil {
			t.Fatalf("seedPluginLeases create lease %s: %v", lease.Name, err)
		}
	}
	return res.Leases
}

// assertIntentPod checks one UNSCHEDULED workload pod against the new
// manager→plugin contract: routed to the jobtree scheduler, unbound, restarted
// never, carrying a real nvidia.com/gpu request and the gang metadata the
// plugin reads. The manager only emits these; real binding + lease minting is
// the plugin's job, proven live by hack/e2e/plugin-smoke.sh.
func assertIntentPod(t *testing.T, pod corev1.Pod, runName string, gpusPerPod int) {
	t.Helper()
	if pod.Spec.SchedulerName != schedulerName {
		t.Errorf("pod %s schedulerName = %q, want %q", pod.Name, pod.Spec.SchedulerName, schedulerName)
	}
	if pod.Spec.NodeName != "" {
		t.Errorf("pod %s is bound to %q; an intent pod must be unscheduled", pod.Name, pod.Spec.NodeName)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("pod %s restartPolicy = %q, want Never", pod.Name, pod.Spec.RestartPolicy)
	}
	var got resource.Quantity
	found := false
	for i := range pod.Spec.Containers {
		if q, ok := pod.Spec.Containers[i].Resources.Requests[GPUCapacityResource]; ok {
			got, found = q, true
			break
		}
	}
	if want := int64(gpusPerPod); !found || got.Value() != want {
		t.Errorf("pod %s nvidia.com/gpu request = %d (found=%v), want %d", pod.Name, got.Value(), found, want)
	}
	if pod.Labels[binder.LabelRunName] != runName || pod.Labels[binder.LabelRunRole] != binder.RoleActive {
		t.Errorf("pod %s gang labels = run %q/role %q, want %q/Active", pod.Name, pod.Labels[binder.LabelRunName], pod.Labels[binder.LabelRunRole], runName)
	}
	if pod.Annotations[binder.AnnotationFlavor] != "H100-80GB" {
		t.Errorf("pod %s flavor annotation = %q, want H100-80GB", pod.Name, pod.Annotations[binder.AnnotationFlavor])
	}
	if pod.Annotations[PodGPUAnnotation] != strconv.Itoa(gpusPerPod) {
		t.Errorf("pod %s gpus annotation = %q, want %d", pod.Name, pod.Annotations[PodGPUAnnotation], gpusPerPod)
	}
}

// TestManagerBindsRunEndToEnd: a valid Run is defaulted by the mutating
// webhook and admitted by the engine, which now emits UNSCHEDULED intent pods
// for the jobtree scheduler plugin instead of binding them itself. Standing in
// for the plugin (seedPluginLeases) drives the adoption flip to Running; the
// budget reconciler then folds the plugin's lease back into envelope headroom.
// Real end-to-end binding + minting is proven live by hack/e2e/plugin-smoke.sh.
func TestManagerBindsRunEndToEnd(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.Spec.Locality == nil || run.Spec.Locality.AllowCrossGroupSpread == nil || !*run.Spec.Locality.AllowCrossGroupSpread {
		t.Errorf("mutating webhook should default allowCrossGroupSpread=true on a persisted create, got %+v", run.Spec.Locality)
	}

	// The manager emits its full width of UNSCHEDULED intent pods (1 GPU each)
	// and parks Pending until the plugin schedules them; assert that contract
	// before standing in for the plugin.
	intents := waitForRunPods(t, "train", 4)
	for _, pod := range intents {
		assertIntentPod(t, pod, "train", 1)
	}

	seedPluginLeases(t, "train")

	bound := waitForRunPhase(t, "train", "Running")
	if bound.Status.Width == nil || bound.Status.Width.Allocated != 4 {
		t.Errorf("expected width.allocated=4, got %+v", bound.Status.Width)
	}
	if bound.Status.Funding == nil || bound.Status.Funding.OwnedGPUs != 4 {
		t.Errorf("expected funding.ownedGPUs=4, got %+v", bound.Status.Funding)
	}

	leases := waitForRunLeases(t, "train", 1)
	lease := leases[0]
	wantLeaseName := fmt.Sprintf("train-g00-team-west-%d-0", baseTime.UnixNano())
	if lease.Name != wantLeaseName {
		t.Errorf("lease name = %q, want %q", lease.Name, wantLeaseName)
	}
	if lease.Spec.Owner != "org:team" || lease.Spec.PaidByBudget != "team" || lease.Spec.PaidByEnvelope != "west" {
		t.Errorf("lease funding = owner %q paidByBudget %q paidByEnvelope %q", lease.Spec.Owner, lease.Spec.PaidByBudget, lease.Spec.PaidByEnvelope)
	}
	if lease.Spec.RunRef != (v1.RunReference{Name: "train", Namespace: "default"}) {
		t.Errorf("lease runRef = %+v", lease.Spec.RunRef)
	}
	wantSlots := []string{"node-a#0", "node-a#1", "node-a#2", "node-a#3"}
	if !slices.Equal(lease.Spec.Slice.Nodes, wantSlots) || lease.Spec.Slice.Role != binder.RoleActive {
		t.Errorf("lease slice = %+v, want nodes %v role %s", lease.Spec.Slice, wantSlots, binder.RoleActive)
	}
	if lease.Status.Closed {
		t.Error("fresh lease must not be closed")
	}

	eventually(t, 15*time.Second, func() error {
		var budget v1.Budget
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "team"}, &budget); err != nil {
			return err
		}
		if len(budget.Status.Headroom) != 1 || budget.Status.Headroom[0].Concurrency != 4 {
			return fmt.Errorf("headroom = %+v, want [west: 4]", budget.Status.Headroom)
		}
		return nil
	})
}

// TestRunCompletesWhenPodsSucceed (B0): once a bound run's workload pods reach
// Succeeded, the pod watch re-triggers the run, which finalizes to Completed,
// closes its leases, and frees the budget headroom.
func TestRunCompletesWhenPodsSucceed(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "finish", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	// Let the manager emit its intent pods, then stand in for the plugin so the
	// run adopts the leases and reaches Running (it no longer binds/mints itself).
	waitForRunPods(t, "finish", 4)
	seedPluginLeases(t, "finish")
	waitForRunPhase(t, "finish", "Running")
	pods := waitForRunPods(t, "finish", 4)

	// No kubelet in envtest, so drive the workload pods to Succeeded by hand;
	// the Succeeded-only pod watch should re-trigger the run once the whole
	// active gang has finished.
	for i := range pods {
		pods[i].Status.Phase = corev1.PodSucceeded // antifake:allow-terminal-phase — documented interim exception, see hack/antifake/terminal-phase-allowlist.txt; remove when Track B (JOBSET) lands a real container
		if err := kubeClient.Status().Update(suiteCtx, &pods[i]); err != nil {
			t.Fatalf("mark pod succeeded: %v", err)
		}
	}

	waitForRunPhase(t, "finish", "Completed")

	eventually(t, 15*time.Second, func() error {
		for _, l := range listRunLeases(t, "finish") {
			if !l.Status.Closed || l.Status.ClosureReason != "Completed" {
				return fmt.Errorf("lease %s closed=%v reason=%q, want closed/Completed", l.Name, l.Status.Closed, l.Status.ClosureReason)
			}
		}
		var budget v1.Budget
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "team"}, &budget); err != nil {
			return err
		}
		if len(budget.Status.Headroom) != 1 || budget.Status.Headroom[0].Concurrency != 8 {
			return fmt.Errorf("headroom = %+v, want full 8 after completion", budget.Status.Headroom)
		}
		return nil
	})
}

// TestFollowGatesUntilUpstreamCompletes (B): a run that follows another stays
// Waiting until its upstream completes, at which point the Run→Run watch
// re-triggers it and it admits.
func TestFollowGatesUntilUpstreamCompletes(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 8)
	createBudget(t, "team", "org:team", 16)

	prep := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "prep", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
	}
	if err := kubeClient.Create(suiteCtx, prep); err != nil {
		t.Fatalf("create prep: %v", err)
	}
	waitForRunPods(t, "prep", 4)
	seedPluginLeases(t, "prep")
	waitForRunPhase(t, "prep", "Running")

	train := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
			Follow:    &v1.RunFollow{After: []string{"prep"}},
		},
	}
	if err := kubeClient.Create(suiteCtx, train); err != nil {
		t.Fatalf("create train: %v", err)
	}
	waiting := waitForRunPhase(t, "train", "Waiting")
	if !strings.Contains(waiting.Status.Message, "prep") {
		t.Errorf("waiting message should name prep, got %q", waiting.Status.Message)
	}

	// Complete the upstream by driving its pods to Succeeded.
	pods := waitForRunPods(t, "prep", 4)
	for i := range pods {
		pods[i].Status.Phase = corev1.PodSucceeded // antifake:allow-terminal-phase — documented interim exception, see hack/antifake/terminal-phase-allowlist.txt; remove when Track B (JOBSET) lands a real container
		if err := kubeClient.Status().Update(suiteCtx, &pods[i]); err != nil {
			t.Fatalf("mark prep pod succeeded: %v", err)
		}
	}
	waitForRunPhase(t, "prep", "Completed")

	// The Run→Run watch should re-trigger train, which now clears the follow
	// gate and emits its intent pods; standing in for the plugin flips it Running.
	waitForRunPods(t, "train", 4)
	seedPluginLeases(t, "train")
	waitForRunPhase(t, "train", "Running")
}

// TestETAMirroredFromPodAnnotation (A): a workload pod's rq.davidlangworthy.io/
// eta annotation is mirrored into Run.status.eta (source "job"), observability
// only — the run stays Running.
func TestETAMirroredFromPodAnnotation(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "eta-run", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitForRunPods(t, "eta-run", 4)
	seedPluginLeases(t, "eta-run")
	waitForRunPhase(t, "eta-run", "Running")
	pods := waitForRunPods(t, "eta-run", 4)

	want := baseTime.Add(3 * time.Hour).UTC().Format(time.RFC3339)
	pod := pods[0]
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[binder.EtaAnnotation] = want
	if err := kubeClient.Update(suiteCtx, &pod); err != nil {
		t.Fatalf("annotate pod: %v", err)
	}

	eventually(t, 20*time.Second, func() error {
		var got v1.Run
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "eta-run"}, &got); err != nil {
			return err
		}
		if got.Status.Phase != "Running" {
			return fmt.Errorf("run phase %q, want still Running", got.Status.Phase)
		}
		if got.Status.ETA == nil {
			return fmt.Errorf("ETA not mirrored yet")
		}
		if g := got.Status.ETA.EstimatedCompletion.Time.UTC().Format(time.RFC3339); g != want {
			return fmt.Errorf("ETA = %q, want %q", g, want)
		}
		if got.Status.ETA.Source != "job" {
			return fmt.Errorf("ETA source = %q, want job", got.Status.ETA.Source)
		}
		return nil
	})
}

// TestReservationActivatesWhenCapacityArrives: a run the cluster cannot
// place parks as Pending behind a Reservation; once capacity exists and the
// clock passes EarliestStart, the reservation reconciler activates it and
// the run binds. This is the R21 requeue-driven activation path.
func TestReservationActivatesWhenCapacityArrives(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 16)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "train8", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	var resName string
	eventually(t, 30*time.Second, func() error {
		var parked v1.Run
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "train8"}, &parked); err != nil {
			return err
		}
		if parked.Status.Phase != "Pending" || parked.Status.PendingReservation == nil {
			return fmt.Errorf("run status = %+v, want Pending with a reservation", parked.Status)
		}
		resName = *parked.Status.PendingReservation
		return nil
	})

	var res v1.Reservation
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: resName}, &res); err != nil {
		t.Fatalf("get reservation: %v", err)
	}
	// The activation lead is data-driven (forecast.go): a base 15-minute
	// lead plus 30s per deficit GPU, so a 4-GPU deficit adds 2 minutes
	// instead of promising the same flat window regardless of size.
	if got, want := res.Spec.EarliestStart.Time, baseTime.Add(17*time.Minute); !got.Equal(want) {
		t.Errorf("earliestStart = %s, want %s (deficit-scaled activation lead)", got, want)
	}
	if res.Spec.RunRef != (v1.RunReference{Name: "train8", Namespace: "default"}) {
		t.Errorf("reservation runRef = %+v", res.Spec.RunRef)
	}
	if res.Status.Forecast == nil || res.Status.Forecast.DeficitGPUs != 4 {
		t.Errorf("forecast = %+v, want deficit of 4 GPUs", res.Status.Forecast)
	}

	// Capacity arrives, the activation moment passes, and a metadata-only
	// touch stands in for the wall-clock requeue the frozen test clock
	// cannot deliver. Two hazards shape the loop: a Run reconcile while
	// still Pending may replace the reservation under a new clock-derived
	// name (follow the run's pendingReservation pointer, don't pin one);
	// and a poke that lands while the bridge is mid-apply conflicts its
	// reservation status write, which can abort the apply after the leases
	// materialized and force a reschedule (the R28 convergence gap). So
	// pace the pokes and, when a reschedule pushed EarliestStart out, march
	// the clock past it.
	createH100Node(t, "node-b", 4)
	clock.Set(baseTime.Add(18 * time.Minute))
	attempt := 0
	eventually(t, 30*time.Second, func() error {
		var current v1.Run
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "train8"}, &current); err != nil {
			return err
		}
		if current.Status.Phase == "Running" {
			return nil
		}
		// Funded activation emits the run's intent pods and clears the pending
		// reservation; once those pods are out, the scheduler plugin mints their
		// leases (stood in for by seedPluginLeases) and the run reaches Running.
		if len(listRunPods(t, "train8")) > 0 {
			seedPluginLeases(t, "train8")
			return nil
		}
		if current.Status.PendingReservation == nil {
			return fmt.Errorf("run is %s with no pending reservation", current.Status.Phase)
		}
		resName = *current.Status.PendingReservation
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: resName}, &res); err != nil {
			return err
		}
		if earliest := res.Spec.EarliestStart.Time; clock.Now().Before(earliest) {
			clock.Set(earliest.Add(time.Minute))
		}
		attempt++
		poked := res.DeepCopy()
		if poked.Annotations == nil {
			poked.Annotations = map[string]string{}
		}
		poked.Annotations["test.rq.davidlangworthy.io/poke"] = fmt.Sprintf("%d", attempt)
		if err := kubeClient.Update(suiteCtx, poked); err != nil {
			return err
		}
		// Let the reconcile finish before the next poke: overlapping pokes
		// are what manufacture the mid-apply conflict.
		time.Sleep(time.Second)
		return fmt.Errorf("poke %d on %s; run %s (%q)", attempt, resName, current.Status.Phase, current.Status.Message)
	})

	activated := waitForRunPhase(t, "train8", "Running")
	if activated.Status.PendingReservation != nil {
		t.Errorf("pendingReservation should clear after activation, got %v", *activated.Status.PendingReservation)
	}
	// Which reservation object survives — and whether its release reason is
	// Activated or Superseded — depends on which event healed a mid-apply
	// conflict first (see the poke-loop comment). The path-independent
	// invariant: once the run is Running, no reservation for it may still
	// be pending. Exact release semantics are covered by the engine unit
	// tests.
	eventually(t, 20*time.Second, func() error {
		var list v1.ReservationList
		if err := kubeClient.List(suiteCtx, &list); err != nil {
			return err
		}
		for i := range list.Items {
			leftover := &list.Items[i]
			if leftover.Spec.RunRef.Name != "train8" {
				continue
			}
			if leftover.Status.State != "Released" {
				return fmt.Errorf("reservation %s state %s/%s, want Released", leftover.Name, leftover.Status.State, leftover.Status.Reason)
			}
		}
		return nil
	})

	// Funded reservation activation now EMITS unscheduled intent pods (like
	// initial admission) instead of minting — the scheduler plugin mints, stood
	// in for by seedPluginLeases above (2 chunk-leases spanning both nodes;
	// proven live end-to-end by hack/e2e/fullstack-smoke.sh). Assert the seeded
	// leases span both nodes and the run's 8 intent pods (1 GPU each for the
	// 8-GPU run) carry no nodeName.
	leases := waitForRunLeases(t, "train8", 2)
	nodesUsed := map[string]bool{}
	for _, lease := range leases {
		for _, slot := range lease.Spec.Slice.Nodes {
			nodesUsed[strings.SplitN(slot, "#", 2)[0]] = true
		}
	}
	if !nodesUsed["node-a"] || !nodesUsed["node-b"] {
		t.Errorf("activated leases cover %v, want both node-a and node-b", nodesUsed)
	}
	pods := waitForRunPods(t, "train8", 8)
	for _, pod := range pods {
		if pod.Spec.NodeName != "" {
			t.Errorf("pod %s bound to %q; workload pods must be unscheduled for the plugin", pod.Name, pod.Spec.NodeName)
		}
	}
}

// TestNodeFailureSwapsToSpare: the node watch drives HandleNodeFailure (R21):
// when the active node dies, the spare lease is promoted into a swap lease
// and the workload pods move to the spare's nodes.
// seedSwapLease mints, via the client, the Swap lease the scheduler plugin would
// create for a run's node-failure swap pod — from the provenance the controller
// stamped on it (the spare's payer, on the swap node). The envtest stand-in for
// the plugin's provenance-preserving PreBind now that HandleNodeFailure emits a
// swap pod instead of minting.
func seedSwapLease(t *testing.T, runName string) {
	t.Helper()
	var podList corev1.PodList
	if err := kubeClient.List(suiteCtx, &podList); err != nil {
		t.Fatalf("seedSwapLease list pods: %v", err)
	}
	var swapPod *corev1.Pod
	for i := range podList.Items {
		p := &podList.Items[i]
		if p.Labels[binder.LabelRunName] == runName && p.Annotations[binder.AnnotationLeaseReason] == "Swap" {
			swapPod = p
		}
	}
	if swapPod == nil {
		t.Fatalf("seedSwapLease: no swap pod for run %s", runName)
	}
	node := swapPod.Annotations[binder.AnnotationSwapNode]
	gpus, _ := strconv.Atoi(swapPod.Annotations[binder.AnnotationGPUs])
	slots := make([]string, gpus)
	for i := range slots {
		slots[i] = fmt.Sprintf("%s#%d", node, i)
	}
	lease := &v1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      swapPod.Name + "-lease",
			Labels:    map[string]string{binder.LabelRunName: runName, binder.LabelRunRole: binder.RoleActive},
		},
		Spec: v1.LeaseSpec{
			Owner:          swapPod.Annotations[binder.AnnotationPayerOwner],
			RunRef:         v1.RunReference{Name: runName, Namespace: "default"},
			Slice:          v1.LeaseSlice{Nodes: slots, Role: binder.RoleActive},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(clock.Now())},
			PaidByBudget:   swapPod.Annotations[binder.AnnotationPayerBudget],
			PaidByEnvelope: swapPod.Annotations[binder.AnnotationPayerEnvelope],
			Reason:         "Swap",
		},
	}
	if err := kubeClient.Create(suiteCtx, lease); err != nil {
		t.Fatalf("seedSwapLease create: %v", err)
	}
}

func TestNodeFailureSwapsToSpare(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createH100Node(t, "node-b", 4)
	createBudget(t, "team", "org:team", 8) // 4 active + 2 spare = 6
	groupGPUs := int32(4)
	spares := int32(2)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "resilient", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
			Locality:  &v1.RunLocality{GroupGPUs: &groupGPUs},
			Spares:    &spares,
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	// The manager emits the active gang's width of unscheduled intent pods AND
	// the declared spares as held RoleSpare intent pods (CASCADE-3b) — real
	// standby capacity, not an inert declaration. Standing in for the plugin
	// mints the active + spare leases and adopts.
	intents := waitForRunPods(t, "resilient", 6) // 4 active + 2 spare
	active, spares2 := 0, 0
	for _, pod := range intents {
		switch pod.Labels[binder.LabelRunRole] {
		case binder.RoleActive:
			active++
			assertIntentPod(t, pod, "resilient", 1)
		case binder.RoleSpare:
			spares2++
			// A held spare is a real, unscheduled pod reserving one GPU with a
			// long-lived holder container (it runs no work until a swap promotes it).
			if pod.Spec.NodeName != "" {
				t.Errorf("spare pod %s is bound to %q; a held spare intent pod must be unscheduled", pod.Name, pod.Spec.NodeName)
			}
			if pod.Spec.SchedulerName != schedulerName {
				t.Errorf("spare pod %s schedulerName = %q, want %q", pod.Name, pod.Spec.SchedulerName, schedulerName)
			}
			if pod.Annotations[PodGPUAnnotation] != "1" {
				t.Errorf("spare pod %s gpus annotation = %q, want 1 (it reserves the slice)", pod.Name, pod.Annotations[PodGPUAnnotation])
			}
		default:
			t.Errorf("unexpected pod role %q on %s", pod.Labels[binder.LabelRunRole], pod.Name)
		}
	}
	if active != 4 || spares2 != 2 {
		t.Fatalf("emitted %d active + %d spare intent pods, want 4 + 2", active, spares2)
	}
	seedPluginLeases(t, "resilient")
	waitForRunPhase(t, "resilient", "Running")

	leases := waitForRunLeases(t, "resilient", 2)
	var spare *v1.Lease
	for i := range leases {
		if leases[i].Spec.Slice.Role == binder.RoleSpare {
			spare = &leases[i]
		}
	}
	if spare == nil {
		t.Fatalf("no spare lease among %+v", leases)
	}
	if !slices.Equal(spare.Spec.Slice.Nodes, []string{"node-b#0", "node-b#1"}) {
		t.Fatalf("spare slots = %v, want node-b#0,node-b#1", spare.Spec.Slice.Nodes)
	}

	// Fail the active node.
	var node corev1.Node
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Name: "node-a"}, &node); err != nil {
		t.Fatalf("get node-a: %v", err)
	}
	node.Spec.Unschedulable = true
	if err := kubeClient.Update(suiteCtx, &node); err != nil {
		t.Fatalf("cordon node-a: %v", err)
	}

	eventually(t, 30*time.Second, func() error {
		var swapped v1.Run
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "resilient"}, &swapped); err != nil {
			return err
		}
		if want := "group 0 swapping to spare after node node-a failure"; swapped.Status.Message != want {
			return fmt.Errorf("run message %q, want %q", swapped.Status.Message, want)
		}
		return nil
	})

	// The manager closed the failed + spare leases and emitted a SWAP pod
	// (stamped with the spare's provenance, hard-targeted at node-b); the plugin
	// mints the Swap lease from that provenance — stood in for by seedSwapLease.
	seedSwapLease(t, "resilient")

	leases = waitForRunLeases(t, "resilient", 3) // closed active, closed spare, open swap
	var swap *v1.Lease
	closedReasons := map[string]string{}
	for i := range leases {
		lease := &leases[i]
		if lease.Spec.Reason == "Swap" {
			swap = lease
			continue
		}
		if !lease.Status.Closed {
			t.Errorf("lease %s should be closed after the swap", lease.Name)
		}
		closedReasons[lease.Spec.Slice.Role] = lease.Status.ClosureReason
	}
	if closedReasons[binder.RoleActive] != "NodeFailure" || closedReasons[binder.RoleSpare] != "Swap" {
		t.Errorf("closure reasons = %+v, want Active:NodeFailure Spare:Swap", closedReasons)
	}
	if swap == nil {
		t.Fatalf("no swap lease found among %d leases", len(leases))
	}
	if swap.Status.Closed {
		t.Error("swap lease must be open")
	}
	if !slices.Equal(swap.Spec.Slice.Nodes, []string{"node-b#0", "node-b#1"}) || swap.Spec.Slice.Role != binder.RoleActive {
		t.Errorf("swap slice = %+v, want Active on node-b#0,node-b#1", swap.Spec.Slice)
	}
	if swap.Spec.PaidByBudget != "team" || swap.Spec.PaidByEnvelope != "west" {
		t.Errorf("swap funding = %q/%q, want team/west", swap.Spec.PaidByBudget, swap.Spec.PaidByEnvelope)
	}

	// The swap's placement now lives in the open swap lease (node-b#0,#1),
	// asserted above — the controller no longer binds the workload to a node.
	// Real pod movement onto the spare is the plugin's job on a live cluster
	// (proven by hack/e2e/plugin-smoke.sh); here the immutable lease ledger is
	// the authoritative record of the swap.

	// Regression: the cordoned node's 4 GPUs must not count as capacity
	// for later admissions — its evacuation freed the leases, not the
	// hardware. A new 4-GPU run can only see node-b's 2 remaining GPUs,
	// so it must park rather than bind onto the dead node.
	opportunist := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "opportunist", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, opportunist); err != nil {
		t.Fatalf("create opportunist run: %v", err)
	}
	waitForRunPhase(t, "opportunist", "Pending")
	for _, pod := range listRunPods(t, "opportunist") {
		if pod.Spec.NodeName == "node-a" {
			t.Errorf("pod %s bound to the failed node", pod.Name)
		}
	}
}

// TestSpareNodeFailureIsAbsorbed: losing a node that hosts only spare
// capacity must not disturb the active lease — the reconciler swallows the
// engine's "no active lease found".
func TestSpareNodeFailureIsAbsorbed(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createH100Node(t, "node-b", 4)
	createBudget(t, "team", "org:team", 8)
	groupGPUs := int32(4)
	spares := int32(2)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "steady", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
			Locality:  &v1.RunLocality{GroupGPUs: &groupGPUs},
			Spares:    &spares,
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitForRunPods(t, "steady", 6) // 4 active + 2 held spare (CASCADE-3b)
	seedPluginLeases(t, "steady")
	waitForRunPhase(t, "steady", "Running")

	// Fail the spare node (node-b holds only the spare lease).
	var node corev1.Node
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Name: "node-b"}, &node); err != nil {
		t.Fatalf("get node-b: %v", err)
	}
	node.Spec.Unschedulable = true
	if err := kubeClient.Update(suiteCtx, &node); err != nil {
		t.Fatalf("cordon node-b: %v", err)
	}

	// Nothing should change. Watch the state over a window rather than a
	// single post-sleep check so a wrong closure is caught the moment it
	// lands, and the window covers the watch-delivery latency.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		steady := getRun(t, "steady")
		if steady.Status.Phase != "Running" {
			t.Fatalf("run phase = %s (message %q), want Running untouched", steady.Status.Phase, steady.Status.Message)
		}
		for _, lease := range listRunLeases(t, "steady") {
			if lease.Status.Closed {
				t.Fatalf("lease %s closed after spare-node failure; expected no reaction", lease.Name)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRunDeletionReleasesItsWorld: deleting a Run has no engine concept, so
// the kube layer must release what it left behind — otherwise the leases
// charge the budget and occupy nodes forever and the pods keep running.
func TestRunDeletionReleasesItsWorld(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "ephemeral", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitForRunPods(t, "ephemeral", 4)
	seedPluginLeases(t, "ephemeral")
	waitForRunPhase(t, "ephemeral", "Running")
	waitForRunLeases(t, "ephemeral", 1)
	waitForRunPods(t, "ephemeral", 4)

	if err := kubeClient.Delete(suiteCtx, run); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	eventually(t, 15*time.Second, func() error {
		leases := listRunLeases(t, "ephemeral")
		if len(leases) != 1 {
			return fmt.Errorf("%d leases, want the historical record kept", len(leases))
		}
		if !leases[0].Status.Closed || leases[0].Status.ClosureReason != "RunDeleted" {
			return fmt.Errorf("lease status = %+v, want Closed/RunDeleted", leases[0].Status)
		}
		for _, pod := range listRunPods(t, "ephemeral") {
			if pod.DeletionTimestamp == nil {
				return fmt.Errorf("pod %s still live", pod.Name)
			}
		}
		return nil
	})
}

// TestRunWithoutAnyMatchingDomainParksCleanly: with zero nodes of the
// flavor, reservation planning has no domain scope; the engine must park
// the run rather than emit a reservation its own webhook rejects (which
// wedged the bridge apply before the guard existed).
func TestRunWithoutAnyMatchingDomainParksCleanly(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "stranded", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	parked := waitForRunPhase(t, "stranded", "Pending")
	if !strings.Contains(parked.Status.Message, "no capacity in any matching domain") {
		t.Errorf("message = %q, want the domainless-park explanation", parked.Status.Message)
	}
	var reservations v1.ReservationList
	if err := kubeClient.List(suiteCtx, &reservations); err != nil {
		t.Fatalf("list reservations: %v", err)
	}
	if len(reservations.Items) != 0 {
		t.Errorf("expected no reservation objects, got %d", len(reservations.Items))
	}
}

// The requeue-at-EarliestStart contract is exercised by direct invocation:
// with a frozen clock the wall-clock requeue can never fire inside a test,
// so assert the returned RequeueAfter instead.
func TestReservationReconcilerRequeuesAtEarliestStart(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	res := &v1.Reservation{
		ObjectMeta: metav1.ObjectMeta{Name: "future", Namespace: "default"},
		Spec: v1.ReservationSpec{
			RunRef:         v1.RunReference{Name: "ghost", Namespace: "default"},
			IntendedSlice:  v1.IntendedSlice{Domain: map[string]string{"region": "us-west"}},
			PayingEnvelope: "west",
			EarliestStart:  metav1.NewTime(baseTime.Add(time.Hour)),
		},
	}
	if err := kubeClient.Create(suiteCtx, res); err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	rec := &ReservationReconciler{Bridge: suiteBridge}
	result, err := rec.Reconcile(suiteCtx, reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "future"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter != time.Hour {
		t.Errorf("RequeueAfter = %s, want 1h (EarliestStart - now)", result.RequeueAfter)
	}
}

// A run parked as Pending without a reservation has no driving watch event
// (new budgets or flavor nodes announce nothing to it), so the reconciler
// must poll it back.
func TestRunReconcilerRequeuesParkedRun(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "parked", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitForRunPhase(t, "parked", "Pending")

	rec := &RunReconciler{Bridge: suiteBridge}
	result, err := rec.Reconcile(suiteCtx, reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "parked"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter != pendingRunResync {
		t.Errorf("RequeueAfter = %s, want %s for a parked run", result.RequeueAfter, pendingRunResync)
	}
}
