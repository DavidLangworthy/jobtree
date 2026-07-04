package cmd

import (
	"fmt"
	"os"
	"strings"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
	sigsyaml "sigs.k8s.io/yaml"
)

// NewSubmitCommand wires the submit subcommand.
func NewSubmitCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	var file string
	var follow []string
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a Run manifest (YAML or JSON)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			payload, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read manifest: %w", err)
			}
			trimmed := strings.TrimSpace(string(payload))
			if trimmed == "" {
				return fmt.Errorf("manifest is empty")
			}
			// sigs.k8s.io/yaml decodes both YAML and JSON (JSON is a subset
			// of YAML): converts to JSON internally, then uses encoding/json
			// semantics, so struct tags behave identically either way.
			var run v1.Run
			if err := sigsyaml.Unmarshal([]byte(trimmed), &run); err != nil {
				return fmt.Errorf("decode manifest (must be YAML or JSON): %w", err)
			}
			if run.Namespace == "" {
				run.Namespace = opts.Namespace
			}
			if run.Name == "" {
				return fmt.Errorf("metadata.name is required")
			}
			if len(follow) > 0 {
				if run.Spec.Follow == nil {
					run.Spec.Follow = &v1.RunFollow{}
				}
				run.Spec.Follow.After = append(run.Spec.Follow.After, follow...)
			}
			run.Default()
			if err := run.ValidateCreate(); err != nil {
				return err
			}

			if opts.UseLocal() {
				return submitLocal(cmd, opts, store, printer, &run)
			}
			return submitLive(cmd, opts, printer, &run)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Path to a Run manifest (YAML or JSON)")
	cmd.Flags().StringSliceVar(&follow, "follow", nil, "Run name(s) this run must wait to complete before starting (repeatable)")
	return cmd
}

func submitLocal(cmd *cobra.Command, opts *RootOptions, store *StateStore, printer *Printer, run *v1.Run) error {
	unlock, err := store.Lock(opts.StatePath)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := store.Load(opts.StatePath)
	if err != nil {
		return err
	}
	key := keys.NamespacedKey(run.Namespace, run.Name)
	copy := *run
	state.Runs[key] = &copy
	if err := reconcileRun(state, run.Namespace, run.Name); err != nil {
		return err
	}
	if err := store.Save(opts.StatePath, state); err != nil {
		return err
	}

	summary := Payload{
		Headers: []string{"Run", "Phase", "Message"},
		Rows:    [][]string{{key, state.Runs[key].Status.Phase, state.Runs[key].Status.Message}},
		Raw: map[string]interface{}{
			"run": state.Runs[key],
		},
	}
	return printer.Print(cmd, opts, summary)
}

func submitLive(cmd *cobra.Command, opts *RootOptions, printer *Printer, run *v1.Run) error {
	c, err := opts.LiveClient()
	if err != nil {
		return err
	}
	created, err := liveSubmitRun(cmd.Context(), c, run)
	if err != nil {
		return err
	}
	key := keys.NamespacedKey(created.Namespace, created.Name)
	summary := Payload{
		Headers: []string{"Run", "Phase", "Message"},
		Rows:    [][]string{{key, created.Status.Phase, created.Status.Message}},
		Raw: map[string]interface{}{
			"run": created,
		},
		Title: "Run submitted (status populates once the manager reconciles; use `kubectl runs watch` to follow)",
	}
	return printer.Print(cmd, opts, summary)
}
