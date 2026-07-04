package kube

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// TestRunEmitsRealAdmittedEvent proves audit finding #9 ("event streams")
// closed for real: admitting a run emits a genuine corev1.Event, read back
// from the API server exactly like `kubectl describe` would show it — not
// just a CLI polling a local JSON file, and not only a log line.
func TestRunEmitsRealAdmittedEvent(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)

	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "events-run", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	eventually(t, 20*time.Second, func() error {
		var got v1.Run
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "events-run"}, &got); err != nil {
			return err
		}
		if got.Status.Phase != "Running" {
			return fmt.Errorf("run phase %q, want Running", got.Status.Phase)
		}
		return nil
	})

	eventually(t, 20*time.Second, func() error {
		var events corev1.EventList
		if err := kubeClient.List(suiteCtx, &events); err != nil {
			return err
		}
		for _, ev := range events.Items {
			if ev.InvolvedObject.Kind != "Run" || ev.InvolvedObject.Name != "events-run" {
				continue
			}
			if ev.Reason == "Admitted" && ev.Type == corev1.EventTypeNormal {
				return nil
			}
		}
		return fmt.Errorf("no real Admitted/Normal event found for run events-run yet (saw %d events)", len(events.Items))
	})
}
