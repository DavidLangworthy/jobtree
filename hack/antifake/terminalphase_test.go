package antifake

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestNoFakeTerminalPodPhase is the CI gate (`make antifake`): it fails the
// build if any *_test.go outside test/e2e/ hand-sets a workload Pod's
// .Status.Phase to a terminal value without going through the shrink-only
// ratcheted allowlist AND the matching inline `antifake:allow-terminal-
// phase` source annotation. See terminalphase.go for the detection rule and
// docs/project/fake-features-audit.md finding #5 / docs/project/make-it-
// real-plan.md Track F for why this pattern is the single most effective
// camouflage the audit found.
func TestNoFakeTerminalPodPhase(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}

	findings, err := scanTerminalPodPhase(root)
	if err != nil {
		t.Fatalf("scanTerminalPodPhase: %v", err)
	}

	allowlistPath := filepath.Join(root, "hack", "antifake", terminalPhaseAllowlistName)
	allowlist, err := loadTerminalPhaseAllowlist(allowlistPath)
	if err != nil {
		t.Fatalf("loadTerminalPhaseAllowlist: %v", err)
	}
	if len(allowlist) > maxAllowedTerminalPhaseExceptions {
		t.Fatalf("%s lists %d exceptions, exceeding the shrink-only ratchet of %d; "+
			"bump maxAllowedTerminalPhaseExceptions in terminalphase.go only alongside "+
			"a new, individually-reasoned exception — never just to make this pass",
			terminalPhaseAllowlistName, len(allowlist), maxAllowedTerminalPhaseExceptions)
	}

	var newFakes []string
	var driftedAnnotations []string
	seenAllowlistKeys := map[string]bool{}

	for _, f := range findings {
		key := f.key()
		reason, allowed := allowlist[key]
		line, lerr := sourceLine(root, f.RelPath, f.Line)
		if lerr != nil {
			t.Fatalf("read %s:%d: %v", f.RelPath, f.Line, lerr)
		}
		annotated := strings.Contains(line, antifakeAllowAnnotation)

		switch {
		case allowed && annotated:
			seenAllowlistKeys[key] = true
		case allowed && !annotated:
			driftedAnnotations = append(driftedAnnotations, fmt.Sprintf(
				"%s is in %s (reason: %s) but its source line lacks the %q comment — "+
					"add the annotation so the exception is documented at the call site, not just in the allowlist file",
				key, terminalPhaseAllowlistName, reason, antifakeAllowAnnotation))
		case !allowed && annotated:
			driftedAnnotations = append(driftedAnnotations, fmt.Sprintf(
				"%s carries the %q annotation but is not listed in %s — add it there with a reason, or remove the annotation",
				key, antifakeAllowAnnotation, terminalPhaseAllowlistName))
		default:
			newFakes = append(newFakes, fmt.Sprintf(
				"%s: %s\n    hand-sets a terminal Pod phase — derive Succeeded/Failed from a real "+
					"kubelet (see test/e2e/) instead of injecting it. If this is a genuinely new, "+
					"deliberately-accepted interim exception, add it to %s AND annotate the line with "+
					"%q, and bump maxAllowedTerminalPhaseExceptions in terminalphase.go",
				key, strings.TrimSpace(line), terminalPhaseAllowlistName, antifakeAllowAnnotation))
		}
	}

	var stale []string
	for key, reason := range allowlist {
		if !seenAllowlistKeys[key] {
			stale = append(stale, fmt.Sprintf("%s (reason: %s) no longer matches a hand-set terminal Pod phase — remove it from %s (shrink-only ratchet: fixed exceptions must be deleted, not left behind)", key, reason, terminalPhaseAllowlistName))
		}
	}

	sort.Strings(newFakes)
	sort.Strings(driftedAnnotations)
	sort.Strings(stale)

	if len(newFakes) > 0 || len(driftedAnnotations) > 0 || len(stale) > 0 {
		var b strings.Builder
		for _, m := range newFakes {
			b.WriteString("NEW FAKE: ")
			b.WriteString(m)
			b.WriteString("\n")
		}
		for _, m := range driftedAnnotations {
			b.WriteString("DRIFT: ")
			b.WriteString(m)
			b.WriteString("\n")
		}
		for _, m := range stale {
			b.WriteString("STALE ALLOWLIST ENTRY: ")
			b.WriteString(m)
			b.WriteString("\n")
		}
		t.Fatal(b.String())
	}
}

// terminalAssignmentCount walks a parsed file and counts matches of the
// terminal-pod-phase assignment rule, for use by the heuristic unit test
// below (which doesn't want the file-walking/allowlist machinery, just the
// AST predicate).
func terminalAssignmentCount(file *ast.File, corev1Alias string) int {
	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.ASSIGN {
			return true
		}
		if len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for i, lhs := range assign.Lhs {
			if isTerminalPodPhaseAssignment(lhs, assign.Rhs[i], corev1Alias) {
				count++
			}
		}
		return true
	})
	return count
}

// TestScanTerminalPodPhase_Heuristic unit-tests the detection rule in
// isolation against small in-memory source snippets (not real repo files),
// so its precision/recall is pinned independent of scenario_test.go's
// current contents.
func TestScanTerminalPodPhase_Heuristic(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "typed corev1 constant on indexed pod slice",
			src: `package x
import corev1 "k8s.io/api/core/v1"
func f(pods []corev1.Pod) {
	for i := range pods {
		pods[i].Status.Phase = corev1.PodSucceeded
	}
}`,
			want: true,
		},
		{
			name: "typed corev1 constant, PodFailed, plain v1 alias",
			src: `package x
import v1 "k8s.io/api/core/v1"
func f(pod *v1.Pod) {
	pod.Status.Phase = v1.PodFailed
}`,
			want: true,
		},
		{
			name: "bare string literal onto a pod-named variable",
			src: `package x
func g() {
	var pod struct{ Status struct{ Phase string } }
	pod.Status.Phase = "Succeeded"
}`,
			want: true,
		},
		{
			name: "Run phase assigned via named identifier constant — not flagged",
			src: `package x
const RunPhaseComplete = "Completed"
func f() {
	var up struct{ Status struct{ Phase string } }
	up.Status.Phase = RunPhaseComplete
}`,
			want: false,
		},
		{
			name: "comparison, not assignment — not flagged",
			src: `package x
import corev1 "k8s.io/api/core/v1"
func f(pod corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded
}`,
			want: false,
		},
		{
			name: "bare string literal onto a non-pod-named variable — not flagged",
			src: `package x
func f() {
	var run struct{ Status struct{ Phase string } }
	run.Status.Phase = "Succeeded"
}`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "snippet.go", tc.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			corev1Alias := corev1ImportAlias(file)
			got := terminalAssignmentCount(file, corev1Alias) > 0
			if got != tc.want {
				t.Errorf("%s: detected=%v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
