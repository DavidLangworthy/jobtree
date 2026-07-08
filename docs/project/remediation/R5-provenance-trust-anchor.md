# R5 — Stop trusting pod annotations for funding provenance

**Priority:** P1 (critical if multi-tenant) · **Design:** complete (Fable) · **Next:** Opus implements, Sonnet verifies
**Pairs with:** R6 — the two share one ValidatingAdmissionPolicy. Implement together, first among P0/P1.

## Problem (evidence)

`Permit` returns Allow immediately for any pod whose `lease-reason` annotation is
`Swap`, skipping the gang **and** funding gate (`cmd/scheduler/plugin/
plugin.go:144-146`). `PreBind` then builds the paying `cover.Segment` **directly
from the pod's own `payer-owner/budget/envelope` annotations**, checking only that
they are non-empty (`plugin.go:213-223,236`), and mints a Lease with them. Nothing
verifies the pod came from the controller: `kube.buildPod` sets **no
OwnerReference** (`controllers/kube/bridge.go:400-408`), and there is **no pod
admission webhook** (`controllers/kube/webhooks.go` registers only the four CRD
webhooks). The controller's own swap pod is the exact forgeable template
(`run_controller.go:1692-1699`).

**Exploit (confirmed):** a user with ordinary namespaced `create pods` creates a
pod with `schedulerName=jobtree`, `lease-reason=Swap`, `payer-*=<victim's
owner/budget/envelope>`, and a real `nvidia.com/gpu` request. The plugin binds it
and mints a real Lease charged to the victim's envelope — even an exhausted one.
A cross-tenant billing/quota-integrity bypass.

## Root cause

The single committer trusts unauthenticated, user-writable pod metadata as if it
were controller-issued. There is no authentication boundary between "the
controller emitted this" and "a tenant hand-crafted this".

## Design decision

Defense in depth, two layers — the policy layer is the real fix; the plugin layer
is belt-and-suspenders.

1. **Authentication at admission (the fix — shared with R6).** A
   ValidatingAdmissionPolicy on Pods (k8s 1.36 → VAP is GA, no webhook server
   code) that: for any pod carrying a jobtree-owned field —
   `spec.schedulerName == "jobtree"`, or any of the `rq.davidlangworthy.io/`
   `payer-*`, `lease-reason`, `cohort`, `expected-width`, `swap-node`, `promise`
   annotations, or the `role` label — **rejects the create unless the requesting
   user is the jobtree controller's ServiceAccount** (`request.userInfo.username
   == "system:serviceaccount:<ns>:<controller-sa>"`). Tenants therefore cannot set
   provenance, swap/promise markers, or select the jobtree scheduler at all; only
   the controller can. See R6 for the same policy's mandatory-scheduler half.
2. **Defense-in-depth in the plugin.** Even assuming (1), `PreBind` for a `Swap`
   (or `Promise`, R3) pod should not blind-trust annotations: look up the actual
   prior **Spare lease** for this run+node and mint from *its* recorded provenance,
   verifying the carried annotations match. Set an `OwnerReference` to the Run on
   every emitted pod (in `buildPod`) so the plugin can additionally require that
   the pod is owned by a Run in its namespace. (OwnerReference alone is forgeable
   without the OwnerReferencesPermissionEnforcement plugin, so it is a hint, not
   the anchor — the anchor is the VAP.)

**Why the VAP is the anchor, not the OwnerReference or the plugin check:** a pod
creator can set arbitrary `ownerReferences` and arbitrary annotations; only an
admission gate keyed on the authenticated `userInfo` can actually distinguish the
controller from a tenant. The plugin cross-check catches a controller bug or a
policy gap but cannot be the sole boundary.

## Invariant

A Lease can be minted only for a pod that the jobtree controller created; a pod's
`payer-*` provenance is honored only if it matches a real controller-issued lease
(spare/promise) for that run. No tenant-authored pod can cause a mint against any
envelope.

## Implementation spec (Opus)

- **Policy** (shared artifact with R6): `deploy/helm/gpu-fleet/templates/` — a
  `ValidatingAdmissionPolicy` + `ValidatingAdmissionPolicyBinding`. Parameterize
  the controller SA name via values. CEL: match jobtree-owned fields → require
  `request.userInfo.username == params.controllerSA` (and allow the system masters
  group for break-glass). Ship the controller-only field list as a values list so
  R3's `Promise` marker slots in.
- **`controllers/kube/bridge.go` `buildPod`**: set
  `ObjectMeta.OwnerReferences` to the owning Run (Controller=true,
  BlockOwnerDeletion as appropriate) — this also advances R7/R12 (GC).
- **`cmd/scheduler/plugin/plugin.go` `PreBind`**: for Swap/Promise, resolve the
  real prior lease (List leases for run+node, RoleSpare/open) and mint from its
  provenance; error if none matches the carried annotations. Optionally require the
  pod's OwnerReference names an existing Run in-namespace.
- Keep `failurePolicy` decision aligned with R6.

## Verification spec (Sonnet)

1. **Exploit test (must now fail closed).** As a non-controller user, create a pod
   with `schedulerName=jobtree` + forged `payer-*` + `lease-reason=Swap`; assert
   the VAP **rejects** the create; assert no Lease exists. (Pre-R5 this mints.)
2. **Controller still works.** As the controller SA, create the same-shaped swap
   pod; assert it is admitted and the plugin mints from the verified prior spare
   lease.
3. **Plugin cross-check.** Feed the plugin a Swap pod whose `payer-*` does **not**
   match any real spare lease; assert PreBind errors (no mint).
4. **OwnerReference/GC.** Assert emitted pods carry the Run OwnerReference and are
   garbage-collected when the Run is deleted.
5. **Live.** Extend `swap-smoke.sh`: attempt the forgery with a limited-RBAC
   kubeconfig; assert rejection; confirm the legitimate controller-driven swap
   still passes.

## Interactions

- **R6** is the same policy; build once.
- **R3**'s `Promise` marker joins the controller-only field list here.
- **R7/R12** benefit from the OwnerReference added in `buildPod`.
