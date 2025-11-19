package cmd

import (
	"fmt"

	cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
)

// NewShrinkCommand enables voluntary shrink for elastic runs.
func NewShrinkCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	var by int
	cmd := &cobra.Command{
		Use:   "shrink RUN",
		Short: "Request a voluntary shrink for an elastic Run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if by <= 0 {
				return fmt.Errorf("--by must be > 0")
			}
			name := args[0]
			state, err := store.Load(opts.StatePath)
			if err != nil {
				return err
			}
			if err := ensureRunExists(state, opts.Namespace, name); err != nil {
				return err
			}
			if err := ensureMalleable(state, opts.Namespace, name); err != nil {
				return err
			}
			key := namespacedKey(opts.Namespace, name)
			run := state.Runs[key]
			desired := run.Spec.Malleable.MaxTotalGPUs
			if run.Spec.Malleable.DesiredTotalGPUs != nil {
				desired = *run.Spec.Malleable.DesiredTotalGPUs
			}
			target := desired - int32(by)
			clamped, err := clampDesired(run.Spec.Malleable.MinTotalGPUs, run.Spec.Malleable.MaxTotalGPUs, target)
			if err != nil {
				return err
			}
			run.Spec.Malleable.DesiredTotalGPUs = &clamped
			if err := reconcileRun(state, opts.Namespace, name); err != nil {
				return err
			}
			if err := store.Save(opts.StatePath, state); err != nil {
				return err
			}
			payload := Payload{
				Headers: []string{"Run", "DesiredGPUs"},
				Rows:    [][]string{{key, fmt.Sprintf("%d", clamped)}},
				Raw: map[string]interface{}{
					"run": run,
				},
				Title: "Shrink Requested",
			}
			return printer.Print(cmd, opts, payload)
		},
	}
	cmd.Flags().IntVar(&by, "by", 0, "Number of GPUs to remove from desired width")
	return cmd
}
