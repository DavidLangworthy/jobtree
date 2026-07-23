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
	"sort"
	"strings"
)

// maxAllowedUnreadCRDFields is the shrink-only ratchet for
// crdFieldsAllowlistName. As with maxAllowedTerminalPhaseExceptions, raising
// it requires a deliberate edit to this file alongside a new, individually
// reasoned allowlist entry.
//
// Seeded with the 8 api/v1 fields this lint's first run found accepted by
// the API, schema-validated, and deep-copied, but read (or even written) by
// nothing outside api/v1 — see crd-fields-allowlist.txt for the full,
// individually-reasoned list. Track D (TRUTH, PR #32) then wired four of them
// for real — RunSpec.Runtime + RunRuntime.Checkpoint (checkpoint grace in
// HandleNodeFailure) and BudgetSpec.AutoRenew + AutoRenewSchedule.NotifyBefore
// (renewal in BudgetController) — so the ratchet was shrunk 8→4. The remaining
// four (GPULeaseSpec.CompPath — deferred to Track E/ROLES; BudgetStatus.Observed
// Generation, RunStatus.Generation, ReservationStatus.CanceledAt — newly
// surfaced by this lint, awaiting a wire-or-delete triage) stay documented in
// the allowlist file.
//
// Both in-flight roles-API seam fields added by #28 gained real readers in the
// P2b cutover, so the trunk's temporary 4->6 bump has fully ratcheted back to 4:
// RunRole.Template is read by controllers/kube/bridge.go's buildPod (renders the
// real workload pod) and RunRole.GPUsPerPod by run_controller.go's
// intentPodShape (the per-pod GPU count the controller emits). Back to the
// original baseline — nothing to shrink before merging to main.
const maxAllowedUnreadCRDFields = 2

const crdFieldsAllowlistName = "crd-fields-allowlist.txt"

const apiV1RelDir = "api/v1"

// crdField is one exported struct field declared in a non-generated,
// non-test file under api/v1.
type crdField struct {
	StructName string
	FieldName  string
	RelPath    string
	Line       int
}

func (f crdField) key() string {
	return f.StructName + "." + f.FieldName
}

// collectAPIFields returns every exported field of every struct type
// declared in api/v1's non-generated, non-test Go files.
func collectAPIFields(root string) ([]crdField, error) {
	apiDir := filepath.Join(root, filepath.FromSlash(apiV1RelDir))
	entries, err := os.ReadDir(apiDir)
	if err != nil {
		return nil, err
	}

	var fields []crdField
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, "zz_generated") {
			continue
		}
		path := filepath.Join(apiDir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		relPath := apiV1RelDir + "/" + name
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				for _, field := range st.Fields.List {
					for _, fieldName := range field.Names {
						if !fieldName.IsExported() {
							continue
						}
						fields = append(fields, crdField{
							StructName: ts.Name.Name,
							FieldName:  fieldName.Name,
							RelPath:    relPath,
							Line:       fset.Position(fieldName.Pos()).Line,
						})
					}
				}
			}
		}
	}

	sort.Slice(fields, func(i, j int) bool {
		if fields[i].StructName != fields[j].StructName {
			return fields[i].StructName < fields[j].StructName
		}
		return fields[i].FieldName < fields[j].FieldName
	})
	return fields, nil
}

// collectUsedFieldNames parses every non-test, non-generated Go file
// outside api/v1 (and outside anything skipDir excludes) once, and returns
// the set of identifier names used either as a selector (`x.Foo`) or as a
// composite literal key (`Foo: value`). This is intentionally name-only
// (not struct-qualified) and intentionally excludes *_test.go files
// repo-wide — matching the "non-generated non-test readers" bar TESTINFRA-6
// sets: a field exercised only by its own validation/round-trip test is not
// the same as a controller actually consuming it, and is exactly the
// "accepted-but-unread" pattern (audit §3.4) this check exists to catch.
//
// Best-effort by construction (documented, not a bug): because matching is
// by bare field name, a field named identically to something unrelated
// elsewhere (e.g. "Name") will never be flagged even if the specific field
// in question is never truly read. Distinctively-named fields — which is
// most of what a schema review would actually miss — are exactly what this
// catches.
func collectUsedFieldNames(root string) (map[string]bool, error) {
	used := map[string]bool{}
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
			if rel == apiV1RelDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "zz_generated") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				used[x.Sel.Name] = true
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok {
					used[id.Name] = true
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return used, nil
}

// loadCRDFieldsAllowlist parses crd-fields-allowlist.txt. Format matches
// terminal-phase-allowlist.txt: `<StructName.FieldName> <reason>` per line,
// blank lines and '#' comments ignored.
func loadCRDFieldsAllowlist(path string) (map[string]string, error) {
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
		if len(fields) != 2 || !strings.Contains(fields[0], ".") {
			return nil, fmt.Errorf("%s:%d: malformed entry %q, want '<Struct.Field> <reason>'", path, lineNo, line)
		}
		entries[fields[0]] = fields[1]
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
