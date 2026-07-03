package cmd

import (
	"bytes"
	"path/filepath"
	"testing"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

func TestCompleteCommandFinishesRunAndClosesLeases(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.yaml")
	store := &StateStore{}

	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "job", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
		Status:     v1.RunStatus{Phase: "Running"},
	}
	lease := v1.Lease{
		ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "job-lease", Labels: map[string]string{binder.LabelRunName: "job"}},
		Spec: v1.LeaseSpec{
			Owner:  "org:team",
			RunRef: v1.RunReference{Name: "job", Namespace: "default"},
			Slice:  v1.LeaseSlice{Nodes: []string{"node-a#0"}, Role: binder.RoleActive},
		},
	}
	state := &controllers.ClusterState{
		Runs:         map[string]*v1.Run{keys.NamespacedKey("default", "job"): run},
		Reservations: map[string]*v1.Reservation{},
		Leases:       []v1.Lease{lease},
		Pods: []binder.PodManifest{{
			Namespace: "default", Name: "job-p0", Phase: "Running",
			Labels: map[string]string{binder.LabelRunName: "job", binder.LabelRunRole: binder.RoleActive},
		}},
	}
	if err := store.Save(statePath, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--state", statePath, "--namespace", "default", "--output", "table", "complete", "job"})
	if err := root.Execute(); err != nil {
		t.Fatalf("complete: %v", err)
	}

	reloaded, err := store.Load(statePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Runs[keys.NamespacedKey("default", "job")].Status.Phase; got != "Completed" {
		t.Fatalf("expected Completed, got %s", got)
	}
	if len(reloaded.Leases) != 1 || !reloaded.Leases[0].Status.Closed || reloaded.Leases[0].Status.ClosureReason != "Completed" {
		t.Fatalf("expected the lease closed with reason Completed, got %+v", reloaded.Leases)
	}
}
