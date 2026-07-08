# R6 — Make jobtree mandatory for GPU pods

**Priority:** P1 · **Design:** complete (Fable), **one `failurePolicy` decision for David** · **Next:** Opus implements, Sonnet verifies
**Pairs with:** R5 — same ValidatingAdmissionPolicy artifact.

## Problem (evidence)

`schedulerName: jobtree` is voluntary. The plugin only runs for pods that select
the jobtree profile; a default-scheduler pod "is unaffected"
(`cmd/scheduler/main.go:30-32`). The only admission webhooks are on the four CRDs
(`controllers/kube/webhooks.go:18-22`); there is **no policy on core Pods**
requiring `schedulerName=jobtree` for a `nvidia.com/gpu` request. So any user with
`create pods` can submit a raw GPU pod on the default scheduler, get a GPU, and
never create a Lease or charge a budget. The "budget-correct fleet" promise
(`docs/index.md:19,54`) is skipped by simply not using jobtree.

## Root cause

Budget enforcement lives entirely in the jobtree scheduling path, which is opt-in.
There is no cluster-level rule binding GPU consumption to that path.

## Design decision

**A ValidatingAdmissionPolicy on Pods (the R5/R6 shared artifact):** for any pod
that requests `nvidia.com/gpu` (request or limit > 0 on any container),
**require `spec.schedulerName == "jobtree"`**; reject otherwise. Combined with
R5's clause (jobtree-owned fields settable only by the controller SA), a tenant
can neither escape jobtree (raw GPU pod → rejected) nor forge their way through it
(provenance fields → rejected). VAP (CEL, GA in k8s 1.36) needs no webhook server.

**Exemptions** (values-configurable allow-list): the jobtree control-plane
namespace and any operator-run infra namespaces (device-plugin DaemonSets, etc.)
must be exempt so cluster components with legitimate GPU access are not blocked.
Bind the policy to workload namespaces via a `ValidatingAdmissionPolicyBinding`
with a namespaceSelector, rather than cluster-wide, so the exempt set is explicit.

### Decision for David (flagged)

`failurePolicy`:
- **`Fail` (recommended):** if the policy/API is unavailable, GPU-pod creation is
  blocked — safe (no unbudgeted GPU use) but a policy outage stops new GPU work.
  Mitigate by exempting the control-plane namespace so jobtree itself can always
  come up.
- **`Ignore`:** GPU pods admit during an outage — available but leaves a bypass
  window. Not recommended for a budget-integrity control.

## Invariant

No pod consumes `nvidia.com/gpu` outside the jobtree scheduling path (except in
explicitly exempt namespaces), so every funded GPU has a Lease and every GPU-hour
is attributed to a budget.

## Implementation spec (Opus)

- `deploy/helm/gpu-fleet/templates/`: `ValidatingAdmissionPolicy` +
  `ValidatingAdmissionPolicyBinding` (shared with R5). Two CEL rules:
  1. `has(gpu request) && object.spec.schedulerName != "jobtree"` → deny.
  2. (R5) `sets any jobtree-owned field && userInfo.username != controllerSA` → deny.
- Values: `controllerServiceAccount`, `exemptNamespaces`/`namespaceSelector`,
  `failurePolicy`, and the controller-only field list (shared with R3/R5).
- `docs/operator-guide/admin-setup.md`: document the policy, the exempt-namespace
  requirement, and the break-glass (delete the binding) — this also feeds R18.
- Note the managed-cloud caveat: on GKE/EKS/AKS you cannot edit the control-plane
  scheduler, but you **can** run the jobtree scheduler as a second scheduler
  Deployment and apply this VAP — document that path.

## Verification spec (Sonnet)

1. **Raw GPU pod rejected.** As a tenant, create a pod requesting `nvidia.com/gpu`
   with the default scheduler; assert the VAP denies it.
2. **jobtree GPU pod allowed.** Same pod with `schedulerName=jobtree` (created by
   the controller SA, given R5); assert admitted.
3. **Non-GPU pod unaffected.** A CPU-only pod on the default scheduler admits.
4. **Exempt namespace.** A GPU pod in an exempt namespace admits (device plugins
   keep working).
5. **failurePolicy behavior.** Simulate policy unavailability; assert the chosen
   policy (Fail/Ignore) behaves as specified; assert the control-plane namespace
   is never blocked.
6. **Live.** Add to a smoke: attempt a raw GPU pod as a limited user on kind →
   rejected; controller path → works.

## Interactions

- **R5** is the same artifact; the two CEL rules ship together.
- **R3** contributes the `Promise` marker to the controller-only field list.
- **R18** (operator break-glass docs) should cover disabling this policy.
