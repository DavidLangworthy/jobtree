package antifake

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The sole-closer rule.
//
// An open Lease charges a budget and holds GPUs. Closing one is therefore an
// obligation, and this repository has now shipped, and had to fix, three
// separate hand-rolled implementations of it:
//
//   - controllers.CloseLease — the real one.
//   - applyResolution        — three raw field assignments, inline in a loop.
//   - cleanupDeletedRun      — three more, in another package.
//
// Cloned obligations drift. Only one of the three could be instrumented, metered,
// or fixed in a single place, and a clone that set Closed without Ended made
// pkg/funding bill the lease to its START instant, so it accrued nothing at all
// for its entire life — a silent under-charge that no dashboard showed.
//
// So: exactly one function in this repository may write a Lease's closure fields.
// Not by convention, not by review, but because `make antifake` fails otherwise.
//
// Unlike the other checks in this package there is NO allowlist and no ratchet
// constant. The permitted count is zero, forever. An allowlist here would be a
// door, and this rule exists precisely because doors kept being found.
const soleCloserFunc = "CloseLease"

// soleCloserFile is where soleCloserFunc must live. Pinning it means renaming or
// moving the function cannot silently disable this check — the anchor assertion
// below fails instead.
const soleCloserFile = "controllers/run_controller.go"

// leaseClosureFields are the three fields that together constitute "this lease is
// closed". They are unique to LeaseStatus (api/v1/lease_types.go:54-56); no other
// CRD status carries them, so a selector match is unambiguous.
var leaseClosureFields = map[string]bool{
	"Closed":        true,
	"Ended":         true,
	"ClosureReason": true,
}

// closureWrite is one assignment to a Lease closure field.
type closureWrite struct {
	RelPath  string // slash-separated, relative to repo root
	Line     int
	Field    string
	FuncName string // enclosing function, "" at file scope
}

func (w closureWrite) String() string {
	where := w.FuncName
	if where == "" {
		where = "<file scope>"
	}
	return fmt.Sprintf("%s:%d: %s writes .Status.%s", w.RelPath, w.Line, where, w.Field)
}

// scanLeaseClosureWrites walks every non-test .go file under root and reports
// every assignment whose left-hand side is an `X.Status.<closure field>`
// selector chain.
//
// Test files are exempt: a fixture may construct an already-closed lease, and a
// half-stamped fixture is caught at runtime by pkg/invariant's INV-CLOSE-STAMPED
// on the next engine call, which is a better error than a lint could give.
func scanLeaseClosureWrites(root string) ([]closureWrite, error) {
	var found []closureWrite
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
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		file, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range assign.Lhs {
					if field, ok := leaseClosureTarget(lhs); ok {
						found = append(found, closureWrite{
							RelPath: rel, Line: fset.Position(lhs.Pos()).Line,
							Field: field, FuncName: fn.Name.Name,
						})
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].RelPath != found[j].RelPath {
			return found[i].RelPath < found[j].RelPath
		}
		return found[i].Line < found[j].Line
	})
	return found, nil
}

// leaseClosureTarget reports whether e is an `X.Status.<closure field>` selector
// chain, and which field it names.
func leaseClosureTarget(e ast.Expr) (string, bool) {
	fieldSel, ok := e.(*ast.SelectorExpr)
	if !ok || !leaseClosureFields[fieldSel.Sel.Name] {
		return "", false
	}
	statusSel, ok := fieldSel.X.(*ast.SelectorExpr)
	if !ok || statusSel.Sel.Name != "Status" {
		return "", false
	}
	return fieldSel.Sel.Name, true
}

// soleCloserAnchorExists reports whether soleCloserFunc is still declared in
// soleCloserFile. Without this, renaming the function would make the check pass
// vacuously: zero writes outside a function that no longer exists.
func soleCloserAnchorExists(root string) (bool, error) {
	path := filepath.Join(root, filepath.FromSlash(soleCloserFile))
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		return false, err
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == soleCloserFunc {
			return true, nil
		}
	}
	return false, nil
}
