package cmd

import (
	"fmt"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// NewPlanCommand creates the plan subcommand.
func NewPlanCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan RUN",
		Short: "Show reservation plan and forecast for a Run (read-only; the state file is not modified)",
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
			key := keys.NamespacedKey(opts.Namespace, name)
			run := state.Runs[key]
			payload := buildPlanPayload(state, opts, run)
			return printer.Print(cmd, opts, payload)
		},
	}
	return cmd
}

func buildPlanPayload(state *controllers.ClusterState, opts *RootOptions, run *v1.Run) Payload {
	key := keys.NamespacedKey(run.Namespace, run.Name)
	rows := [][]string{
		{"Run", key},
		{"Phase", run.Status.Phase},
		{"Message", run.Status.Message},
	}
	var reservation *v1.Reservation
	if run.Status.PendingReservation != nil {
		resKey := keys.NamespacedKey(run.Namespace, *run.Status.PendingReservation)
		reservation = state.Reservations[resKey]
		if run.Status.EarliestStart != nil {
			rows = append(rows, []string{"EarliestStart", run.Status.EarliestStart.Format(time.RFC3339)})
		}
		if reservation != nil && reservation.Status.Forecast.DeficitGPUs > 0 {
			rows = append(rows, []string{"Deficit", fmt.Sprintf("%d GPUs", reservation.Status.Forecast.DeficitGPUs)})
		}
		if reservation != nil && reservation.Status.Forecast.Confidence != "" {
			rows = append(rows, []string{"Confidence", reservation.Status.Forecast.Confidence})
		}
	}
	raw := map[string]interface{}{
		"run": run,
	}
	if reservation != nil {
		raw["reservation"] = reservation
	}
	return Payload{
		Headers: []string{"Field", "Value"},
		Rows:    rows,
		Raw:     raw,
		Title:   "Run Plan",
	}
}
