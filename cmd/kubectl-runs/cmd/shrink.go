package cmd

import (
	"fmt"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// NewShrinkCommand enables voluntary shrink for elastic runs.
func NewShrinkCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	var by int
	cmd := &cobra.Command{
		Use:   "shrink RUN",
		Short: "Request a voluntary shrink for an elastic Run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if by <= 0 {
				return fmt.Errorf("--by must be > 0")
			}
			name := args[0]
			if opts.UseLocal() {
				return shrinkLocal(cmd, opts, store, printer, name, by)
			}
			return shrinkLive(cmd, opts, printer, name, by)
		},
	}
	cmd.Flags().IntVar(&by, "by", 0, "Number of GPUs to remove from desired width")
	return cmd
}

func applyShrink(run *v1.Run, by int) (int32, error) {
	if run.Spec.Malleable == nil {
		return 0, errNoMalleability
	}
	target := run.Spec.Malleable.Desired() - int32(by)
	clamped, err := clampDesired(run.Spec.Malleable.MinTotalGPUs, run.Spec.Malleable.MaxTotalGPUs, target)
	if err != nil {
		return 0, err
	}
	run.Spec.Malleable.DesiredTotalGPUs = &clamped
	return clamped, nil
}

func shrinkLocal(cmd *cobra.Command, opts *RootOptions, store *StateStore, printer *Printer, name string, by int) error {
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
	if err := ensureMalleable(state, opts.Namespace, name); err != nil {
		return err
	}
	key := keys.NamespacedKey(opts.Namespace, name)
	run := state.Runs[key]
	clamped, err := applyShrink(run, by)
	if err != nil {
		return err
	}
	if err := reconcileRun(state, opts.Namespace, name); err != nil {
		return err
	}
	if err := store.Save(opts.StatePath, state); err != nil {
		return err
	}
	payload := Payload{
		Headers: []string{"Run", "DesiredGPUs"},
		Rows:    [][]string{{key, fmt.Sprintf("%d", clamped)}},
		Raw: map[string]interface{}{
			"run": run,
		},
		Title: "Shrink Requested",
	}
	return printer.Print(cmd, opts, payload)
}

func shrinkLive(cmd *cobra.Command, opts *RootOptions, printer *Printer, name string, by int) error {
	c, err := opts.LiveClient()
	if err != nil {
		return err
	}
	var clamped int32
	run, err := liveMutateRun(cmd.Context(), c, opts.Namespace, name, func(run *v1.Run) error {
		var mutateErr error
		clamped, mutateErr = applyShrink(run, by)
		return mutateErr
	})
	if err != nil {
		return err
	}
	key := keys.NamespacedKey(opts.Namespace, name)
	payload := Payload{
		Headers: []string{"Run", "DesiredGPUs"},
		Rows:    [][]string{{key, fmt.Sprintf("%d", clamped)}},
		Raw: map[string]interface{}{
			"run": run,
		},
		Title: "Shrink Requested (manager applies it on its next reconcile)",
	}
	return printer.Print(cmd, opts, payload)
}
