package antifake

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestOnlyTheSoleCloserClosesALease is the CI gate (`make antifake`) for the
// CLONED OBLIGATION class (docs/project/adversarial-review-playbook.md, class 7).
//
// Exactly one function may transition a Lease from open to closed. There is no
// allowlist and no ratchet: the permitted count is zero. An allowlist would be a
// door, and this rule exists because doors kept being found.
func TestOnlyTheSoleCloserClosesALease(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}

	// Prove the anchor. If CloseLease were renamed or moved, every check below
	// would pass vacuously — zero writes outside a function that no longer
	// exists. A lint that cannot fail is not a lint.
	ok, err := soleCloserAnchorExists(root)
	if err != nil {
		t.Fatalf("looking for %s in %s: %v", soleCloserFunc, soleCloserFile, err)
	}
	if !ok {
		t.Fatalf("%s is not declared in %s.\n"+
			"This check is anchored to it. If you moved or renamed the sole closer, update\n"+
			"soleCloserFunc/soleCloserFile in hack/antifake/soleclose.go — do not delete the check.",
			soleCloserFunc, soleCloserFile)
	}

	writes, err := scanLeaseClosureWrites(root)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	var offenders []string
	sawSoleCloser := false
	for _, w := range writes {
		if w.FuncName == soleCloserFunc && w.RelPath == soleCloserFile {
			sawSoleCloser = true
			continue
		}
		offenders = append(offenders, w.String())
	}

	if !sawSoleCloser {
		t.Errorf("%s in %s writes no Lease closure field. Either it stopped closing leases "+
			"(and something else now does), or the scanner is broken. Both are bugs.",
			soleCloserFunc, soleCloserFile)
	}

	if len(offenders) > 0 {
		t.Fatalf("%d site(s) close a Lease outside %s:\n  %s\n\n"+
			"An open Lease charges a budget and holds GPUs. Closing one is an obligation, and a\n"+
			"cloned obligation drifts: applyResolution and cleanupDeletedRun each hand-rolled it,\n"+
			"and a clone that set Closed without Ended made pkg/funding bill the lease to its START\n"+
			"instant, so it accrued nothing for its entire life.\n\n"+
			"Call controllers.CloseLease(lease, reason, now) instead. There is no allowlist.",
			len(offenders), soleCloserFunc, strings.Join(offenders, "\n  "))
	}
}

// The check must be able to FAIL. A lint nobody has seen fail is a lint nobody
// knows works — this repo shipped a helm assertion that passed against the very
// bug it was written to catch, and only trying to make it fail revealed that it
// was comparing a selector with itself.
//
// So: feed the detector each shape it must catch, and each shape it must not.
func TestSoleCloserDetectorActuallyDetects(t *testing.T) {
	mustFlag := map[string]string{
		"direct closed":       `lease.Status.Closed = true`,
		"indexed closed":      `state.Leases[i].Status.Closed = true`,
		"ended stamp":         `l.Status.Ended = &ended`,
		"closure reason":      `other.Status.ClosureReason = "Swap"`,
		"pointer deref chain": `c.State.Leases[j].Status.Closed = true`,
	}
	for name, stmt := range mustFlag {
		t.Run("flags/"+name, func(t *testing.T) {
			if got := scanStmt(t, stmt); len(got) != 1 {
				t.Fatalf("the detector missed %q: it would let a clone through", stmt)
			}
		})
	}

	mustIgnore := map[string]string{
		"reading closed":         `if lease.Status.Closed { return }`,
		"run phase":              `run.Status.Phase = RunPhaseFailed`,
		"different status field": `run.Status.Message = "shrunk"`,
		"comparison":             `x := lease.Status.ClosureReason == "Swap"`,
		"not under Status":       `lease.Spec.Closed = true`,
	}
	for name, stmt := range mustIgnore {
		t.Run("ignores/"+name, func(t *testing.T) {
			if got := scanStmt(t, stmt); len(got) != 0 {
				t.Fatalf("the detector false-flagged %q as a closure write: %v", stmt, got)
			}
		})
	}
}

// scanStmt runs the LHS detector over a single statement, in isolation.
func scanStmt(t *testing.T, stmt string) []string {
	t.Helper()
	src := "package p\nfunc f() {\n" + stmt + "\n}\n"
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatalf("parse %q: %v", stmt, err)
	}
	var hits []string
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			if field, ok := leaseClosureTarget(lhs); ok {
				hits = append(hits, field)
			}
		}
		return true
	})
	return hits
}
