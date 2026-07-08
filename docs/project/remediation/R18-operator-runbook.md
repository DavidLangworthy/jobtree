# R18 — Operator day-2 runbook: break-glass, uninstall, CRD upgrade

**Priority:** P4 · **Design:** complete (Fable — the procedures below ARE the design) · **Next:** Sonnet/Opus writes them into `admin-setup.md` and adds the scripts

## Problem (evidence)

There is no break-glass, uninstall, or CRD-upgrade story. When jobtree misbehaves,
an operator has no documented way to fall back to the default scheduler or disable
the (fail-closed) webhooks/policy; when they want it gone, no uninstall order; when
they upgrade, no CRD migration guidance. Several fail-closed surfaces
(webhook `failurePolicy=fail`, and the R6 GPU-pod policy) make this dangerous.

## Root cause

Day-2 operations were never designed; only the install (which is itself broken —
R15) was documented.

## Design decision — the procedures (Fable deliverable)

These are the correct sequences given jobtree's architecture. R18's remaining work
is to write them into the operator guide and add helper scripts.

**Break-glass (jobtree is wedging GPU scheduling):**
1. **Restore GPU scheduling immediately:** delete the
   `ValidatingAdmissionPolicyBinding` from R6 (`kubectl delete
   validatingadmissionpolicybinding jobtree-gpu-mandatory`). New GPU pods can now
   use the default scheduler. This is the single most important lever and must be a
   one-liner.
2. **Stop jobtree committing:** scale the jobtree scheduler Deployment to 0. Pods
   selecting `schedulerName=jobtree` will pend (expected) — repoint urgent work to
   the default scheduler by editing `schedulerName` (now allowed, step 1).
3. **If the CRD webhook is the problem:** set the four CRD webhooks'
   `failurePolicy=Ignore` (or delete the `ValidatingWebhookConfiguration`) so
   Run/Budget/Lease writes are unblocked. Document that this drops cross-object
   validation until restored.
4. Leave the manager running so it keeps *closing* leases (accounting stays honest)
   unless it is the faulting component.

**Uninstall (ordered so nothing is orphaned):**
1. Break-glass step 1 (remove the mandatory policy) so no future GPU pods are
   blocked mid-teardown.
2. Delete Runs (their R12 finalizers close leases); wait for finalizers to drain.
3. Scale down + delete the manager and scheduler Deployments, Services, webhooks,
   policy, RBAC.
4. Delete the CRDs last (this deletes all Budget/Lease/Reservation objects; ensure
   leases are closed first for a clean audit trail).
5. `helm uninstall` covers most of this if the finalizer-drain in (2) is respected;
   document the manual order for the non-helm path.

**CRD upgrade / migration:**
- Today all CRD changes are **additive** (new optional fields), so `kubectl apply`
  / `helm upgrade` of the CRDs is safe and needs no conversion. Document this
  guarantee and the "additive-only until a real conversion webhook exists" rule.
- The one non-additive change on the horizon is **R13 (Lease→GPULease rename)** —
  point the upgrade doc at R13's migration section.
- Pin a Kubernetes version-compatibility statement (the scheduler binary tracks a
  k8s minor — currently 1.36); document supported skew.

## Invariant

An operator can, in one documented step each, (a) restore default GPU scheduling,
(b) stop jobtree committing, and (c) unblock CRD writes — and can uninstall without
orphaning leases/finalizers or losing accounting.

## Implementation spec (Sonnet/Opus)

- `docs/operator-guide/admin-setup.md` (+ maybe a new `runbook.md`): write the three
  procedures above.
- `hack/` scripts: `break-glass.sh` (does the 3 levers), `uninstall.sh` (ordered).
- Cross-link from R6 (policy break-glass), R12 (finalizer drain), R13 (rename
  migration), R15 (fix install first so the guide is trustworthy end to end).

## Verification spec (Sonnet)

1. **Break-glass on kind.** Wedge scheduling (scale scheduler to 0 with pending GPU
   pods); run `break-glass.sh`; assert a GPU pod schedules on the default scheduler.
2. **Uninstall on kind.** Install, create a Running run, run `uninstall.sh`; assert
   no leaked finalizers/objects and leases were closed before CRD deletion.
3. **Additive upgrade.** `helm upgrade` across an additive CRD change with live
   objects; assert no data loss and no downtime.

## Interactions

- Depends conceptually on **R6** (policy), **R12** (finalizers), **R13** (rename),
  **R15** (a working install to document against).
