package cmd

import (
	"fmt"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// NewEtaCommand sets a Run's estimated completion time directly (source
// "controller"). This is a --local-only development/demo convenience: against
// a live cluster the workload reports its ETA via a pod annotation the
// controller mirrors (see docs/user-guide/researcher-guide.md), so the CLI
// must not write a fabricated ETA onto a live Run's status. ETA is
// observability only — it never affects scheduling.
func NewEtaCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eta RUN TIME",
		Short: "Set a Run's estimated completion time (RFC3339); observability only (--local only)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !opts.UseLocal() {
				return fmt.Errorf("eta requires --local: against a live cluster the ETA is written by the workload reporting to the controller, not by this CLI")
			}
			name := args[0]
			eta, err := time.Parse(time.RFC3339, args[1])
			if err != nil {
				return fmt.Errorf("TIME must be RFC3339 (e.g. 2026-07-03T18:00:00Z): %w", err)
			}
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
			key := keys.NamespacedKey(opts.Namespace, name)
			run := state.Runs[key]
			run.Status.ETA = &v1.RunETA{
				EstimatedCompletion: v1.NewTime(eta.UTC()),
				ReportedAt:          v1.NewTime(time.Now().UTC()),
				Source:              "controller",
			}
			if err := store.Save(opts.StatePath, state); err != nil {
				return err
			}
			payload := Payload{
				Headers: []string{"Run", "ETA"},
				Rows:    [][]string{{key, eta.UTC().Format(time.RFC3339)}},
				Raw:     map[string]interface{}{"run": run},
				Title:   "ETA Set",
			}
			return printer.Print(cmd, opts, payload)
		},
	}
	return cmd
}
