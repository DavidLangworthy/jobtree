# R13 — Rename the `Lease` kind to avoid the `coordination.k8s.io/Lease` collision

**Priority:** P3 · **Design:** complete (Fable), **name choice for David** · **Next:** Opus implements, Sonnet verifies

## Problem (evidence)

jobtree's CRD kind is `Lease` in group `rq.davidlangworthy.io`, colliding with the
core `coordination.k8s.io/Lease`. `kubectl get leases` is ambiguous, tooling and
RBAC that say `leases` may hit the wrong resource, and a documented audit command
targets the wrong resource with a field selector CRDs don't support (per the audit;
Opus should grep docs for the exact `kubectl ... leases` command to fix). The
collision is a latent operational footgun.

## Root cause

A generic kind name was chosen without namespacing against core API kinds.

## Design decision

Rename the kind to an unambiguous, domain-specific name. **Recommended:
`GPULease`** (short, unmistakable, reads well: `kubectl get gpuleases`). Alternative:
`FundingLease`. Keep the group; only Kind + resource + shortnames change.

**Migration (this is the real design work, since Leases are live funding facts):**
1. Introduce the new CRD `GPULease` alongside the old `Lease` (additive; no data
   loss). Add a short name (`gl`).
2. **Dual-write / dual-read window:** the controller + plugin write the new kind and
   read both, so in-flight leases from the old kind are still honored during
   rollout. (A one-shot migration Job that copies open `Lease` → `GPULease` is the
   simplest; funding derivation reads both until the old ones close naturally.)
3. Switch the funding derivation, plugin PreBind mint, and CLI to the new kind.
4. Deprecate + remove the old `Lease` CRD once no open old leases remain.
5. Fix the RBAC `-committer` ClusterRole resource name and the broken audit command.

Because there is no production install yet (release pipeline never ran — see R15),
the team may choose a **hard rename with no migration** for pre-release simplicity.
Flag: is there any cluster with live jobtree Leases to preserve?

### Decision for David (flagged)

Name (`GPULease` recommended vs `FundingLease`), and migration mode (dual-read
window vs hard pre-release rename). If nothing runs jobtree in anger yet, hard
rename is fine and far cheaper.

## Invariant

`kubectl get <newkind>` is unambiguous and never aliases `coordination.k8s.io`;
funding continuity is preserved across the rename (no open lease is lost).

## Implementation spec (Opus)

- `api/v1/lease_types.go`: rename the Kind/resource (+ `+kubebuilder` markers,
  shortnames); regenerate deepcopy + CRD YAML.
- `pkg/admission`, `cmd/scheduler/plugin`, `pkg/funding`, `controllers/*`,
  `cmd/kubectl-runs`: update all type references.
- `deploy/helm/gpu-fleet/crds` + RBAC: new CRD, new resource name in the committer
  ClusterRole.
- Migration Job or dual-read code per the chosen mode.
- Grep docs for the broken `kubectl ... leases` audit command; fix it.

## Verification spec (Sonnet)

1. **No collision.** `kubectl get gpuleases` returns jobtree objects; `kubectl get
   leases.coordination.k8s.io` is untouched.
2. **Migration (if dual-read).** Seed an old `Lease`; assert funding still counts it
   and a new mint uses the new kind; assert the old one closes normally.
3. **RBAC.** The committer SA can CRUD the new kind and nothing else new.
4. **Golden + full suite.** Regenerate; `go test ./...`, envtest green.

## Interactions

- Pure rename; coordinate with **R4/R7** which also touch the funding lease reads,
  so the reference churn lands once.
