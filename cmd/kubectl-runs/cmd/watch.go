package cmd

import (
	"fmt"
	"time"

	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// NewWatchCommand streams run status repeatedly.
func NewWatchCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch RUN",
		Short: "Continuously render run status and reservation forecasts (reconciles and persists state each iteration)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			iterations := opts.WatchCount
			if iterations == 0 {
				iterations = int(^uint(0) >> 1)
			}
			interval := waitDuration(opts.WatchInterval)
			iterate := func(i int) error {
				// Hold the lock for the whole load-modify-save cycle so a
				// concurrent CLI invocation cannot lose this iteration's write.
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
				if err := reconcileRun(state, opts.Namespace, name); err != nil {
					return err
				}
				key := keys.NamespacedKey(opts.Namespace, name)
				payload := buildPlanPayload(state, opts, state.Runs[key])
				payload.Title = fmt.Sprintf("[%s] iteration %d", time.Now().Format(time.RFC3339), i+1)
				if err := printer.Print(cmd, opts, payload); err != nil {
					return err
				}
				return store.Save(opts.StatePath, state)
			}
			for i := 0; i < iterations; i++ {
				if i > 0 {
					time.Sleep(interval)
				}
				if err := iterate(i); err != nil {
					return err
				}
			}
			return nil
		},
	}
	return cmd
}
