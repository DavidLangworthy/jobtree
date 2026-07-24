package kube

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// TestRunEmitsRealSchedulingEvent proves audit finding #9 ("event streams")
// closed for real: when the manager requests width for a run (emits its
// unscheduled intent pods for the scheduler plugin to place and fund), it emits
// a genuine corev1.Event, read back from the API server exactly like `kubectl
// describe` would show it — not a CLI polling a local JSON file, not only a log
// line. Post-cutover the controller no longer emits "Admitted" on a bind it no
// longer performs; "Scheduling" is the honest event on the path it does own.
// (End-to-end admission + the plugin's own events are proven on a live cluster
// by hack/e2e/fullstack-smoke.sh.)
func TestRunEmitsRealSchedulingEvent(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)

	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "events-run", Namespace: "default"},
		Spec: v1.RunSpec{
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	eventually(t, 20*time.Second, func() error {
		var events corev1.EventList
		if err := kubeClient.List(suiteCtx, &events); err != nil {
			return err
		}
		for _, ev := range events.Items {
			if ev.InvolvedObject.Kind != "Run" || ev.InvolvedObject.Name != "events-run" {
				continue
			}
			if ev.Reason == "Scheduling" && ev.Type == corev1.EventTypeNormal {
				return nil
			}
		}
		return fmt.Errorf("no real Scheduling/Normal event found for run events-run yet (saw %d events)", len(events.Items))
	})
}
