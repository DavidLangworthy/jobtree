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
		Short: "Show budget usage by funding class and remaining headroom (read-only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.UseLocal() {
				return budgetsUsageLocal(cmd, opts, store, printer)
			}
			return budgetsUsageLive(cmd, opts, printer)
		},
	}
	return cmd
}

func budgetsUsageLocal(cmd *cobra.Command, opts *RootOptions, store *StateStore, printer *Printer) error {
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
		r, a := budgetUsageRows(budgetObj.ObjectMeta.Name, status.Usage, status.Headroom)
		rows = append(rows, r...)
		raw = append(raw, a...)
	}
	payload := Payload{
		Headers: []string{"Budget", "Envelope", "Flavor", "Owned", "Shared", "Borrowed", "Unfunded", "Headroom"},
		Rows:    rows,
		Raw:     raw,
		Title:   "Budget Usage",
	}
	return printer.Print(cmd, opts, payload)
}

// budgetsUsageLive lists Budgets from the API server and prints exactly the
// Status.Usage/Status.Headroom the manager's own BudgetReconciler already
// computed and wrote — it never re-runs pkg/funding client-side, so the
// numbers can lag the manager's next reconcile (documented in
// docs/cli/kubectl-runs.md).
func budgetsUsageLive(cmd *cobra.Command, opts *RootOptions, printer *Printer) error {
	c, err := opts.LiveClient()
	if err != nil {
		return err
	}
	budgets, err := liveListBudgets(cmd.Context(), c, opts.Namespace)
	if err != nil {
		return err
	}
	rows := [][]string{}
	raw := make([]map[string]interface{}, 0)
	for _, budgetObj := range budgets {
		r, a := budgetUsageRows(budgetObj.ObjectMeta.Name, budgetObj.Status.Usage, budgetObj.Status.Headroom)
		rows = append(rows, r...)
		raw = append(raw, a...)
	}
	payload := Payload{
		Headers: []string{"Budget", "Envelope", "Flavor", "Owned", "Shared", "Borrowed", "Unfunded", "Headroom"},
		Rows:    rows,
		Raw:     raw,
		Title:   "Budget Usage (as last reconciled by the manager)",
	}
	return printer.Print(cmd, opts, payload)
}

func budgetUsageRows(budgetName string, usages []v1.EnvelopeUsage, headroom []v1.EnvelopeHeadroom) ([][]string, []map[string]interface{}) {
	rows := make([][]string, 0, len(usages))
	raw := make([]map[string]interface{}, 0, len(usages))
	for _, usage := range usages {
		head := findHeadroom(headroom, usage.Name)
		rows = append(rows, []string{
			budgetName,
			usage.Name,
			usage.Flavor,
			fmt.Sprintf("%d", usage.OwnedGPUs),
			fmt.Sprintf("%d", usage.SharedGPUs),
			fmt.Sprintf("%d", usage.BorrowedGPUs),
			fmt.Sprintf("%d", usage.UnfundedGPUs),
			fmt.Sprintf("%d", head.Concurrency),
		})
		raw = append(raw, map[string]interface{}{
			"budget":   budgetName,
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
	return rows, raw
}

func findHeadroom(heads []v1.EnvelopeHeadroom, name string) v1.EnvelopeHeadroom {
	for _, h := range heads {
		if h.Name == name {
			return h
		}
	}
	return v1.EnvelopeHeadroom{}
}
