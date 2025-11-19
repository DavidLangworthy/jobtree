package cmd

import (
    cobra "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/internal/cobra"
)

// RootOptions captures configuration shared across subcommands.
type RootOptions struct {
    StatePath string
    Namespace string
    Output    string
    WatchInterval int
    WatchCount    int
}

// NewRootCommand constructs the root CLI command.
func NewRootCommand() *cobra.Command {
    opts := &RootOptions{}
    root := &cobra.Command{
        Use:   "kubectl-runs",
        Short: "kubectl plugin for interacting with jobtree runs",
        PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
            if opts.StatePath == "" {
                opts.StatePath = "cluster-state.yaml"
            }
            if opts.Output == "" {
                opts.Output = "table"
            }
            if opts.Namespace == "" {
                opts.Namespace = "default"
            }
            if opts.WatchInterval <= 0 {
                opts.WatchInterval = 2
            }
            if opts.WatchCount < 0 {
                opts.WatchCount = 0
            }
            return nil
        },
        SilenceUsage:  true,
        SilenceErrors: true,
    }

    root.PersistentFlags().StringVar(&opts.StatePath, "state", "cluster-state.yaml", "Path to the local cluster state snapshot")
    root.PersistentFlags().StringVar(&opts.Namespace, "namespace", "default", "Namespace to use for Run operations")
    root.PersistentFlags().StringVar(&opts.Output, "output", "table", "Output format: table|json")
    root.PersistentFlags().IntVar(&opts.WatchInterval, "watch-interval", 2, "Watch refresh interval in seconds")
    root.PersistentFlags().IntVar(&opts.WatchCount, "watch-count", 0, "Number of watch iterations (0 = infinite)")

    store := &StateStore{}
    printer := &Printer{}

    root.AddCommand(NewSubmitCommand(opts, store, printer))
    root.AddCommand(NewPlanCommand(opts, store, printer))
    root.AddCommand(NewWatchCommand(opts, store, printer))
    root.AddCommand(NewExplainCommand(opts, store, printer))
    root.AddCommand(NewBudgetsCommand(opts, store, printer))
    root.AddCommand(NewSponsorsCommand(opts, store, printer))
    root.AddCommand(NewShrinkCommand(opts, store, printer))
    root.AddCommand(NewLeasesCommand(opts, store, printer))
    root.AddCommand(NewCompletionsCommand(opts, printer))

    return root
}
