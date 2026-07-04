package cmd

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

func reconcileRun(state *controllers.ClusterState, namespace, name string) error {
	controller := controllers.NewRunController(state, controllers.RealClock{})
	if err := controller.Reconcile(namespace, name); err != nil {
		return err
	}
	simulatePluginCommit(state)
	// Re-reconcile so the adoption path flips the just-committed run Running.
	return controller.Reconcile(namespace, name)
}

func reconcileAll(state *controllers.ClusterState) error {
	controller := controllers.NewRunController(state, controllers.RealClock{})
	// Sorted iteration keeps admission order — and therefore who wins the
	// GPUs when runs compete — deterministic across invocations.
	runKeys := make([]string, 0, len(state.Runs))
	for key := range state.Runs {
		runKeys = append(runKeys, key)
	}
	sort.Strings(runKeys)
	for _, key := range runKeys {
		run := state.Runs[key]
		if run == nil {
			continue
		}
		if err := controller.Reconcile(run.Namespace, run.Name); err != nil {
			return err
		}
	}
	simulatePluginCommit(state)
	for _, key := range runKeys {
		run := state.Runs[key]
		if run == nil {
			continue
		}
		if err := controller.Reconcile(run.Namespace, run.Name); err != nil {
			return err
		}
	}
	return nil
}

// simulatePluginCommit is the OFFLINE (--local) stand-in for the real scheduler
// plugin. On a live cluster the plugin schedules the controller's intent pods
// and mints the Lease; the offline simulator has no scheduler, so — for each run
// Reconcile left Pending with intent pods but no lease — it mints the leases
// admission.Plan would produce, so the demo shows the realistic bound state. A
// run that cannot be admitted now (admission.Plan errors → it reserves) is left
// Pending. This never runs on the live path; there the plugin is authoritative.
func simulatePluginCommit(state *controllers.ClusterState) {
	now := time.Now().UTC()
	for _, run := range state.Runs {
		if run == nil || run.Status.Phase != controllers.RunPhasePending {
			continue
		}
		if runHasOpenLease(state, run.Namespace, run.Name) {
			continue
		}
		res, err := admission.Plan(admission.Input{
			Run:     run,
			Budgets: state.Budgets,
			Runs:    state.Runs,
			Leases:  state.Leases,
			Nodes:   state.Nodes,
			Now:     now,
		})
		if err != nil {
			continue // not admittable now; the controller reserves it
		}
		state.Leases = append(state.Leases, res.Leases...)
	}
}

func runHasOpenLease(state *controllers.ClusterState, namespace, name string) bool {
	for i := range state.Leases {
		l := &state.Leases[i]
		if l.Spec.RunRef.Namespace == namespace && l.Spec.RunRef.Name == name && !l.Status.Closed {
			return true
		}
	}
	return false
}

func waitDuration(interval int) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	return time.Duration(interval) * time.Second
}

func ensureRunExists(state *controllers.ClusterState, namespace, name string) error {
	key := keys.NamespacedKey(namespace, name)
	if _, ok := state.Runs[key]; !ok {
		return fmt.Errorf("run %s not found", key)
	}
	return nil
}

func copySlice[T any](src []T) []T {
	if len(src) == 0 {
		return nil
	}
	dst := make([]T, len(src))
	copy(dst, src)
	return dst
}

func uniqueAppend(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func requireArgument(args []string, index int, name string) (string, error) {
	if len(args) <= index {
		return "", fmt.Errorf("%s is required", name)
	}
	value := args[index]
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return value, nil
}

func clampDesired(min, max, value int32) (int32, error) {
	if value < min {
		return 0, fmt.Errorf("desired GPUs %d cannot be less than min %d", value, min)
	}
	if value > max {
		return 0, fmt.Errorf("desired GPUs %d cannot exceed max %d", value, max)
	}
	return value, nil
}

var errNoMalleability = errors.New("run is not malleable")

func ensureMalleable(state *controllers.ClusterState, namespace, name string) error {
	key := keys.NamespacedKey(namespace, name)
	obj := state.Runs[key]
	if obj == nil || obj.Spec.Malleable == nil {
		return errNoMalleability
	}
	return nil
}
