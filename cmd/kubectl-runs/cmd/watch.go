package cmd

import (
	"fmt"
	"time"

	cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
)

// NewWatchCommand streams run status repeatedly.
func NewWatchCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch RUN",
		Short: "Continuously render run status and reservation forecasts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			iterations := opts.WatchCount
			if iterations == 0 {
				iterations = int(^uint(0) >> 1)
			}
			interval := waitDuration(opts.WatchInterval)
			for i := 0; i < iterations; i++ {
				if i > 0 {
					time.Sleep(interval)
				}
				state, err := store.Load(opts.StatePath)
				if err != nil {
					return err
				}
				if err := ensureRunExists(state, opts.Namespace, name); err != nil {
					return err
				}
				if err := reconcileRun(state, opts.Namespace, name); err != nil {
					return err
				}
				key := namespacedKey(opts.Namespace, name)
				payload := buildPlanPayload(state, opts, state.Runs[key])
				payload.Title = fmt.Sprintf("[%s] iteration %d", time.Now().Format(time.RFC3339), i+1)
				if err := printer.Print(cmd, opts, payload); err != nil {
					return err
				}
				if err := store.Save(opts.StatePath, state); err != nil {
					return err
				}
			}
			return nil
		},
	}
	return cmd
}
