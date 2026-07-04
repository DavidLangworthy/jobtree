package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/spf13/cobra"
)

// NewSubmitCommand wires the submit subcommand.
func NewSubmitCommand(opts *RootOptions, store *StateStore, printer *Printer) *cobra.Command {
	var file string
	var follow []string
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a Run manifest and trigger immediate reconciliation",
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
			var run v1.Run
			if trimmed[0] != '{' {
				return fmt.Errorf("manifest must be JSON for the local simulator (received non-JSON input)")
			}
			if err := json.Unmarshal([]byte(trimmed), &run); err != nil {
				return fmt.Errorf("decode manifest: %w", err)
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
			copy := run
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
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Path to a Run manifest")
	cmd.Flags().StringSliceVar(&follow, "follow", nil, "Run name(s) this run must wait to complete before starting (repeatable)")
	return cmd
}
