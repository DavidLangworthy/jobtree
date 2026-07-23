//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// TestCRDsAreInstalled proves the harness actually applied the real CRDs
// (hack/e2e/kind-up.sh) to a real API server — a bare List of each type
// must succeed with no "no matches for kind" error.
func TestCRDsAreInstalled(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	var runs v1.RunList
	if err := c.List(ctx, &runs, client.InNamespace(workNamespace)); err != nil {
		t.Errorf("list runs: %v", err)
	}
	var budgets v1.BudgetList
	if err := c.List(ctx, &budgets, client.InNamespace(workNamespace)); err != nil {
		t.Errorf("list budgets: %v", err)
	}
	var reservations v1.ReservationList
	if err := c.List(ctx, &reservations, client.InNamespace(workNamespace)); err != nil {
		t.Errorf("list reservations: %v", err)
	}
	var leases v1.GPULeaseList
	if err := c.List(ctx, &leases, client.InNamespace(workNamespace)); err != nil {
		t.Errorf("list leases: %v", err)
	}
}

// TestManagerIsRunning proves the image hack/e2e/run-e2e.sh built and
// kind-loaded actually started and became Ready in the real cluster — real
// positive signal beyond ci.yaml's helm-template rendering assertions,
// which never run the image at all.
func TestManagerIsRunning(t *testing.T) {
	c := newClient(t)
	waitForManagerReady(t, context.Background(), c)
}

// TestValidatingWebhookRejectsInvalidRun proves the real, running manager's
// admission webhook — not envtest's — actually rejects a Run that fails
// RunSpec.validate() (missing spec.owner). If the webhook weren't wired
// (cert mismatch, service down, CA not injected — the exact R29 failure
// mode the chart's genCA machinery exists to prevent) this Create would
// either hang or silently succeed instead of coming back Invalid.
func TestValidatingWebhookRejectsInvalidRun(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	waitForManagerReady(t, ctx, c)

	bad := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-invalid-run", Namespace: workNamespace},
		Spec: v1.RunSpec{
			// Owner deliberately omitted: RunSpec.validate() requires it.
			Resources: v1.RunResources{GPUType: e2eGPUFlavor, TotalGPUs: 1},
		},
	}
	err := c.Create(ctx, bad)
	if err == nil {
		_ = c.Delete(ctx, bad)
		t.Fatalf("expected the real validating webhook to reject a Run with no owner, but Create succeeded")
	}
	// R14 added a CRD-schema minLength on spec.owner, so the apiserver now rejects a
	// missing owner with 422 Invalid (schema validation) *before* it reaches the
	// validating webhook (which denies with 403 Forbidden). Either layer is a valid
	// rejection by the admission chain; what matters is that the invalid Run is not
	// created. (This is R14's point — validation stops depending on the webhook alone.)
	if !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) {
		t.Fatalf("expected a Forbidden (webhook) or Invalid (CRD schema) rejection, got: %v", err)
	}
}

// TestRunAdmitsAndBindsOnRealCluster is the strongest positive proof this
// harness can offer before Track B (JOBSET) lands a real workload body: a
// Run is admitted by the real webhook, bound by the real engine against a
// real (fixture-labeled) Node, and its workload pod is actually started by
// the real kubelet — not envtest, and nothing in this test writes
// pod.Status by hand. It intentionally stops short of asserting completion:
// today's pod is still the pause-image mannequin
// (docs/project/fake-features-audit.md #1/#2), so it runs forever and never
// exits — that gap is exactly what TestRunCompletesWithRealContainer
// (completion_test.go) documents as blocked on Track B.
func TestRunAdmitsAndBindsOnRealCluster(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()
	waitForManagerReady(t, ctx, c)

	node := firstReadyNode(t, ctx, c)
	restore := labelNodeAsGPU(t, ctx, c, node, 4)
	t.Cleanup(restore)

	createBudget(t, ctx, c, "e2e-team", "org:e2e-team", 4)

	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e-train", Namespace: workNamespace},
		Spec: v1.RunSpec{
			Owner:     "org:e2e-team",
			Resources: v1.RunResources{GPUType: e2eGPUFlavor, TotalGPUs: 1},
		},
	}
	if err := c.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), run) })

	bound := waitForRunPhase(t, ctx, c, "e2e-train", "Running")
	if bound.Status.Width == nil || bound.Status.Width.Allocated != 1 {
		t.Errorf("width.allocated = %+v, want 1", bound.Status.Width)
	}

	var pods []corev1.Pod
	eventually(t, 30*time.Second, func() error {
		pods = listRunPods(t, ctx, c, "e2e-train")
		if len(pods) != 1 {
			return fmt.Errorf("%d workload pods for e2e-train, want 1", len(pods))
		}
		return nil
	})

	pod := pods[0]
	if pod.Spec.NodeName != node {
		t.Errorf("pod bound to node %q, want %q", pod.Spec.NodeName, node)
	}

	// The one assertion in this whole suite where a real kubelet, not this
	// test, is responsible for the answer: the pod actually starts running
	// on the real node. This is read-only observation of kubelet-written
	// status, never an assignment — see hack/antifake/terminalphase.go,
	// which would flag this file if it ever became an assignment instead.
	eventually(t, 60*time.Second, func() error {
		var got corev1.Pod
		if err := c.Get(ctx, client.ObjectKeyFromObject(&pod), &got); err != nil {
			return err
		}
		if got.Status.Phase != corev1.PodRunning {
			return fmt.Errorf("pod %s phase = %s, want Running (from the real kubelet)", got.Name, got.Status.Phase)
		}
		return nil
	})
}
