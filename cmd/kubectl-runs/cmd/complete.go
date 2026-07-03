package cmd

import (
	"fmt"

	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// NewCompleteCommand marks a Run's workload as finished (dev/snapshot path).
// Against a live cluster the controller completes a run when its workload pods
// reach Succeeded; the JSON snapshot has no live pods, so this marks the run's
// active pods Succeeded and reconciles, exercising the same completion path
// (leases close, GPUs and budget free).
func NewCompleteCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "complete RUN",
		Short: "Mark a Run's workload as finished, closing its leases (dev/snapshot)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			unlock, err := store.Lock(opts.StatePath)
			if err != nil {
				return err
			}
			defer unlock()
			state, err := store.Load(opts.StatePath)
			if err != nil {
				return err
			}
			if err := ensureRunExists(state, opts.Namespace, name); err != nil {
				return err
			}
			marked := 0
			for i := range state.Pods {
				pod := &state.Pods[i]
				if pod.Namespace != opts.Namespace || pod.Labels[binder.LabelRunName] != name {
					continue
				}
				if pod.Labels[binder.LabelRunRole] == binder.RoleSpare {
					continue
				}
				pod.Phase = binder.PodPhaseSucceeded
				marked++
			}
			if marked == 0 {
				return fmt.Errorf("run %s has no active pods to complete", name)
			}
			if err := reconcileRun(state, opts.Namespace, name); err != nil {
				return err
			}
			if err := store.Save(opts.StatePath, state); err != nil {
				return err
			}
			key := keys.NamespacedKey(opts.Namespace, name)
			run := state.Runs[key]
			payload := Payload{
				Headers: []string{"Run", "Phase"},
				Rows:    [][]string{{key, run.Status.Phase}},
				Raw:     map[string]interface{}{"run": run},
				Title:   "Run Completed",
			}
			return printer.Print(cmd, opts, payload)
		},
	}
	return cmd
}
