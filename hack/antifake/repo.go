// Package antifake implements the anti-fake lint gates for jobtree (Track F
// — TESTINFRA of docs/project/make-it-real-plan.md). Both checks exist to
// make the exact failure pattern documented in docs/project/fake-features-
// audit.md §3 ("the pattern") mechanically hard to repeat:
//
//   - checkNoFakeTerminalPodPhase (terminalphase.go): no *_test.go may
//     hand-set a workload Pod's .Status.Phase to a terminal value — the
//     precise move that made Run/gang completion look real in
//     controllers/kube/scenario_test.go while every pod was still a
//     pause-container mannequin (audit finding #5).
//
//   - checkCRDFieldsHaveReaders (crdfields.go): no api/v1 CRD spec/status
//     field may ship schema-validated and deep-copied while zero production
//     code outside api/v1 ever reads (or writes) it — the "accepted-but-
//     unread" pattern behind audit findings #10/#18 (Runtime.Checkpoint) and
//     #22 (Budget.AutoRenew).
//
// Both are wired as `go test` targets (`make antifake`) rather than shell
// greps so a single `go build`-checked Go AST walk is the source of truth,
// per the "a Go check (a go test or a small go/analysis-based analyzer)"
// instruction. Both use a shrink-only ratcheted allowlist: a hard-coded
// constant caps how many documented exceptions may exist, so a new one can
// only be added by a deliberate code change to the checker itself, and a
// fixed exception must be removed from the allowlist file or the check fails
// on the resulting staleness.
package antifake

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// repoRoot returns the absolute path to the repository root, derived from
// this source file's own compile-time location rather than the process's
// working directory — `go test` always runs with cwd set to the package
// directory (hack/antifake), so relying on cwd would be fragile if that ever
// changes (e.g. -run from the repo root with a full package path).
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrNotExist
	}
	// file is .../hack/antifake/repo.go; the repo root is two levels up.
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		return "", err
	}
	return root, nil
}

// skipDir reports whether the given directory (relative to the repo root,
// slash-separated, no leading/trailing slash) should be excluded entirely
// from both checks' repo walks.
func skipDir(rel string) bool {
	switch {
	case rel == ".git", rel == "vendor":
		return true
	case rel == ".claude" || strings.HasPrefix(rel, ".claude/"):
		// Nested git worktrees (e.g. parallel-agent scratch under
		// .claude/worktrees/) are separate checkouts of this same repo. Walking
		// into them double-counts every file — and would flag their in-progress
		// copies (mid-wired fields, un-annotated fixtures) as fakes — so the
		// scan must stay within the current checkout.
		return true
	case rel == "hack/antifake":
		// The checker's own package: it necessarily mentions the field/
		// pattern names it looks for (allowlists, doc comments, its own
		// fixtures), which would otherwise make every seeded exception look
		// "read" or "safe" by matching against the checker's own source.
		return true
	case rel == "specs" || filepath.Base(rel) == ".cache":
		return true
	default:
		return false
	}
}
