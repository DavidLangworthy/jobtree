package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidlangworthy/jobtree/cmd/kubectl-runs/cmd"
)

// R11: every failure must print a one-line actionable error to stderr and
// exit non-zero. The CLI used to exit 1 with no output at all.
//
// This exercises --local: explain talks to a live cluster by default now
// (fake-features-audit.md #4), and the simulator is only reachable via
// --local, which also announces itself on stderr (LocalSimulatorNotice) so
// the simulator can never be mistaken for a live cluster.
func TestRunPrintsErrorsToStderr(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "cluster-state.json")
	stderr := &bytes.Buffer{}

	code := run([]string{"--local", "--state", statePath, "explain", "missing-run"}, stderr)

	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	want := cmd.LocalSimulatorNotice + "\n" + "error: run default/missing-run not found\n"
	if stderr.String() != want {
		t.Fatalf("expected stderr %q, got %q", want, stderr.String())
	}
}

func TestRunSucceedsSilentlyOnStderr(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "cluster-state.json")
	stderr := &bytes.Buffer{}

	code := run([]string{"--state", statePath, "completions", "bash"}, stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "error") {
		t.Fatalf("expected clean stderr, got %q", stderr.String())
	}
}
