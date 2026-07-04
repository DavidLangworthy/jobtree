package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LocalSimulatorNotice is printed to stderr on every invocation that runs
// against the in-process simulator, so the simulator always announces
// itself as a simulator rather than silently standing in for a live
// cluster (fake-features-audit.md finding #4).
const LocalSimulatorNotice = "note: --local uses the in-process cluster-state.json simulator; no live cluster is contacted"

// RootOptions captures configuration shared across subcommands.
type RootOptions struct {
	StatePath     string
	Namespace     string
	Output        string
	WatchInterval int
	WatchCount    int

	// Kubeconfig/KubeContext select the live cluster. They are ignored in
	// --local/--dry-run mode.
	Kubeconfig  string
	KubeContext string
	// Local and DryRun are synonyms: either one selects the in-process
	// cluster-state.json simulator instead of a live cluster. The live
	// cluster is the default (fake-features-audit.md finding #4: the CLI
	// used to be a simulator with no opt-out; now the simulator is the
	// explicit opt-in).
	Local  bool
	DryRun bool

	namespaceSet bool

	liveClientBuilt bool
	liveClientVal   client.Client
	liveClientErr   error
}

// UseLocal reports whether this invocation should use the in-process
// cluster-state.json simulator instead of a live cluster.
func (o *RootOptions) UseLocal() bool {
	return o.Local || o.DryRun
}

// LiveClient lazily builds (and memoizes) the real controller-runtime client
// for this invocation. Building it lazily means commands that never touch a
// cluster (e.g. `completions`) don't need a reachable kubeconfig at all, and
// commands run under --local never build one.
func (o *RootOptions) LiveClient() (client.Client, error) {
	if !o.liveClientBuilt {
		o.liveClientVal, o.liveClientErr = newLiveClient(o.Kubeconfig, o.KubeContext)
		o.liveClientBuilt = true
	}
	return o.liveClientVal, o.liveClientErr
}

// NewRootCommand constructs the root CLI command.
func NewRootCommand() *cobra.Command {
	opts := &RootOptions{}
	root := &cobra.Command{
		Use:   "kubectl-runs",
		Short: "kubectl plugin for interacting with jobtree Runs, Budgets, Reservations, and Leases",
		Long: `kubectl-runs talks to a live Kubernetes API server by default, using the same
kubeconfig/context resolution as kubectl (override with --kubeconfig/--context).

Pass --local (or its synonym --dry-run) to instead drive the in-process
cluster-state.json simulator — useful for docs, demos, and offline
experimentation, but it is not a cluster: no kube-apiserver is contacted, and
no manager reconciles anything you submit to it.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			opts.namespaceSet = cmd.Flags().Changed("namespace")
			if opts.StatePath == "" {
				opts.StatePath = "cluster-state.json"
			}
			if opts.Output == "" {
				opts.Output = "table"
			}
			if opts.WatchInterval <= 0 {
				opts.WatchInterval = 2
			}
			if opts.WatchCount < 0 {
				opts.WatchCount = 0
			}
			if !opts.namespaceSet {
				if opts.UseLocal() {
					opts.Namespace = "default"
				} else if ns, ok := resolveContextNamespace(opts.Kubeconfig, opts.KubeContext); ok {
					opts.Namespace = ns
				} else {
					opts.Namespace = "default"
				}
			}
			if opts.UseLocal() {
				fmt.Fprintln(cmd.ErrOrStderr(), LocalSimulatorNotice)
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&opts.StatePath, "state", "cluster-state.json", "Path to the local cluster state snapshot (--local/--dry-run only)")
	root.PersistentFlags().StringVar(&opts.Namespace, "namespace", "default", "Namespace to use for Run operations (live mode default: current kubeconfig context's namespace)")
	root.PersistentFlags().StringVar(&opts.Output, "output", "table", "Output format: table|json")
	root.PersistentFlags().IntVar(&opts.WatchInterval, "watch-interval", 2, "Watch refresh interval in seconds")
	root.PersistentFlags().IntVar(&opts.WatchCount, "watch-count", 0, "Number of watch iterations (0 = infinite)")
	root.PersistentFlags().StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to a kubeconfig file (default: standard kubeconfig discovery, like kubectl)")
	root.PersistentFlags().StringVar(&opts.KubeContext, "context", "", "kubeconfig context to use (live mode only)")
	root.PersistentFlags().BoolVar(&opts.Local, "local", false, "Use the in-process cluster-state.json simulator instead of a live cluster (this is NOT a real cluster)")
	root.PersistentFlags().BoolVar(&opts.DryRun, "dry-run", false, "Synonym for --local")

	store := &StateStore{}
	printer := &Printer{}

	root.AddCommand(NewSubmitCommand(opts, store, printer))
	root.AddCommand(NewPlanCommand(opts, store, printer))
	root.AddCommand(NewWatchCommand(opts, store, printer))
	root.AddCommand(NewExplainCommand(opts, store, printer))
	root.AddCommand(NewBudgetsCommand(opts, store, printer))
	root.AddCommand(NewSponsorsCommand(opts, store, printer))
	root.AddCommand(NewShrinkCommand(opts, store, printer))
	root.AddCommand(NewCompleteCommand(opts, store, printer))
	root.AddCommand(NewEtaCommand(opts, store, printer))
	root.AddCommand(NewLeasesCommand(opts, store, printer))
	root.AddCommand(NewCompletionsCommand(opts, printer))

	return root
}
