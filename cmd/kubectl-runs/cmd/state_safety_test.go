package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func seedStateFile(t *testing.T) string {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "cluster-state.json")
	store := &StateStore{}
	state := &controllers.ClusterState{
		Runs: map[string]*v1.Run{
			keys.NamespacedKey("default", "train"): {
				ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
				Spec: v1.RunSpec{
					Owner:     "org:team-a",
					Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
				},
			},
		},
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
	if err := store.Save(statePath, state); err != nil {
		t.Fatalf("save initial state: %v", err)
	}
	return statePath
}

// R13: read-looking commands must leave the state file byte-identical.
func TestReadCommandsLeaveStateFileUntouched(t *testing.T) {
	statePath := seedStateFile(t)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	commands := [][]string{
		{"--local", "--state", statePath, "plan", "train"},
		{"--local", "--state", statePath, "explain", "train"},
		{"--local", "--state", statePath, "budgets", "usage"},
		{"--local", "--state", statePath, "leases", "train"},
	}
	for _, args := range commands {
		root := NewRootCommand()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v failed: %v", args, err)
		}
		after, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatalf("read state after %v: %v", args, err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("command %v modified the state file", args)
		}
	}
}

// R13: concurrent load-modify-save cycles under the advisory lock must not
// lose writes.
func TestStateStoreLockPreventsLostWrites(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "cluster-state.json")
	store := &StateStore{}
	if err := store.Save(statePath, &controllers.ClusterState{
		Runs:         map[string]*v1.Run{},
		Reservations: map[string]*v1.Reservation{},
	}); err != nil {
		t.Fatalf("save initial state: %v", err)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			unlock, err := store.Lock(statePath)
			if err != nil {
				errs <- err
				return
			}
			defer unlock()
			state, err := store.Load(statePath)
			if err != nil {
				errs <- err
				return
			}
			name := fmt.Sprintf("run-%d", w)
			state.Runs[keys.NamespacedKey("default", name)] = &v1.Run{
				ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: v1.RunSpec{
					Owner:     "org:team-a",
					Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1},
				},
			}
			errs <- store.Save(statePath, state)
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("writer failed: %v", err)
		}
	}

	final, err := store.Load(statePath)
	if err != nil {
		t.Fatalf("load final state: %v", err)
	}
	if len(final.Runs) != writers {
		t.Fatalf("lost writes: expected %d runs, got %d", writers, len(final.Runs))
	}
}

// R13: Save must not leave temp files behind, and the write is atomic (the
// file always parses).
func TestStateStoreSaveIsAtomicAndTidy(t *testing.T) {
	statePath := seedStateFile(t)
	dir := filepath.Dir(statePath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", entry.Name())
		}
	}

	store := &StateStore{}
	if _, err := store.Load(statePath); err != nil {
		t.Fatalf("state does not parse after save: %v", err)
	}
}
