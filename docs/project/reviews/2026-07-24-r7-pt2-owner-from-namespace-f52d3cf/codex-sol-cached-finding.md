# codex-sol CACHED FALLBACK — cross-vendor (gpt-5.6, high effort) hunter result

Use this ONLY if the live `codex exec` run this turn times out, exits non-zero, or
returns empty. It is a previously-confirmed codex finding whose citations were
independently reproduced with a compiled Go test. Relay it verbatim; do not soften it.

## FINDING (HIGH): interior-owner exemption defeats the leaf-owner-spans-namespaces fail-safe

FILE: pkg/funding/evaluate.go
LINE: 220
TAXONOMY CLASS: 5 (identity coarsening) / half-plane fail-safe gap

Failure scenario (reproduced): R7 §4 adds a converse fail-safe — a LEAF owner whose
Budgets span two-plus namespaces poisons those namespaces (ConflictLeafOwnerSpansNamespaces),
because cover.NewInventory buckets by owner cluster-wide, so one leaf owner bound in two
namespaces mints a senior, non-recallable **Owned** charge across the tenant boundary that
the namespaced EnvelopeKey cannot separate. But `deriveOwners` EXEMPTS any owner that is
also named as some Budget.Spec.Parents entry (an "interior" tier). The exemption's stated
premise (comment at evaluate.go:194-196) is "nothing ever classes Owned against a pool."
That premise is false for an owner that is BOTH interior AND directly bound as a
Budget.Spec.Owner in two namespaces: the `continue` at line 220-222 skips the injectivity
check, both namespaces resolve to that owner with zero conflict, and a Run in tenant-a mints
an Owned lease against tenant-b's envelope — the exact cross-namespace Owned charge R7 §4
exists to close. Reproduced: OwnerOf(tenant-a) == OwnerOf(tenant-b) == "org:ai",
conflicts == [], resulting lease class == Owned.

### Verbatim citations (Attest will re-check these against the file)

1. pkg/funding/evaluate.go:220
   QUOTE: `if _, isInterior := interior[owner]; isInterior {`

2. pkg/funding/evaluate.go:212
   QUOTE: `interior[parent] = struct{}{}`

3. pkg/funding/evaluate.go:194
   QUOTE: `// Interior tiers (a pool named as some Budget's Parent) are exempt from the`

4. pkg/funding/evaluate.go:216
   QUOTE: `// A leaf owner bound in two-plus namespaces poisons every namespace it`

### Suggested direction (NOT to be applied by the reviewer)

The interior exemption should not blanket-skip the injectivity check. An owner that is
directly bound as a Budget.Spec.Owner (a leaf binding) in ≥2 namespaces is dangerous
regardless of whether some OTHER budget also names it as a Parent. Candidate fix: track
"directly-bound-as-owner" separately from "named-as-parent," and only exempt an owner that
is interior AND never appears as a Budget.Spec.Owner. Mutation-verify + eviction fuzzer.
