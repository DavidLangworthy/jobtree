package antifake

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCRDFieldsHaveReaders is the CI gate (`make antifake`) for audit
// pattern P4 ("API accepts fields no controller reads",
// docs/project/fake-features-audit.md §3.4): every exported field on a
// struct declared in api/v1's non-generated, non-test source must be
// referenced (as a selector or composite-literal key) somewhere outside
// api/v1 in non-generated, non-test Go code. A field with zero such
// references is schema-validated and deep-copied but behaviorally inert —
// exactly the shape of Runtime.Checkpoint (#10/#18) and Budget.AutoRenew
// (#22).
//
// Like the terminal-phase check, this uses a shrink-only ratcheted
// allowlist: known, individually-reasoned exceptions must be listed in
// crd-fields-allowlist.txt, capped at maxAllowedUnreadCRDFields in
// crdfields.go. A field that gains a real reader must be removed from the
// allowlist (the check fails on the resulting staleness) rather than left
// there.
func TestCRDFieldsHaveReaders(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}

	fields, err := collectAPIFields(root)
	if err != nil {
		t.Fatalf("collectAPIFields: %v", err)
	}
	used, err := collectUsedFieldNames(root)
	if err != nil {
		t.Fatalf("collectUsedFieldNames: %v", err)
	}

	allowlistPath := filepath.Join(root, "hack", "antifake", crdFieldsAllowlistName)
	allowlist, err := loadCRDFieldsAllowlist(allowlistPath)
	if err != nil {
		t.Fatalf("loadCRDFieldsAllowlist: %v", err)
	}
	if len(allowlist) > maxAllowedUnreadCRDFields {
		t.Fatalf("%s lists %d exceptions, exceeding the shrink-only ratchet of %d; "+
			"bump maxAllowedUnreadCRDFields in crdfields.go only alongside a new, "+
			"individually-reasoned exception — never just to make this pass",
			crdFieldsAllowlistName, len(allowlist), maxAllowedUnreadCRDFields)
	}

	var newlyUnread []string
	seenAllowlistKeys := map[string]bool{}

	for _, f := range fields {
		if used[f.FieldName] {
			continue
		}
		key := f.key()
		if reason, ok := allowlist[key]; ok {
			_ = reason
			seenAllowlistKeys[key] = true
			continue
		}
		newlyUnread = append(newlyUnread, fmt.Sprintf(
			"%s (%s:%d) has zero non-generated, non-test readers outside api/v1 — "+
				"ship it with a real consumer or delete the field, don't accept-and-ignore. "+
				"If this is a genuinely new, deliberately-accepted interim gap, add it to %s "+
				"with a reason and bump maxAllowedUnreadCRDFields in crdfields.go",
			key, f.RelPath, f.Line, crdFieldsAllowlistName))
	}

	var stale []string
	for key, reason := range allowlist {
		if !seenAllowlistKeys[key] {
			stale = append(stale, fmt.Sprintf(
				"%s (reason: %s) either no longer exists or now has a real reader — remove it from %s (shrink-only ratchet)",
				key, reason, crdFieldsAllowlistName))
		}
	}

	sort.Strings(newlyUnread)
	sort.Strings(stale)

	if len(newlyUnread) > 0 || len(stale) > 0 {
		var b strings.Builder
		for _, m := range newlyUnread {
			b.WriteString("UNREAD FIELD: ")
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
