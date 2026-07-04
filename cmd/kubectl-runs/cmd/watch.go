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
		Short: "Continuously render run status and reservation forecasts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			iterations := opts.WatchCount
			if iterations == 0 {
				iterations = int(^uint(0) >> 1)
			}
			interval := waitDuration(opts.WatchInterval)

			var iterate func(i int) error
			if opts.UseLocal() {
				// Hold the lock for the whole load-modify-save cycle so a
				// concurrent CLI invocation cannot lose this iteration's write.
				iterate = func(i int) error {
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
			} else {
				c, err := opts.LiveClient()
				if err != nil {
					return err
				}
				ctx := cmd.Context()
				iterate = func(i int) error {
					run, err := liveGetRun(ctx, c, opts.Namespace, name)
					if err != nil {
						return err
					}
					var reservationName string
					if run.Status.PendingReservation != nil {
						reservationName = *run.Status.PendingReservation
					}
					reservation, err := liveGetReservation(ctx, c, opts.Namespace, reservationName)
					if err != nil {
						return err
					}
					state := reservationLookupState(opts.Namespace, reservation)
					payload := buildPlanPayload(state, opts, run)
					payload.Title = fmt.Sprintf("[%s] iteration %d", time.Now().Format(time.RFC3339), i+1)
					return printer.Print(cmd, opts, payload)
				}
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
