package antifake

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// maxAllowedTerminalPhaseExceptions is the shrink-only ratchet for
// terminalPhaseAllowlistPath. Raising it requires a deliberate edit to this
// file alongside a new, reasoned allowlist entry — it must never be bumped
// just to make CI pass.
//
// Currently seeded with exactly the two sites named in
// docs/project/fake-features-audit.md finding #5 and
// docs/project/make-it-real-plan.md Track F: controllers/kube/scenario_test.go
// hand-driving a pause pod to Succeeded because envtest has no kubelet. Both
// are meant to disappear once Track TESTINFRA-4 / JOBSET land a real
// container that a real kubelet can actually run to completion.
const maxAllowedTerminalPhaseExceptions = 2

const terminalPhaseAllowlistName = "terminal-phase-allowlist.txt"

// antifakeAllowAnnotation is the inline marker required, in addition to the
// allowlist file entry, on every hand-injected terminal Pod phase line that
// is accepted as a documented interim exception. Requiring both keeps the
// two in sync: an allowlist entry with no annotation (or vice versa) fails
// the check, so neither can silently drift from the other.
const antifakeAllowAnnotation = "antifake:allow-terminal-phase"

// terminalPhaseFinding is one assignment statement that hand-sets a Pod's
// .Status.Phase to a terminal value.
type terminalPhaseFinding struct {
	RelPath string // slash-separated, relative to repo root
	Line    int
}

func (f terminalPhaseFinding) key() string {
	return fmt.Sprintf("%s:%d", f.RelPath, f.Line)
}

// scanTerminalPodPhase walks every *_test.go file under root (skipping
// vendor/.git/hack-antifake/specs per skipDir, and test/e2e/ — that
// directory watches a real kubelet-written phase, it never assigns one)
// looking for the pattern audit finding #5 named: an assignment whose LHS
// selector chain ends `.Status.Phase` and whose RHS is a terminal Pod phase.
//
// Deliberately NOT flagged: `run.Status.Phase = RunPhaseComplete` and
// similar (controllers/follow_test.go, controllers/reservation_semantics_
// test.go) — those drive synthetic in-memory Run fixtures directly (never
// through envtest, no pod involved) to unit-test evaluateFollow/
// runGangComplete's own logic, which docs/project/make-it-real-plan.md Track
// C explicitly keeps ("a scoped unit test... given synthetic manifests, not
// 'scenario'"). Because RunPhase values are named Go identifiers (not
// corev1.Pod{Succeeded,Failed} constants, nor bare "Succeeded"/"Failed"
// string literals assigned onto something pod-shaped), the detection rule
// below does not match them at all.
func scanTerminalPodPhase(root string) ([]terminalPhaseFinding, error) {
	var findings []terminalPhaseFinding
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skipDir(rel) {
				return filepath.SkipDir
			}
			if rel == "test/e2e" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		file, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		corev1Alias := corev1ImportAlias(file)
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
					pos := fset.Position(lhs.Pos())
					findings = append(findings, terminalPhaseFinding{RelPath: rel, Line: pos.Line})
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].RelPath != findings[j].RelPath {
			return findings[i].RelPath < findings[j].RelPath
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, nil
}

// corev1ImportAlias returns the local identifier the file uses for
// "k8s.io/api/core/v1", or "" if the file does not import it.
func corev1ImportAlias(file *ast.File) string {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "k8s.io/api/core/v1" {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		// Unaliased: the package's own declared name is "v1".
		return "v1"
	}
	return ""
}

// isTerminalPodPhaseAssignment reports whether lhs is some `X.Status.Phase`
// selector chain and rhs is a terminal Pod phase: corev1.PodSucceeded /
// corev1.PodFailed, or the bare string "Succeeded"/"Failed" assigned onto
// something that looks like a Pod by identifier name (a defense-in-depth
// fallback for code that skips the typed constant).
func isTerminalPodPhaseAssignment(lhs, rhs ast.Expr, corev1Alias string) bool {
	phaseSel, ok := lhs.(*ast.SelectorExpr)
	if !ok || phaseSel.Sel.Name != "Phase" {
		return false
	}
	statusSel, ok := phaseSel.X.(*ast.SelectorExpr)
	if !ok || statusSel.Sel.Name != "Status" {
		return false
	}

	switch r := rhs.(type) {
	case *ast.SelectorExpr:
		ident, ok := r.X.(*ast.Ident)
		if !ok || corev1Alias == "" || ident.Name != corev1Alias {
			return false
		}
		return r.Sel.Name == "PodSucceeded" || r.Sel.Name == "PodFailed"
	case *ast.BasicLit:
		if r.Kind != token.STRING {
			return false
		}
		v, err := strconv.Unquote(r.Value)
		if err != nil {
			return false
		}
		if v != "Succeeded" && v != "Failed" {
			return false
		}
		return looksLikePod(statusSel.X)
	default:
		return false
	}
}

var podNameRE = regexp.MustCompile(`(?i)pod`)

// looksLikePod extracts a best-effort base identifier name from the
// expression a .Status.Phase selector hangs off of — e.g. `pods[i]` -> "pods",
// `pod` -> "pod", `x.pod` -> "pod" — and reports whether it looks
// Pod-shaped by name. This only gates the bare-string-literal fallback path;
// the typed corev1.PodSucceeded/PodFailed path above needs no name check.
func looksLikePod(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return podNameRE.MatchString(x.Name)
	case *ast.IndexExpr:
		return looksLikePod(x.X)
	case *ast.SelectorExpr:
		return podNameRE.MatchString(x.Sel.Name)
	case *ast.StarExpr:
		return looksLikePod(x.X)
	case *ast.ParenExpr:
		return looksLikePod(x.X)
	default:
		return false
	}
}

// terminalPhaseAllowlistEntry is one line of terminal-phase-allowlist.txt.
type terminalPhaseAllowlistEntry struct {
	Key    string // "relpath:line"
	Reason string
}

// loadTerminalPhaseAllowlist parses the allowlist file. Blank lines and
// lines starting with '#' are ignored; every other line must be
// `<relpath>:<line> <reason>`.
func loadTerminalPhaseAllowlist(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	entries := map[string]string{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 || !strings.Contains(fields[0], ":") {
			return nil, fmt.Errorf("%s:%d: malformed entry %q, want '<relpath>:<line> <reason>'", path, lineNo, line)
		}
		entries[fields[0]] = fields[1]
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// sourceLine returns the exact text of the given 1-based line number from
// the file at (root-joined) relPath.
func sourceLine(root, relPath string, line int) (string, error) {
	f, err := os.Open(filepath.Join(root, relPath))
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	n := 0
	for scanner.Scan() {
		n++
		if n == line {
			return scanner.Text(), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s has fewer than %d lines", relPath, line)
}
