package cmd

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

func TestEtaCommandSetsControllerSourcedStatus(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.yaml")
	store := &StateStore{}
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "job", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
		Status:     v1.RunStatus{Phase: "Running"},
	}
	state := &controllers.ClusterState{
		Runs:         map[string]*v1.Run{keys.NamespacedKey("default", "job"): run},
		Reservations: map[string]*v1.Reservation{},
	}
	if err := store.Save(statePath, state); err != nil {
		t.Fatalf("save: %v", err)
	}

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--state", statePath, "--namespace", "default", "eta", "job", "2026-07-04T00:00:00Z"})
	if err := root.Execute(); err != nil {
		t.Fatalf("eta: %v", err)
	}

	reloaded, err := store.Load(statePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	eta := reloaded.Runs[keys.NamespacedKey("default", "job")].Status.ETA
	if eta == nil {
		t.Fatalf("expected ETA set")
	}
	if eta.Source != "controller" {
		t.Errorf("source = %q, want controller", eta.Source)
	}
	if got := eta.EstimatedCompletion.Time.UTC().Format(time.RFC3339); got != "2026-07-04T00:00:00Z" {
		t.Errorf("estimatedCompletion = %q, want 2026-07-04T00:00:00Z", got)
	}
}
