# Remediation design specs

Design-complete handoffs for the findings in
[`../design-vs-implementation-audit.md`](../design-vs-implementation-audit.md).
Each spec is self-contained: an Opus (code) or Sonnet (mechanical debug + verify)
agent should be able to pick one up cold, without re-deriving the design.

## Model legend

- **Fable** — design / analysis / hard root-cause. **All of these specs are the
  Fable output; the design decisions here are made.** Do not re-open them without
  a stated reason.
- **Opus** — writes the code from the "Implementation spec" section.
- **Sonnet** — mechanical debugging, repro harnesses, and the "Verification spec".

**Sizing, sequencing, and the honest schedule: [SIZING.md](SIZING.md).**
R9's architecture was re-scoped after the decision: **[R9-jobset-amendment.md](R9-jobset-amendment.md)**.
R7's tenancy model is settled in **[R7-tenancy-amendment.md](R7-tenancy-amendment.md)**. Every
remaining item is sized (XS–XL), with its blast radius, test surface, and what
blocks it. Read it before picking up work — five undecided forks sit upstream of
about a third of what is left, and two of them change the *size* of other items.

## Status board

Design = the Fable layer (this repo). ✅ = design spec written; **mech** = purely
mechanical, no design needed (implement straight from the audit — Opus/Sonnet).

**P0 — correctness at the new committer**

| Spec | Finding | Design | Code | Verify |
|---|---|---|---|---|
| [R1](R1-phantom-lease-clear.md) | Phantom `pending` lease funding leak | ✅ | ✅ #46 | ✅ |
| [R2](R2-gang-recovery.md) | Partial-gang wedge / restart / adopt-at-partial-width | ✅ | ◐ pt1 de-wedge + pt2 adopt-at-width done; pt3 restart reconstruction pending | ◐ unit; restart + live-proof pending |
| [R3](R3-opportunistic-fork.md) | Opportunistic activation incoherent post-cutover | ✅ (refined) | ✅ Promise path | ✅ engine + plugin |
| [R4](R4-plugin-hotpath.md) | Permit hot-path relists + unbounded ledger replay | ✅ | ◐ pt1 metrics + pt2a compaction primitive done; pt1b caching + pt2b settlement pending | ◐ unit+race+round-trip; caching/bench deferred |

**P1 — multi-tenant safety**

| Spec | Finding | Design | Code | Verify |
|---|---|---|---|---|
| [R5](R5-provenance-trust-anchor.md) | Forgeable funding provenance (swap mint) | ✅ | ✅ plugin+VAP | ◐ unit; VAP CEL needs kind |
| [R6](R6-mandatory-scheduler.md) | Budget is opt-in for GPU pods | ✅ | ✅ VAP (off by default) | ◐ VAP CEL needs kind |
| [R7](R7-tenancy-envelope-namespace.md) | Namespaces are not a tenancy boundary | ✅ | ⏳ Opus | ⏳ Sonnet |

**P2 — workload lifecycle (blocks "usable for ML")**

| Spec | Finding | Design | Code | Verify |
|---|---|---|---|---|
| [R8](R8-pod-failure-handling.md) | Failed pod = immortal budget-charging zombie | ✅ | ⏳ Opus | ⏳ Sonnet |
| [R9](R9-rendezvous.md) | No distributed-training rendezvous on the live path | ✅ **amended** ([9A](R9-jobset-amendment.md)) | 9A-0 ✅ · 9A-1..4 ⏳ Opus | ⏳ Sonnet |
| R10 | False rendezvous API comment (`run_types.go:67`) | **mech** | ✅ (in 9A-0) | — |

**P3 — Kubernetes conventions & API hardening**

| Spec | Finding | Design | Code | Verify |
|---|---|---|---|---|
| [R11](R11-status-conditions.md) | No `status.conditions` anywhere | ✅ | ⏳ Opus | ⏳ Sonnet |
| [R12](R12-ownerrefs-finalizers.md) | Zero ownerRefs/finalizers; hand-rolled GC | ✅ | ⏳ Opus | ⏳ Sonnet |
| [R13](R13-lease-rename.md) | `Lease` collides with `coordination.k8s.io/Lease` | ✅ | ⏳ Opus | ⏳ Sonnet |
| [R14](R14-crd-validation.md) | Near-zero CRD validation; webhook-only immutability | ✅ | ⏳ Opus | ⏳ Sonnet |

**P4 — admin, release & project hygiene**

| Spec | Finding | Design | Code | Verify |
|---|---|---|---|---|
| R15 | Documented install can't work; phantom notifier | **mech** | ⏳ Sonnet | ⏳ Sonnet |
| R16 | ServiceMonitor selector mismatch; Prom-Operator hard dep | **mech** | ✅ label + capability gate | ✅ helm-assertions |
| R17 | Prod overlay: 3 replicas, leader-election off; scheduler off | **mech** | ✅ flag wired + overlays fixed | ✅ helm-assertions |
| [R18](R18-operator-runbook.md) | No break-glass / uninstall / CRD-upgrade story | ✅ | ⏳ Sonnet | ⏳ Sonnet |
| [R19](R19-license-governance.md) | No LICENSE; fictional governance | ✅ | ✅ all-rights-reserved + real MAINTAINERS/SECURITY | ✅ |

**P5 — observability & correctness papercuts**

| Spec | Finding | Design | Code | Verify |
|---|---|---|---|---|
| [R20](R20-plugin-events.md) | Plugin scheduling refusals invisible to `explain` | ✅ | ⏳ Opus | ⏳ Sonnet |
| [R21](R21-cordon-not-failure.md) | Cordon treated as node failure → destructive swap | ✅ **amended** | ✅ fencing, not a NotReady timer | ✅ |
| [R22](R22-reclaim-slot-granularity.md) | Swap reclaim closes co-located runs (node granularity) | ✅ | ✅ slot-exact, funding-aware decline | ✅ |
| [R23](R23-workload-observability.md) | No logs/pods/artifacts story | ✅ | ⏳ Opus | ⏳ Sonnet |
| R24 | Doc-honesty leftovers (README/spares-and-fill/guide) + funding-model doc fixes (see below) | **mech** | 🟡 partial — see below | — |

**From the funding-model review** (`../funding-model-review.md`, 2026-07-08):

| Spec | Finding | Design | Code | Verify |
|---|---|---|---|---|
| [R25](R25-spare-node-lease-leak.md) | Spare-only node deletion leaks an immortal spare lease | ✅ | ✅ (with R21/R22) | ✅ |
| [R26](R26-ledger-auditor.md) | No runtime audit of leases vs pods/nodes — ledger integrity is unverified | ✅ | ⏳ Opus | ⏳ Sonnet |

**Mechanical-only (R10, R15, R24; R16 + R17 landed 2026-07-09):** no design
decision — the audit's finding text is the spec. R10 = correct the false comment.
R15 = build+push images in `release.yaml`, fix helm repo + `image.tag`, default
notifier off. ~~R16 = fix the ServiceMonitor selector + make the
Prometheus-Operator dep optional.~~ ~~R17 = enable leader election in prod, enable
the scheduler in both overlays.~~ R24 = fix
the stale README claim, drop the `spares-and-fill.md` "opportunistic fill" fake,
correct the researcher-guide `spares` field name, **plus the funding-model doc
fixes** (funding-model-review §1/§3): index.md's budget-as-gate framing and its
"sole committer" claim (now TRUE — R3 landed 2026-07-08; just drop any hedge);
`concepts/leases.md` dead `Fail`
enum + role/class conflation (`Borrowed` as a role); `concepts/budgets.md` and
`concepts/runs.md` pre-four-class models; add the explicit three-plane /
quota-may-over-or-under-commit statement to fundamentals or quota-semantics.

**R24 progress (2026-07-09).** Done: `concepts/leases.md` (the role/class conflation,
the dead `Fail` and `RandomPreempt` mint reasons, `endTime`→`ended`, the two reason
enums separated and each value checked against the code); `user-guide/spares-and-fill.md`
(the `role=Borrowed` fake, the "discounted" spare); `examples/worked-examples.md` (same,
plus "cordon failed node", which R21 made a no-op); `roadmap/design/M6` (banner naming
both divergences); `concepts/runs.md` (two-class → four-class, derived-not-stored);
`index.md` (budget-as-gate framing). `index.md`'s "sole committer" claim needed no
change — it is true and already unhedged since R3.

Still open: `concepts/budgets.md`'s pre-four-class model, and the explicit
three-plane / quota-may-over-or-under-commit statement in fundamentals or
quota-semantics.

## How the pieces compose (read before implementing any single one)

The P0 specs share machinery and must be implemented as a set, in this order:

1. **R5 + R6 first, together** — they are one artifact (a ValidatingAdmissionPolicy
   on pods). R6 makes jobtree mandatory for GPU pods; R5 makes jobtree-owned
   fields (`payer-*`, `lease-reason`, `cohort`, `schedulerName=jobtree`) settable
   only by the controller's ServiceAccount. R5's plugin-side defense-in-depth then
   layers on top. Doing this first means R1–R3 can assume pods are trustworthy.
2. **R2** — the Permit width accounting (count already-minted siblings toward the
   gang), gang-state reconstruction on restart, and the controller width-check
   before adopting Running. This is the load-bearing one.
3. **R1** — pending-lease clearing + gang GC. Shares the `PostBind` hook and the
   stale-gang sweep that R2 introduces, so land it right after R2.
4. **R3** — reconcile the opportunistic fork; depends on R5/R6 (its emitted pods
   must carry trustworthy provenance) and on R2 (adopt-at-correct-width).
5. **R4** — perf; last, because R1 removes the phantom-lease growth that is half
   the hot-path cost, and R4's caching must not reintroduce the decide→mint
   overspend window R1 closes.

## P2–P5 sequencing (roughly priority order, with the real couplings)

- **R9 is the pivotal fork** — Option A (finish JobSet lowering) *subsumes* R8 and
  part of gang co-termination; Option B (direct-inject rendezvous) leaves R8
  separate. Decide R9's A/B before starting R8, since it determines whether R8 is
  its own change.
- **R21 + R22 + R25 land together** — all three are bugs in the one
  `HandleNodeFailure` swap path; fixing any without the others re-touches the
  same code.
- **R26 (ledger auditor) is independent** of everything except that its
  swap-grace window must respect the swap flow R21/R22/R25 finalize; it can be
  built in parallel and is the backstop for R8/R25-class leaks.
- **R11 before R2/R8's status writes** — the condition taxonomy should exist so
  `Degraded` (R2) and `Failed` (R8) emit through it, not as ad-hoc strings.
- **R12 shares R5's pod OwnerReference** — do that edge once.
- **R18/R19 are cheap and unblock the OSS/admin story** — do them anytime; R19
  (license) is a legal decision, so surface it early.
- **R10, R15, R16, R17, R24 are mechanical** — parallelizable Sonnet work, no
  dependency on the design specs.

## Decisions that are David's, not Fable's

Collected here so they are not lost in the specs. Each is also flagged inline.

**Two are now decided (2026-07-09).** See [SIZING.md](SIZING.md) for what they cost:
- ✅ **R9 = Option A** — finish the JobSet lowering. It subsumes R8 and the JOBSET track.
- ✅ **R13 = clean break.** *"Never complicate the implementation to support side by
  side. If there is a breaking change, we'll schedule it, stop the jobs, and
  restart."* This is a **project-wide policy**, not an R13 detail: no dual-read
  windows, no conversion webhooks, no migration Jobs. It also settles R4 pt2b's
  persistence location (Budget `status`) and frees R2 pt3 to label leases freely.

- **R3**: whether the "promised-but-unfunded opportunistic start" survives at all,
  or is dropped. Recommendation inside: keep it, but route it through the plugin
  with an explicit authenticated `Promise` marker (no controller mint). **(Decided
  + shipped 2026-07-08: kept, via the Promise path.)**
- **R6**: `failurePolicy` for the mandatory-scheduler policy — `Fail` (safe, but a
  policy/webhook outage blocks all GPU pods) vs `Ignore` (available, but a gap
  during outages). Recommendation inside: `Fail`, with the jobtree control-plane
  namespace exempted.
- **R7**: ✅ **DECIDED — the tenant is the NAMESPACE.** One kind of principal (a
  project is a principal like a user); a team is a *group*, not a principal;
  *"permissions flow with accountability"*; **the namespace pays**; the
  namespace→tier binding is admin-set. Family sharing and sponsor lending **do**
  cross namespaces, along admin-declared edges only — overruling the original
  recommendation. `Run.Spec.Owner` is **deleted**, and R7 needs no new admission
  machinery. See [R7-tenancy-amendment.md](R7-tenancy-amendment.md).
- **R8**: ✅ **DECIDED — per-role, default `Fail`**, with `Retry(n, backoff)` and
  `Ignore` opt-in. Implemented as phase 9A-3 of the amended R9; absorbed there but
  built to R8's own spec, not inherited from a JobSet `failurePolicy`.
- **R9**: ✅ **DECIDED — Option A**, then **re-scoped** by
  [R9-jobset-amendment.md](R9-jobset-amendment.md) (Fable, 2026-07-09): borrow
  JobSet's **design**, not its controller. A real JobSet creates **Jobs**, whose
  pods the *batch Job controller* creates — so R5's VAP would have to trust an
  identity that stamps out pods from every tenant's Job template, reopening the
  cross-tenant charge with one hop. It would also force a *permanent* dual
  pod-creation path (JobSet has no funded, workload-less spare). Our controller
  stays the sole pod creator. Cancels the JOBSET track's XL. R8 is absorbed as
  phase 9A-3 **at its own L cost** — we build the failure edge, not inherit it.
- **R13**: ✅ **DECIDED — hard rename, no side-by-side.** Kind name still open
  (`GPULease` recommended). No dual-read window, no conversion webhook, no migration
  Job; a breaking change is scheduled, jobs stopped, and restarted. R15 established
  there is no production install to migrate.
- **R19**: ✅ **DECIDED — no licence yet.** *"I'm not ready to give this away yet,
  but I want to be able to talk about it."* Explicit `LICENSE` reserving all rights;
  public source for reading and discussion only. Governance made **real and minimal**:
  a truthful one-person `MAINTAINERS.md` and a `SECURITY.md` using GitHub private
  vulnerability reporting — **no email published**. Apache-2.0 remains available later.
