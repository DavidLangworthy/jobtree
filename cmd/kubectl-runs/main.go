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
	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}
