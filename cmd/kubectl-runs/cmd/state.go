package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// StateStore provides helpers for persisting the in-memory controller state.
type StateStore struct{}

// Load retrieves the cluster state from the configured path, creating a default snapshot when the file is missing.
func (s *StateStore) Load(path string) (*controllers.ClusterState, error) {
	if path == "" {
		return nil, errors.New("state path must be provided")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &controllers.ClusterState{
				Runs:         map[string]*v1.Run{},
				Reservations: map[string]*v1.Reservation{},
			}, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}

	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	return snap.toState(), nil
}

// Save persists the cluster state to the configured path.
func (s *StateStore) Save(path string, state *controllers.ClusterState) error {
	if path == "" {
		return errors.New("state path must be provided")
	}
	if state == nil {
		return errors.New("state must not be nil")
	}
	snap := fromState(state)
	payload, err := json.MarshalIndent(&snap, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("ensure state directory: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// snapshot is the serialisable representation of ClusterState.
type snapshot struct {
	Runs         []v1.Run             `json:"runs,omitempty"`
	Budgets      []v1.Budget          `json:"budgets,omitempty"`
	Nodes        []nodeSnapshot       `json:"nodes,omitempty"`
	Leases       []v1.Lease           `json:"leases,omitempty"`
	Pods         []binder.PodManifest `json:"pods,omitempty"`
	Reservations []v1.Reservation     `json:"reservations,omitempty"`
}

type nodeSnapshot struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	GPUs   int               `json:"gpus,omitempty"`
}

func (s snapshot) toState() *controllers.ClusterState {
	state := &controllers.ClusterState{
		Runs:         make(map[string]*v1.Run, len(s.Runs)),
		Budgets:      make([]v1.Budget, len(s.Budgets)),
		Nodes:        make([]topology.SourceNode, len(s.Nodes)),
		Leases:       make([]v1.Lease, len(s.Leases)),
		Pods:         append([]binder.PodManifest{}, s.Pods...),
		Reservations: make(map[string]*v1.Reservation, len(s.Reservations)),
	}
	for i := range s.Nodes {
		node := s.Nodes[i]
		state.Nodes[i] = topology.SourceNode{Name: node.Name, Labels: cloneStringMap(node.Labels), GPUs: node.GPUs}
	}
	for i := range s.Budgets {
		state.Budgets[i] = *s.Budgets[i].DeepCopy()
	}
	for i := range s.Leases {
		state.Leases[i] = *s.Leases[i].DeepCopy()
	}
	for i := range s.Runs {
		run := s.Runs[i]
		copy := *run.DeepCopy()
		key := namespacedKey(copy.Namespace, copy.Name)
		state.Runs[key] = &copy
	}
	for i := range s.Reservations {
		res := s.Reservations[i]
		copy := *res.DeepCopy()
		key := namespacedKey(copy.Namespace, copy.Name)
		state.Reservations[key] = &copy
	}
	return state
}

func fromState(state *controllers.ClusterState) snapshot {
	snap := snapshot{}
	for _, budget := range state.Budgets {
		snap.Budgets = append(snap.Budgets, *budget.DeepCopy())
	}
	for _, lease := range state.Leases {
		snap.Leases = append(snap.Leases, *lease.DeepCopy())
	}
	for _, node := range state.Nodes {
		snap.Nodes = append(snap.Nodes, nodeSnapshot{Name: node.Name, Labels: cloneStringMap(node.Labels), GPUs: node.GPUs})
	}
	snap.Pods = append(snap.Pods, state.Pods...)

	runKeys := make([]string, 0, len(state.Runs))
	for key := range state.Runs {
		runKeys = append(runKeys, key)
	}
	sort.Strings(runKeys)
	for _, key := range runKeys {
		snap.Runs = append(snap.Runs, *state.Runs[key].DeepCopy())
	}

	resKeys := make([]string, 0, len(state.Reservations))
	for key := range state.Reservations {
		resKeys = append(resKeys, key)
	}
	sort.Strings(resKeys)
	for _, key := range resKeys {
		snap.Reservations = append(snap.Reservations, *state.Reservations[key].DeepCopy())
	}
	return snap
}

// namespacedKey mirrors the helper from the controller package without creating an import cycle.
func namespacedKey(namespace, name string) string {
	if namespace == "" {
		namespace = "default"
	}
	return namespace + "/" + name
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
