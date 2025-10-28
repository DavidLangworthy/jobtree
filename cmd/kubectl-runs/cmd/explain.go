package cmd

import (
	"fmt"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
	"github.com/davidlangworthy/jobtree/controllers"
)

// NewExplainCommand produces the explain subcommand.
func NewExplainCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explain RUN",
		Short: "Explain current scheduling state for a Run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
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
			run := state.Runs[key]
			payload := buildExplainPayload(state, run)
			if err := store.Save(opts.StatePath, state); err != nil {
				return err
			}
			return printer.Print(cmd, opts, payload)
		},
	}
	return cmd
}

func buildExplainPayload(state *controllers.ClusterState, run *v1.Run) Payload {
	key := namespacedKey(run.Namespace, run.Name)
	rows := [][]string{
		{"Run", key},
		{"Phase", run.Status.Phase},
		{"Message", run.Status.Message},
	}
	if run.Status.Width != nil {
		rows = append(rows,
			[]string{"Width", fmt.Sprintf("desired=%d allocated=%d", run.Status.Width.Desired, run.Status.Width.Allocated)},
		)
		if run.Status.Width.Pending != "" {
			rows = append(rows, []string{"PendingWidth", run.Status.Width.Pending})
		}
	}
	if run.Status.Funding != nil {
		rows = append(rows, []string{"OwnedGPUs", fmt.Sprintf("%d", run.Status.Funding.OwnedGPUs)})
		rows = append(rows, []string{"BorrowedGPUs", fmt.Sprintf("%d", run.Status.Funding.BorrowedGPUs)})
		for _, sponsor := range run.Status.Funding.Sponsors {
			rows = append(rows, []string{"Sponsor", fmt.Sprintf("%s (%d GPUs)", sponsor.Owner, sponsor.GPUs)})
		}
	}
	if run.Status.PendingReservation != nil {
		resKey := namespacedKey(run.Namespace, *run.Status.PendingReservation)
		if reservation := state.Reservations[resKey]; reservation != nil {
			rows = append(rows, []string{"PendingReservation", reservation.Name})
			if reservation.Status.Forecast.DeficitGPUs > 0 {
				rows = append(rows, []string{"ReservationDeficit", fmt.Sprintf("%d GPUs", reservation.Status.Forecast.DeficitGPUs)})
			}
			if reservation.Status.Reason != "" {
				rows = append(rows, []string{"ReservationReason", reservation.Status.Reason})
			}
		}
	}
	return Payload{
		Headers: []string{"Field", "Value"},
		Rows:    rows,
		Raw: map[string]interface{}{
			"run": run,
		},
		Title: "Run Explanation",
	}
}
