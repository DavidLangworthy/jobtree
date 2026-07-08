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

## Status board

| Spec | Finding | Design | Code (Opus) | Verify (Sonnet) |
|---|---|---|---|---|
| [R1](R1-phantom-lease-clear.md) | Phantom `pending` lease funding leak | ✅ done | ⏳ pending | ⏳ pending |
| [R2](R2-gang-recovery.md) | Partial-gang wedge / restart / adopt-at-partial-width | ✅ done | ⏳ pending | ⏳ pending |
| [R3](R3-opportunistic-fork.md) | Opportunistic activation incoherent post-cutover | ✅ done | ⏳ pending | ⏳ pending |
| [R4](R4-plugin-hotpath.md) | Permit hot-path relists + unbounded ledger replay | ✅ done | ⏳ pending | ⏳ pending |
| [R5](R5-provenance-trust-anchor.md) | Forgeable funding provenance (swap mint) | ✅ done | ⏳ pending | ⏳ pending |
| [R6](R6-mandatory-scheduler.md) | Budget is opt-in for GPU pods | ✅ done | ⏳ pending | ⏳ pending |
| [R7](R7-tenancy-envelope-namespace.md) | Namespaces are not a tenancy boundary | ✅ done | ⏳ pending | ⏳ pending |

R1–R4 are P0 (correctness at the new committer). R5–R7 are P1 (multi-tenant
safety). P2–P5 from the audit are not yet designed.

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

## Decisions that are David's, not Fable's

Collected here so they are not lost in the specs. Each is also flagged inline.

- **R3**: whether the "promised-but-unfunded opportunistic start" survives at all,
  or is dropped. Recommendation inside: keep it, but route it through the plugin
  with an explicit authenticated `Promise` marker (no controller mint).
- **R6**: `failurePolicy` for the mandatory-scheduler policy — `Fail` (safe, but a
  policy/webhook outage blocks all GPU pods) vs `Ignore` (available, but a gap
  during outages). Recommendation inside: `Fail`, with the jobtree control-plane
  namespace exempted.
- **R7**: whether budget family-sharing and sponsor lending may cross namespaces.
  Recommendation inside: envelopes are namespace-scoped; cross-namespace funding
  only via an explicit sponsor ACL that names the lender namespace.
