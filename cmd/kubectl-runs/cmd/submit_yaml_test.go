package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// TestSubmitAcceptsRealYAML proves submit decodes genuine YAML block syntax
// (not JSON saved under a .yaml extension — fake-features-audit.md #11
// called out doc_examples_test.go/root_test.go for exactly that). submit.go
// used to hard-reject any manifest not starting with '{'.
func TestSubmitAcceptsRealYAML(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	store := &StateStore{}
	initial := &controllers.ClusterState{
		Runs:         map[string]*v1.Run{},
		Reservations: map[string]*v1.Reservation{},
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team-a"},
			Spec: v1.BudgetSpec{
				Owner: "org:team-a",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west-h100",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a"},
					Concurrency: 8,
				}},
			},
		}},
		Nodes: []topology.SourceNode{
			{Name: "node-a1", Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "gpu-a", topology.LabelFabricDomain: "0", topology.LabelGPUFlavor: "H100-80GB"}, GPUs: 4},
		},
	}
	if err := store.Save(statePath, initial); err != nil {
		t.Fatalf("save initial state: %v", err)
	}

	// Genuine YAML block syntax: no braces, no quoted keys — this would be
	// rejected outright by the old `trimmed[0] != '{'` gate.
	manifest := `
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata:
  name: yaml-run
  namespace: default
spec:
  owner: org:team-a
  resources:
    gpuType: H100-80GB
    totalGPUs: 4
`
	manifestPath := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	root := NewRootCommand()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--local", "--state", statePath, "submit", "--file", manifestPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("submit real YAML: %v", err)
	}

	reloaded, err := store.Load(statePath)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	got, ok := reloaded.Runs[keys.NamespacedKey("default", "yaml-run")]
	if !ok {
		t.Fatalf("expected yaml-run to be persisted after submit")
	}
	if got.Spec.Resources.GPUType != "H100-80GB" || got.Spec.Resources.TotalGPUs != 4 {
		t.Fatalf("expected the YAML spec to decode correctly, got %+v", got.Spec.Resources)
	}
	if !strings.Contains(buf.String(), "yaml-run") {
		t.Fatalf("expected submit output to reference the run, got %s", buf.String())
	}
}
