package cmd

import (
	"fmt"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/spf13/cobra"
)

// NewBudgetsCommand groups budget-related subcommands.
func NewBudgetsCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "budgets",
		Short: "Inspect budget usage and headroom",
	}
	cmd.AddCommand(newBudgetsUsageCommand(opts, store, printer))
	return cmd
}

func newBudgetsUsageCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show budget usage by funding class and remaining headroom (read-only; the state file is not modified)",
		RunE: func(cmd *cobra.Command, args []string) error {
			state, err := store.Load(opts.StatePath)
			if err != nil {
				return err
			}
			if err := reconcileAll(state); err != nil {
				return err
			}
			rows := [][]string{}
			raw := make([]map[string]interface{}, 0)
			now := time.Now().UTC()
			// One evaluation for the whole state: classification is global
			// (family sharing and lending cross budget boundaries).
			ev := funding.Evaluate(funding.Input{
				Budgets: state.Budgets,
				Leases:  state.Leases,
				Runs:    state.Runs,
				Now:     now,
			})
			for i := range state.Budgets {
				budgetObj := state.Budgets[i]
				copy := budgetObj
				controller := controllers.NewBudgetController(controllers.RealClock{}, controllers.NewBudgetMetrics())
				status := controller.ReconcileBudget(&copy, ev)
				for _, usage := range status.Usage {
					head := findHeadroom(status.Headroom, usage.Name)
					rows = append(rows, []string{
						budgetObj.ObjectMeta.Name,
						usage.Name,
						usage.Flavor,
						fmt.Sprintf("%d", usage.OwnedGPUs),
						fmt.Sprintf("%d", usage.SharedGPUs),
						fmt.Sprintf("%d", usage.BorrowedGPUs),
						fmt.Sprintf("%d", usage.UnfundedGPUs),
						fmt.Sprintf("%d", head.Concurrency),
					})
					raw = append(raw, map[string]interface{}{
						"budget":   budgetObj.ObjectMeta.Name,
						"envelope": usage.Name,
						"flavor":   usage.Flavor,
						"owned":    usage.OwnedGPUs,
						"shared":   usage.SharedGPUs,
						"borrowed": usage.BorrowedGPUs,
						"unfunded": usage.UnfundedGPUs,
						"spare":    usage.SpareGPUs,
						"headroom": head.Concurrency,
					})
				}
			}
			payload := Payload{
				Headers: []string{"Budget", "Envelope", "Flavor", "Owned", "Shared", "Borrowed", "Unfunded", "Headroom"},
				Rows:    rows,
				Raw:     raw,
				Title:   "Budget Usage",
			}
			return printer.Print(cmd, opts, payload)
		},
	}
	return cmd
}

func findHeadroom(heads []v1.EnvelopeHeadroom, name string) v1.EnvelopeHeadroom {
	for _, h := range heads {
		if h.Name == name {
			return h
		}
	}
	return v1.EnvelopeHeadroom{}
}
