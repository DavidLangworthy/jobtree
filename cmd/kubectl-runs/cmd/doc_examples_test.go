package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R12: every command line shown in docs/cli/kubectl-runs.md must actually
// run. The examples are extracted from the doc's fenced code blocks, so a
// doc edit that documents an invocation the parser rejects fails this test.
// (Only the --state and --file values are rewritten to test fixtures.)
func TestDocumentedExamplesRun(t *testing.T) {
	statePath := seedDocExampleState(t)
	manifestPath := writeDocExampleManifest(t)

	examples := extractDocExamples(t)
	if len(examples) == 0 {
		t.Fatalf("no kubectl runs examples found in the CLI doc")
	}

	for _, example := range examples {
		args := rewriteFixturePaths(example, statePath, manifestPath)
		root := NewRootCommand()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Errorf("documented example %q failed: %v", strings.Join(example, " "), err)
		}
	}
}

func extractDocExamples(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "cli", "kubectl-runs.md"))
	if err != nil {
		t.Fatalf("read CLI doc: %v", err)
	}
	var examples [][]string
	inFence := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence || !strings.HasPrefix(trimmed, "kubectl runs ") {
			continue
		}
		examples = append(examples, strings.Fields(strings.TrimPrefix(trimmed, "kubectl runs ")))
	}
	return examples
}

func rewriteFixturePaths(example []string, statePath, manifestPath string) []string {
	args := append([]string(nil), example...)
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--state":
			args[i+1] = statePath
		case "--file":
			args[i+1] = manifestPath
		}
	}
	return args
}

func seedDocExampleState(t *testing.T) string {
	t.Helper()
	// Reuse the fixture from state_safety_test.go, which has capacity and an
	// envelope for org:team-a in us-west/gpu-a.
	return seedStateFile(t)
}

func writeDocExampleManifest(t *testing.T) string {
	t.Helper()
	manifest := `{
  "apiVersion": "rq.davidlangworthy.io/v1",
  "kind": "Run",
  "metadata": {"name": "train-128"},
  "spec": {
    "owner": "org:team-a",
    "resources": {"gpuType": "H100-80GB", "totalGPUs": 4}
  }
}`
	path := filepath.Join(t.TempDir(), "run-128-groups.json")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}
