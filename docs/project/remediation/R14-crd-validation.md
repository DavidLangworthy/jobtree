# R14 — CRD-level validation + Lease immutability (defense that doesn't depend on the webhook being up)

**Priority:** P3 · **Design:** mostly mechanical; **the immutability approach is the Fable bit** · **Next:** Opus/Sonnet
**Mostly Opus/Sonnet — this spec pins only the design choices.**

## Problem (evidence)

CRD schemas carry near-zero validation (no minimums, no enums, no CEL), so every
invariant — including Lease spec immutability — hangs entirely on the validating
webhook, which is `failurePolicy=fail` (`controllers/kube/webhooks.go:18-22`): a
webhook outage either blocks all writes or (if flipped to Ignore) drops all
validation. Structural CRD validation would enforce the basics API-server-side,
independent of the webhook.

## Design decision (the parts that need deciding)

1. **Push field-level invariants into the CRD OpenAPI schema** via kubebuilder
   markers: `Minimum`/`Enum`/`Required` for the fields already checked in
   `api/v1/*.validate()` (gpuType required, totalGPUs>=1, malleable min/max/step
   relations that are expressible, enum on `onUpstreamFailure`, sparesPerGroup>=0).
   The webhook keeps only the **cross-object** checks it alone can do (follow
   existence/cycles, template field guards) — those genuinely need the webhook.
2. **Lease immutability via CEL transition rules**, not the webhook. Use
   `x-kubernetes-validations` (CEL, GA in 1.36) on the Lease CRD:
   `self.spec == oldSelf.spec` for spec immutability, and allow only the
   `status.closed false→true` transition. This makes immutability an apiserver
   invariant that survives a webhook outage — the key robustness win.
3. Note the `PreserveUnknownFields` template (`run_types.go:71-79`) stays
   webhook-validated by necessity (it deliberately has no structural schema).

**Fable decision captured:** immutability = CEL transition rules (not webhook), and
only genuinely cross-object rules remain in the webhook.

## Invariant

Field-level and immutability invariants hold at the apiserver regardless of webhook
availability; the webhook is required only for cross-object judgments.

## Implementation spec (Opus/Sonnet)

- `api/v1/*_types.go`: add kubebuilder validation markers mirroring `validate()`;
  add `+kubebuilder:validation:XValidation` CEL rules for Lease immutability and the
  closed-transition. Regenerate CRDs.
- Trim the webhook to cross-object checks; keep failurePolicy=fail (now it guards a
  smaller, genuinely cross-object surface).

## Verification spec (Sonnet)

1. **Webhook-down validation.** With the webhook disabled, assert a bad
   `totalGPUs=0` Run and a Lease spec mutation are both rejected by the apiserver
   (CEL/structural), while a valid Run is accepted.
2. **Immutability.** Assert `spec` edits to a Lease are rejected; `status.closed
   false→true` is allowed; `true→false` is rejected.
3. **Parity.** Existing webhook tests still pass for the cross-object rules.

## Interactions

- **R13** (Lease rename) touches the same CRD; land the CEL rules in the same pass.
