//go:build e2e

// Package e2e is the kind-cluster verification backbone for jobtree (Track
// F — TESTINFRA, docs/project/make-it-real-plan.md). It is built under the
// `e2e` tag so `go build ./...` / `go test ./...` never touch it — it needs
// a real cluster (`make e2e`, or `make kind-up` followed by
// `go test ./test/e2e/... -tags=e2e`), never envtest.
//
// The one discipline every test in this package must hold to: assert on
// state a real kubelet/apiserver/webhook/controller produced, never write a
// terminal Pod phase (or any other "the system was supposed to derive this"
// value) by hand. hack/antifake's anti-fake lint enforces the same rule for
// *_test.go everywhere else; this package is where the *positive* proof
// lives instead of a unit-level absence-of-fakery check.
package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers/kube"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// e2eGPUFlavor/labels are a fixture GPU flavor this package stamps onto a
// real kind node so the real engine has something to admit a Run against.
// This is environment setup, not output-faking: it is the e2e-harness
// analogue of controllers/kube/scenario_test.go's createH100Node helper (and
// of what TESTINFRA-3's fake-device-plugin DaemonSet will eventually
// automate for real GPU *requests* once Track B/JOBSET lands a workload that
// makes one) — it never touches a Pod's or Run's *Status*.
const (
	e2eGPUFlavor  = "E2E-GPU"
	e2eRegion     = "e2e-region"
	e2eCluster    = "e2e-cluster"
	e2eFabricIsle = "e2e-island"
)

// e2eNodeSelector is the Budget envelope selector matching labelNodeAsGPU's
// labels.
func e2eNodeSelector() map[string]string {
	return map[string]string{"region": e2eRegion, "cluster": e2eCluster, "fabric.domain": e2eFabricIsle}
}

// workNamespace is where e2e tests create Run/Budget/Reservation/Lease
// objects — kind's built-in "default" namespace, so tests need not create
// (and clean up) one of their own.
const workNamespace = "default"

// controllerDeploymentName matches deploy/helm/gpu-fleet/templates/
// _helpers.tpl's `gpu-fleet.controllerName`, which is `<chart
// name>-controller` — independent of the Helm *release* name, so this is
// stable regardless of what hack/e2e/run-e2e.sh names the release.
const controllerDeploymentName = "gpu-fleet-controller"

// managerNamespace returns the namespace the manager is deployed into.
// hack/e2e/run-e2e.sh sets JOBTREE_E2E_NAMESPACE; default matches
// hack/e2e/versions.env's E2E_NAMESPACE.
func managerNamespace() string {
	if ns := os.Getenv("JOBTREE_E2E_NAMESPACE"); ns != "" {
		return ns
	}
	return "jobtree-system"
}

// newClient builds a controller-runtime client against whatever cluster the
// ambient kubeconfig points at (KUBECONFIG env, or ~/.kube/config's current
// context — exactly what `kubectl` and cmd/manager itself would use). It
// does not know anything about kind specifically: pointing KUBECONFIG at
// any real cluster with the jobtree CRDs and manager installed works.
func newClient(t *testing.T) client.Client {
	t.Helper()
	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		t.Fatalf("resolve kubeconfig for the e2e cluster (run `make kind-up`/`make e2e`, or point KUBECONFIG at a real cluster): %v", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add jobtree v1 scheme: %v", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c
}

// eventually polls fn until it returns nil or timeout elapses, failing the
// test with fn's last error otherwise. Mirrors controllers/kube/
// scenario_test.go's helper of the same name/shape, against a real cluster
// instead of envtest.
func eventually(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if lastErr = fn(); lastErr == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s: %v", timeout, lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// firstReadyNode returns the name of any schedulable, Ready node in the
// cluster — kind's single control-plane node in the default topology this
// harness creates.
func firstReadyNode(t *testing.T, ctx context.Context, c client.Client) string {
	t.Helper()
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	for _, n := range nodes.Items {
		if n.Spec.Unschedulable {
			continue
		}
		for _, cond := range n.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				return n.Name
			}
		}
	}
	t.Fatalf("no Ready, schedulable node found in the cluster")
	return ""
}

// waitForManagerReady waits for the controller manager Deployment to report
// at least one available replica — real positive signal that the image
// built+loaded by hack/e2e/run-e2e.sh actually started, versus the CI
// helm-template assertions (ci.yaml) which only check the chart *renders*
// the right shape without ever running it.
func waitForManagerReady(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	eventually(t, 120*time.Second, func() error {
		var dep appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: managerNamespace(), Name: controllerDeploymentName}, &dep); err != nil {
			return fmt.Errorf("get deployment %s/%s: %w", managerNamespace(), controllerDeploymentName, err)
		}
		if dep.Status.AvailableReplicas < 1 {
			return fmt.Errorf("deployment %s/%s has %d available replicas, want >=1", managerNamespace(), controllerDeploymentName, dep.Status.AvailableReplicas)
		}
		return nil
	})
}

// labelNodeAsGPU stamps a real node with the topology labels and a
// nvidia.com/gpu capacity entry the engine's pack/topology code reads for
// admission (controllers/kube/bridge.go's GPUCapacityResource,
// pkg/topology/labels.go's Label{Region,Cluster,FabricDomain,GPUFlavor}) —
// the same fields controllers/kube/scenario_test.go's createH100Node fixture
// sets, applied to a real Node object instead of an envtest one. It returns
// a cleanup func that restores the node as best-effort (label/capacity
// removal), for hygiene when running test/e2e repeatedly against one
// long-lived cluster.
func labelNodeAsGPU(t *testing.T, ctx context.Context, c client.Client, nodeName string, gpus int64) func() {
	t.Helper()

	var node corev1.Node
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		t.Fatalf("get node %s: %v", nodeName, err)
	}
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	node.Labels["region"] = e2eRegion
	node.Labels["cluster"] = e2eCluster
	node.Labels["fabric.domain"] = e2eFabricIsle
	node.Labels["gpu.flavor"] = e2eGPUFlavor
	if err := c.Update(ctx, &node); err != nil {
		t.Fatalf("label node %s: %v", nodeName, err)
	}

	if node.Status.Capacity == nil {
		node.Status.Capacity = corev1.ResourceList{}
	}
	if node.Status.Allocatable == nil {
		node.Status.Allocatable = corev1.ResourceList{}
	}
	node.Status.Capacity[corev1.ResourceName(kube.GPUCapacityResource)] = *resource.NewQuantity(gpus, resource.DecimalSI)
	node.Status.Allocatable[corev1.ResourceName(kube.GPUCapacityResource)] = *resource.NewQuantity(gpus, resource.DecimalSI)
	if err := c.Status().Update(ctx, &node); err != nil {
		t.Fatalf("patch node %s capacity: %v", nodeName, err)
	}

	return func() {
		var n corev1.Node
		if err := c.Get(context.Background(), types.NamespacedName{Name: nodeName}, &n); err != nil {
			return
		}
		delete(n.Labels, "region")
		delete(n.Labels, "cluster")
		delete(n.Labels, "fabric.domain")
		delete(n.Labels, "gpu.flavor")
		_ = c.Update(context.Background(), &n)
		delete(n.Status.Capacity, corev1.ResourceName(kube.GPUCapacityResource))
		delete(n.Status.Allocatable, corev1.ResourceName(kube.GPUCapacityResource))
		_ = c.Status().Update(context.Background(), &n)
	}
}

// createBudget creates a minimal single-envelope Budget scoped to the
// labelNodeAsGPU fixture's topology.
func createBudget(t *testing.T, ctx context.Context, c client.Client, name, owner string, concurrency int32) {
	t.Helper()
	budget := &v1.Budget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workNamespace},
		Spec: v1.BudgetSpec{
			Owner: owner,
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "e2e",
				Flavor:      e2eGPUFlavor,
				Selector:    e2eNodeSelector(),
				Concurrency: concurrency,
			}},
		},
	}
	if err := c.Create(ctx, budget); err != nil {
		t.Fatalf("create budget %s: %v", name, err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), budget) })
}

// listRunPods lists the workload pods jobtree materialized for a Run.
func listRunPods(t *testing.T, ctx context.Context, c client.Client, runName string) []corev1.Pod {
	t.Helper()
	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(workNamespace), client.MatchingLabels{binder.LabelRunName: runName}); err != nil {
		t.Fatalf("list pods for run %s: %v", runName, err)
	}
	return list.Items
}

// getRun fetches a Run by name in workNamespace.
func getRun(t *testing.T, ctx context.Context, c client.Client, name string) *v1.Run {
	t.Helper()
	var run v1.Run
	if err := c.Get(ctx, types.NamespacedName{Namespace: workNamespace, Name: name}, &run); err != nil {
		t.Fatalf("get run %s: %v", name, err)
	}
	return &run
}

// waitForRunPhase polls until the named Run reaches the given phase.
func waitForRunPhase(t *testing.T, ctx context.Context, c client.Client, name, phase string) *v1.Run {
	t.Helper()
	var run v1.Run
	eventually(t, 60*time.Second, func() error {
		if err := c.Get(ctx, types.NamespacedName{Namespace: workNamespace, Name: name}, &run); err != nil {
			return err
		}
		if run.Status.Phase != phase {
			return fmt.Errorf("run %s phase %q (message %q), want %q", name, run.Status.Phase, run.Status.Message, phase)
		}
		return nil
	})
	return &run
}

// isNotFound is a small readability wrapper around apierrors.IsNotFound.
func isNotFound(err error) bool { return apierrors.IsNotFound(err) }
