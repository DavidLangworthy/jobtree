package cmd

import (
	"fmt"
	"sort"
	"strconv"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
)

// NewSponsorsCommand manages sponsor configuration for a Run.
func NewSponsorsCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sponsors",
		Short: "Manage borrowing sponsors for a Run",
	}
	cmd.AddCommand(newSponsorsListCommand(opts, store, printer))
	cmd.AddCommand(newSponsorsAddCommand(opts, store, printer))
	return cmd
}

func newSponsorsListCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list RUN",
		Short: "List sponsors configured for a Run",
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
			key := namespacedKey(opts.Namespace, name)
			run := state.Runs[key]
			var sponsors []string
			if run.Spec.Funding != nil {
				sponsors = copySlice(run.Spec.Funding.Sponsors)
			}
			sort.Strings(sponsors)
			rows := make([][]string, len(sponsors))
			for i, sponsor := range sponsors {
				rows[i] = []string{sponsor}
			}
			payload := Payload{
				Headers: []string{"Sponsor"},
				Rows:    rows,
				Raw: map[string]interface{}{
					"sponsors":    sponsors,
					"allowBorrow": run.Spec.Funding != nil && run.Spec.Funding.AllowBorrow,
				},
				Title: "Run Sponsors",
			}
			return printer.Print(cmd, opts, payload)
		},
	}
	return cmd
}

func newSponsorsAddCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	var maxBorrow int
	cmd := &cobra.Command{
		Use:   "add RUN SPONSOR",
		Short: "Allow a sponsor to fund additional GPUs for a Run",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			sponsor := args[1]
			state, err := store.Load(opts.StatePath)
			if err != nil {
				return err
			}
			if err := ensureRunExists(state, opts.Namespace, name); err != nil {
				return err
			}
			key := namespacedKey(opts.Namespace, name)
			run := state.Runs[key]
			if run.Spec.Funding == nil {
				run.Spec.Funding = &v1.RunFunding{AllowBorrow: true}
			}
			if !run.Spec.Funding.AllowBorrow {
				return fmt.Errorf("run does not allow borrowing; edit spec to enable it")
			}
			run.Spec.Funding.Sponsors = uniqueAppend(run.Spec.Funding.Sponsors, sponsor)
			if maxBorrow > 0 {
				value := int32(maxBorrow)
				run.Spec.Funding.MaxBorrowGPUs = &value
			}
			if err := reconcileRun(state, opts.Namespace, name); err != nil {
				return err
			}
			if err := store.Save(opts.StatePath, state); err != nil {
				return err
			}
			rows := [][]string{{"added", sponsor}}
			if run.Spec.Funding.MaxBorrowGPUs != nil {
				rows = append(rows, []string{"maxBorrow", strconv.Itoa(int(*run.Spec.Funding.MaxBorrowGPUs))})
			}
			payload := Payload{
				Headers: []string{"Key", "Value"},
				Rows:    rows,
				Raw: map[string]interface{}{
					"funding": run.Spec.Funding,
				},
				Title: "Updated Sponsors",
			}
			return printer.Print(cmd, opts, payload)
		},
	}
	cmd.Flags().IntVar(&maxBorrow, "max", 0, "Optional max borrowed GPUs override")
	return cmd
}
