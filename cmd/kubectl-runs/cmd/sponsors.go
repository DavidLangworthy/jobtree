package cmd

import (
	"fmt"
	"sort"
	"strconv"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
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
			var run *v1.Run
			if opts.UseLocal() {
				state, err := store.Load(opts.StatePath)
				if err != nil {
					return err
				}
				if err := ensureRunExists(state, opts.Namespace, name); err != nil {
					return err
				}
				run = state.Runs[keys.NamespacedKey(opts.Namespace, name)]
			} else {
				c, err := opts.LiveClient()
				if err != nil {
					return err
				}
				run, err = liveGetRun(cmd.Context(), c, opts.Namespace, name)
				if err != nil {
					return err
				}
			}
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
			if opts.UseLocal() {
				return sponsorsAddLocal(cmd, opts, store, printer, name, sponsor, maxBorrow)
			}
			return sponsorsAddLive(cmd, opts, printer, name, sponsor, maxBorrow)
		},
	}
	cmd.Flags().IntVar(&maxBorrow, "max", 0, "Optional max borrowed GPUs override")
	return cmd
}

func applySponsor(run *v1.Run, sponsor string, maxBorrow int) error {
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
	return nil
}

func sponsorsAddLocal(cmd *cobra.Command, opts *RootOptions, store *StateStore, printer *Printer, name, sponsor string, maxBorrow int) error {
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
	if err := applySponsor(run, sponsor, maxBorrow); err != nil {
		return err
	}
	if err := reconcileRun(state, opts.Namespace, name); err != nil {
		return err
	}
	if err := store.Save(opts.StatePath, state); err != nil {
		return err
	}
	return printer.Print(cmd, opts, sponsorAddPayload(run, sponsor, "Updated Sponsors"))
}

func sponsorsAddLive(cmd *cobra.Command, opts *RootOptions, printer *Printer, name, sponsor string, maxBorrow int) error {
	c, err := opts.LiveClient()
	if err != nil {
		return err
	}
	run, err := liveMutateRun(cmd.Context(), c, opts.Namespace, name, func(run *v1.Run) error {
		return applySponsor(run, sponsor, maxBorrow)
	})
	if err != nil {
		return err
	}
	return printer.Print(cmd, opts, sponsorAddPayload(run, sponsor, "Updated Sponsors (manager applies it on its next reconcile)"))
}

func sponsorAddPayload(run *v1.Run, sponsor, title string) Payload {
	rows := [][]string{{"added", sponsor}}
	if run.Spec.Funding.MaxBorrowGPUs != nil {
		rows = append(rows, []string{"maxBorrow", strconv.Itoa(int(*run.Spec.Funding.MaxBorrowGPUs))})
	}
	return Payload{
		Headers: []string{"Key", "Value"},
		Rows:    rows,
		Raw: map[string]interface{}{
			"funding": run.Spec.Funding,
		},
		Title: title,
	}
}
