package main

import (
	"fmt"
	"io"
	"os"

	"github.com/davidlangworthy/jobtree/cmd/kubectl-runs/cmd"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	root := cmd.NewRootCommand()
	root.SetArgs(args)
	// Route cobra's own stderr writes (e.g. the --local simulator notice
	// PersistentPreRunE prints) through the same writer the caller gave us,
	// so callers observe everything a real invocation would print to
	// stderr — not just the final error line.
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
