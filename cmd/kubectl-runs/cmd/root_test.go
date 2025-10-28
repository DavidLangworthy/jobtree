package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func TestSubmitPlanAndBudgets(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.yaml")
	store := &StateStore{}
	initial := &controllers.ClusterState{
		Runs:         map[string]*v1.Run{},
		Reservations: map[string]*v1.Reservation{},
		Budgets: []v1.Budget{
			{
				ObjectMeta: v1.ObjectMeta{Name: "team-a"},
				Spec: v1.BudgetSpec{
					Owner: "org:team-a",
					Envelopes: []v1.BudgetEnvelope{
						{
							Name:        "west-h100",
							Flavor:      "H100-80GB",
							Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a"},
							Concurrency: 8,
						},
					},
				},
			},
		},
		Nodes: []topology.SourceNode{
			{Name: "node-a1", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "0", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
			{Name: "node-a2", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "0", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
			{Name: "node-a3", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "1", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
			{Name: "node-a4", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "1", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
		},
	}
	if err := store.Save(statePath, initial); err != nil {
		t.Fatalf("save initial state: %v", err)
	}

	runManifest := `{
  "apiVersion": "rq.davidlangworthy.io/v1",
  "kind": "Run",
  "metadata": {
    "name": "train-1"
  },
  "spec": {
    "owner": "org:team-a",
    "resources": {
      "gpuType": "H100-80GB",
      "totalGPUs": 4
    }
  }
}`
	manifestPath := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(manifestPath, []byte(runManifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	root := NewRootCommand()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--state", statePath, "--namespace", "default", "--output", "table", "submit", "--file", manifestPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("submit command: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "bound") {
		t.Fatalf("expected bound message in output, got %s", output)
	}

	// plan command should surface reservation info (none expected for immediate run)
	buf.Reset()
	root = NewRootCommand()
	root.SetOut(buf)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--state", statePath, "--namespace", "default", "--output", "table", "plan", "train-1"})
	if err := root.Execute(); err != nil {
		t.Fatalf("plan command: %v", err)
	}
	if !strings.Contains(buf.String(), "Run Plan") {
		t.Fatalf("expected plan header, got %s", buf.String())
	}

	// budgets usage command should show concurrency usage
	buf.Reset()
	root = NewRootCommand()
	root.SetOut(buf)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--state", statePath, "--output", "table", "budgets", "usage"})
	if err := root.Execute(); err != nil {
		t.Fatalf("budgets usage: %v", err)
	}
	if !strings.Contains(buf.String(), "Budget Usage") {
		t.Fatalf("expected budget usage header, got %s", buf.String())
	}
}

func TestSponsorsAndShrink(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.yaml")
	store := &StateStore{}
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "elastic", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team-a",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
			Malleable: &v1.RunMalleability{MinTotalGPUs: 4, MaxTotalGPUs: 8, StepGPUs: 4, DesiredTotalGPUs: ptrInt32(8)},
		},
	}
	state := &controllers.ClusterState{
		Runs:         map[string]*v1.Run{namespacedKey("default", "elastic"): run},
		Reservations: map[string]*v1.Reservation{},
		Budgets: []v1.Budget{
			{
				ObjectMeta: v1.ObjectMeta{Name: "team-a"},
				Spec: v1.BudgetSpec{
					Owner: "org:team-a",
					Envelopes: []v1.BudgetEnvelope{{
						Name:        "west-h100",
						Flavor:      "H100-80GB",
						Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a"},
						Concurrency: 16,
					}},
				},
			},
		},
		Nodes: []topology.SourceNode{
			{Name: "node-a1", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "0", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
			{Name: "node-a2", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "0", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
			{Name: "node-a3", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "1", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
			{Name: "node-a4", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "1", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
		},
	}
	if err := store.Save(statePath, state); err != nil {
		t.Fatalf("save state: %v", err)
	}

	root := NewRootCommand()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--state", statePath, "--namespace", "default", "--output", "table", "sponsors", "add", "--max", "4", "elastic", "org:team-b"})
	if err := root.Execute(); err != nil {
		t.Fatalf("sponsors add: %v", err)
	}
	if !strings.Contains(buf.String(), "org:team-b") {
		t.Fatalf("expected sponsor in output, got %s", buf.String())
	}

	buf.Reset()
	root = NewRootCommand()
	root.SetOut(buf)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--state", statePath, "--namespace", "default", "--output", "table", "shrink", "--by", "4", "elastic"})
	if err := root.Execute(); err != nil {
		t.Fatalf("shrink command: %v", err)
	}
	if !strings.Contains(buf.String(), "4") {
		t.Fatalf("expected shrink output to mention 4 GPUs, got %s", buf.String())
	}
}

func ptrInt32(v int32) *int32 { return &v }
