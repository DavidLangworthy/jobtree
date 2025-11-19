package cmd

import (
	"fmt"
	"strings"

	cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
)

// NewCompletionsCommand generates shell completions for the plugin.
func NewCompletionsCommand(opts *RootOptions, printer *Printer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completions SHELL",
		Short: "Generate shell completions (bash|zsh|fish)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := strings.ToLower(args[0])
			script, ok := completionScripts[shell]
			if !ok {
				return fmt.Errorf("unsupported shell %q", shell)
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), script); err != nil {
				return fmt.Errorf("write completion script: %w", err)
			}
			return nil
		},
	}
	return cmd
}

var completionScripts = map[string]string{
	"bash": `# bash completion for kubectl-runs
_kubectl_runs_complete() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local cmds="submit plan watch explain budgets sponsors shrink leases completions"
    COMPREPLY=( $(compgen -W "${cmds}" -- "${cur}") )
    return 0
}
complete -F _kubectl_runs_complete kubectl-runs
`,
	"zsh": `#compdef kubectl-runs
_arguments "*::command:->command"
case $state in
  command)
    _values 'kubectl-runs commands' submit plan watch explain budgets sponsors shrink leases completions
  ;;
esac
`,
	"fish": `function __kubectl_runs_complete
    set -l options submit plan watch explain budgets sponsors shrink leases completions
    for opt in $options
        if string match -q "$argv" "$opt"
            echo $opt
        end
    end
end
complete -c kubectl-runs -f -a "(__kubectl_runs_complete (commandline -ct))"
`,
}
