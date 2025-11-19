package cmd

import (
	"fmt"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
	"github.com/davidlangworthy/jobtree/controllers"
	budgetpkg "github.com/davidlangworthy/jobtree/pkg/budget"
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
		Short: "Show budget usage and remaining headroom",
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
			for i := range state.Budgets {
				budgetObj := state.Budgets[i]
				copy := budgetObj
				controller := controllers.NewBudgetController(controllers.RealClock{}, controllers.NewBudgetMetrics())
				status := controller.ReconcileBudget(&copy, state.Leases)
				usageState := budgetpkg.BuildBudgetState(&copy, state.Leases, now)
				for _, env := range usageState.Envelopes {
					head := findHeadroom(status.Headroom, env.Spec.Name)
					rows = append(rows, []string{
						budgetObj.ObjectMeta.Name,
						env.Spec.Name,
						env.Spec.Flavor,
						fmt.Sprintf("%d", env.Usage.Concurrency),
						fmt.Sprintf("%d", env.Usage.BorrowedConcurrency),
						fmt.Sprintf("%d", head.Concurrency),
					})
					raw = append(raw, map[string]interface{}{
						"budget":      budgetObj.ObjectMeta.Name,
						"envelope":    env.Spec.Name,
						"flavor":      env.Spec.Flavor,
						"concurrency": env.Usage.Concurrency,
						"borrowed":    env.Usage.BorrowedConcurrency,
						"headroom":    head.Concurrency,
					})
				}
			}
			payload := Payload{
				Headers: []string{"Budget", "Envelope", "Flavor", "InUse", "Borrowed", "Headroom"},
				Rows:    rows,
				Raw:     raw,
				Title:   "Budget Usage",
			}
			if err := store.Save(opts.StatePath, state); err != nil {
				return err
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
