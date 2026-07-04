package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// NewCompletionsCommand generates shell completions directly from the real
// Cobra command tree (GenBashCompletion/GenZshCompletion/GenFishCompletion)
// instead of a hand-written, silently-rotting map literal: adding, renaming,
// or removing a subcommand is reflected automatically because the generator
// walks cmd.Root() at run time.
func NewCompletionsCommand(opts *RootOptions, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completions SHELL",
		Short: "Generate shell completions (bash|zsh|fish) from the real command tree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := strings.ToLower(args[0])
			root := cmd.Root()
			out := cmd.OutOrStdout()
			switch shell {
			case "bash":
				return root.GenBashCompletion(out)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			default:
				return fmt.Errorf("unsupported shell %q", shell)
			}
		},
	}
	return cmd
}
