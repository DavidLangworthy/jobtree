package main

import (
    "os"

    "github.com/davidlangworthy/jobtree/cmd/kubectl-runs/cmd"
)

func main() {
    root := cmd.NewRootCommand()
    if err := root.Execute(); err != nil {
        os.Exit(1)
    }
}
