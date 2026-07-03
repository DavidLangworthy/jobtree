# fundamentals.md ↔ implementation gap analysis

*Analysis only — this document recommends changes; it does not make them. Basis: `main` after the
full remediation chain (R1–R29) merged, including R14/R15 quota semantics. Goal: move **both** the
doc and the code forward and end up with the best of each. Not everything belongs in fundamentals;
intentional abstractions are called out as such, not as defects.*

`docs/fundamentals.md` describes the **RQΛ calculus** — the quota/topology-aware scheduling model.
It is a genuinely good spine: Cover-before-Pack, `Pack = None → reservation`, the attested
deterministic lottery, aggregate caps bounding envelope sums, and the family-proximity walk shared
between cover and ranking are all faithful to the code and are the backbone the R14/R15 work builds
on (finding-set E below). The gaps concentrate in three places:

1. **§2 (syntax) and §6 (semantics) contain a cluster of constructs that were never built** — the
   compiled-DAG combinators, `compPath`, checkpoint-restart, and the partial-spare "opportunistic
   fill" splice. These read as implemented but are inert.
2. **§4 (Cover) and parts of §6 predate `quota-semantics.md`** and now describe the *old* funding
   model. The four derived funding classes, metered evaluation, demote-not-kill, owner recall, and
   the `unfunded → spares → shrink → lottery` reclaim order are absent, and several concrete claims
   (Borrowed as a role, `E[duration]` integral, "owned/borrowed indistinguishable") are now false.
3. **Two framing choices need a decision** (§7 "no priorities"; §1/§6 event-ledger) because the
   code deliberately does something the doc's language rules out.

The rest of this document is organized by what to *do*: two decisions first, then fix-the-doc,
add-to-the-doc, maybe-build, and preserve-as-is.

---

## 1. Two framing decisions to make first

These are not typos — they are places where the implementation intentionally does something the
doc's language forbids. They should be resolved before editing §4/§6, because the wording of those
sections depends on the answer.

### D1. "No priorities" vs. the funding ranking *(§7 line 104; also §4 line 52, §9 line 122)*

Fundamentals asserts, as a named property: *"survival decisions depend only on feasibility,
structural cuts, and the attested uniform lottery seed — no numeric priorities influence outcomes."*

The implementation now has a **total deterministic ranking** that decides survival *before* any
lottery: per envelope, claims are ordered by `(numeric tier: Owner=1 < Child=2 < Sibling=3 <
Cousin=4, then admission time, then run name)` and greedy-filled; whatever fits is funded, the rest
is unfunded, and the consolidated cut order is `unfunded → spares → shrink → lottery`. The lottery
only breaks ties *within* a rank class. (`pkg/funding/funding.go:42-48,213-221`;
`pkg/resolver/resolver.go`; `quota-semantics.md:9,71-81` — *"claims are ranked, not labeled … the
normative core"*.)

Both statements are half-true: there is still **no user-assigned numeric priority knob**, but
"survival depends only on feasibility + lottery" is false — a deterministic proximity/recency
ranking is the *primary* determinant and the lottery is last. **Recommendation:** keep the property
but split it — "no user-set priority values; survival is decided by a deterministic
proximity-and-recency ranking (funded vs. unfunded), with the attested lottery breaking ties only
within a rank class." This is the single most misleading line in the doc; confirm the reframing.

### D2. Event-ledger framing vs. CRD-state reality *(§1 lines 40-41; §6 line 70; §7 line 105; §9 line 123)*

Fundamentals says the global state `Σ` is *"a finite sequence of immutable events … 'Live' cluster
state is a pure function of Σ,"* that `Σ ⟶ Σ'` means *"append events to the ledger,"* and that `Σ`
is *"append-only."*

There is **no event ledger.** Live state is Kubernetes CRD objects reconciled in place: the engine
loads a mutable `ClusterState` snapshot, mutates it, and the Bridge applies a *non-atomic*
per-object diff (`controllers/run_controller.go:32-39`; `controllers/kube/bridge.go:38-81`); the
CLI reads a `cluster-state.json` snapshot, not an event stream. The *immutable-facts* spirit does
hold for leases (spec immutable, closed via status), and the funding classification genuinely **is**
a pure function of `(budgets, leases, clock)` — but Run/Budget/Reservation status are derived
caches, not a replayed `Σ`.

**Recommendation:** keep the auditable-by-replay spine but reframe to match Decision 3 of
quota-semantics.md: *leases are the immutable consumption facts; classification is a pure function
of `(budgets, leases, clock)`; Run/Budget/Reservation status are non-authoritative caches reconciled
in place.* This preserves auditability without claiming an append-only log that does not exist.
Confirm whether the event-sourced language is aspirational (and should be softened) or a real target
(and should be filed as future work).

---

## 2. Stale — fundamentals now contradicts the code *(fix the doc)*

These are wrong today. All should be corrected in fundamentals; the code is the source of truth
(and is backed by `quota-semantics.md`).

| # | Section | Claim in fundamentals | Reality (evidence) |
|---|---------|-----------------------|--------------------|
| S1 | §2 l.35, §9 l.121 | Lease `role = Active \| Spare \| Borrowed` | Roles are **Active \| Spare** only. owned/shared/borrowed/unfunded are *derived classes*, never stored (`pkg/binder/binder.go:26-27`; `pkg/funding/funding.go:19-36`). Node-failure closure reason is `NodeFailure`, not `Fail` (`run_controller.go:685`). |
| S2 | §4 l.60 | "Borrowed and owned leases are indistinguishable once funded" | The opposite: four classes are deliberately distinguished in status, metrics, and **reclaim exposure** (owned = general phases only; shared = owner-recallable; borrowed = contractual, never recalled; unfunded = first cut). `pkg/funding/evaluate.go:559-581`; `quota-semantics.md:99-111`. |
| S3 | §4 l.54 | Candidate order "location-first: owner's, then siblings, then parent, repeat for other locations, then sponsors" | Proximity-**major**: `own → parents → siblings → cousins` (parents **before** siblings; cousins missing from doc), same-location pass before cross-location *within each tier*, sponsors last. `pkg/cover/cover.go:104-127`; `quota-semantics.md:48-51`. |
| S4 | §4 l.58, §1 | Integral `usedGPUHours(e) + Δk·E[duration] ≤ B_e` | `E[duration]` does not exist. Admission uses **width × period** against the remaining integral (`period` = configurable 24h accounting horizon, also the eval cadence; leases stay open-ended). `pkg/funding/admission.go:88-104`; `evaluate.go:13-19`. |
| S5 | §4/§7 l.103 | "Family-first borrowing … all borrowing obeys envelope caps (C,B) and aggregate caps" (one gated mechanism) | Two mechanisms: **family/shared** (no lending policy, no ACL, not bound by lending caps; bounded by the lender envelope's own caps via ranking; **recallable**) vs **sponsor/borrowed** (ACL + lending caps; **contractual, never recalled**). "Borrowing" is the wrong word for family. `cover.go:120-198`; `evaluate.go:466-504`. |
| S6 | §6 l.82-86, §8 l.114 | Cut order "spares → shrink → lottery" | A phase-0 the doc omits fires first: **`unfunded → spares → shrink → lottery`**. As written the doc implies a healthy funded lease could be lotteried while filler survives. `pkg/resolver/resolver.go`; `quota-semantics.md:108-111`. |
| S7 | §2 l.18, §3 l.44 | Envelope has a *mandatory* `window=[start,end)`; typing `B ≤ C·\|W\|` | Start/End are **optional** — envelopes are open-ended by default; the `B ≤ C·\|W\|` check runs only when both are set. Open-ended is a first-class R14 concept (window expiry coasts opportunistic). `budget_types.go:58-59,232-248`. |
| S8 | §5 l.64 | "Fill one domain at a time with **whole-node** groups" | Packing is at **GPU granularity**: `allocateInDomain` takes `min(free, remaining)` per node; a node is shared across groups/runs. The "stays within one fabric domain" half *is* enforced. `pkg/pack/pack.go:291-329`. |
| S9 | §5 l.67 | "Malleable INCR components may admit **partial placements**" in Pack | `pack.Planner` is **all-or-nothing** on the width it is handed. Partial width comes from (a) partial **funding** in cover and (b) the controller's incremental **grow** loop — not from Pack. `pack.go:62-84`; `evaluate.go:555-581`. |
| S10 | §6 l.82,94 | Shrink "by stepGPUs down to minTotalGPUs" | Shrink cuts **whole placement groups** (subject to the `minTotalGPUs` floor); only **grow** is `stepGPUs`-bounded. `resolver.go:401-421`; `run_controller.go:1203-1218`. |
| S11 | §3 l.23,45 | Aggregate-cap `maxC` required; typing `maxB ≤ maxC·Σ\|W_e\|` | Both `maxC` and `maxB` are **optional**; the validator never checks the integral relation. `budget_types.go:83-89,186-213`. Mark `maxC` optional and the typing rule informative-only. |
| S12 | §9 l.120 | Reservation "Create → Activate → Released" | Actual: **Pending → Released** (reason `Activated`), or **Pending → Failed** (terminal, omitted by the doc). `run_controller.go:380,400,421-423`. |
| S13 | §6 l.84 | Lottery seed `ρ = H(scope, Res.id, t)` | Seed is `H(seedSource, t.UnixNano)` — **scope is not an input**; seedSource is the reservation id (activation) or run key (admission reclaim); attested via the lease closure reason, not reservation status. `resolver.go:571-575`. |
| S14 | §7 l.100, §3 l.49 | "no two active leases overlap a **node**" | Exclusivity is at **GPU-slot** granularity (`nodeName#ordinal`); nodes are deliberately shared. Reword to "no two active leases overlap a GPU slot." `binder.go:233-245`; `pack.go:315`. |

---

## 3. Aspirational — fundamentals describes behavior that was never built

These read as implemented but are inert. Each is either **fix-the-doc** (mark as notation / not-yet)
or **candidate for implementation** if the behavior is actually wanted — that is David's call.

- **A1. Compiled job-DAG + combinators SHARD/INCR/AND/SEQ** *(§2 l.27-28).* No combinator or
  compiled-DAG type exists; SHARD/INCR/AND/SEQ are pure notation, and the `D = ⟨…perRank…, replace =
  SameDomain|Restart, …⟩` tuple has no counterpart (`grep` returns nothing; the real surface is
  `RunResources`/`Locality`/`Malleability`, `run_types.go:33-56`). **Rec:** present the combinators
  explicitly as descriptive notation and replace the `D` tuple with the actual fields, *or* file the
  DAG-rewrite engine as future work.
- **A2. Checkpoint-restart / abort-and-requeue** *(§6 l.90; §2 l.26).* With no in-domain spare,
  `HandleNodeFailure` marks the run **terminally Failed** — no reservation, no checkpoint restart.
  `RunRuntime.Checkpoint` is a declared-but-unused CRD field (`run_controller.go:688-694`;
  `run_types.go:47`). **Rec:** mark the requeue-at-checkpoint clause not-yet-built; the honest
  behavior today is terminal failure. Candidate for impl if checkpoint restart is desired.
- **A3. `compPath = π` on leases** *(§2 l.35).* `LeaseSpec.CompPath` exists but is never populated
  or read (only the struct + deepcopy). It has no meaning until the DAG (A1) exists.
  **Rec:** drop `compPath` from the lease tuple or mark it reserved (§9 already omits it).
- **A4. Partial-spare "opportunistic fill"** *(§6 l.96-97).* No mechanism seats another run onto a
  spare's held nodes — spare nodes are counted as used and are surrendered only wholesale by the
  resolver's `DropSpare` phase (`run_controller.go:965-983`; `resolver.go:103-125`). R14 *generalizes*
  this: over-quota runs and filler are the same **unfunded** class. **Rec:** rewrite §6's block to
  describe the unified unfunded class + consolidated reclaim order, replacing the unbuilt splice.

---

## 4. Missing — real behavior fundamentals should document *(add to the doc)*

The entire R14/R15 funding model is absent. Adding a compact funding section plus a handful of
half-sentences closes most of this. In priority order:

- **C1. The funding model itself (headline).** "Cover fails" no longer means "no lease" — it means
  **unfunded lease**: the run keeps its GPUs, is metered in a separate visible bucket with **no
  overdraft**, and is **re-funded automatically** when quota returns; admission applies the
  width×period lookahead so work is not *born* opportunistic. Add a short section: funded vs
  unfunded, the four-class + reclaim-exposure table, demote-not-kill, separate meter, no overdraft,
  automatic re-funding. Restate §7 budget-compliance as two clauses (*funded never overdrafts;
  excess is metered unfunded*). `pkg/funding/*`; `quota-semantics.md:19-44,99-111`.
- **C2. Owner recall as re-ranking** *(not a where-to-look step).* An owner's claim outranks family
  borrowers; `AvailableWidth` treats junior family width as recallable headroom. **Recall frees
  nothing physically** — eviction happens later only if the GPUs are actually needed.
  `evaluate.go:785-863`; `quota-semantics.md:46-55`.
- **C3. Reservation activation is two-axis** *(sharpened R7).* A **pure budget shortfall** (physical
  deficit 0) never cuts anyone — the run starts unfunded and re-funds later; an **unfundable**
  physical deficit parks; the **lottery over funded work** is entered *only* for a physical-capacity
  deficit backing fundable demand. Fundamentals' single "apply cuts up to a lottery" rule would
  authorize preempting funded work for a budget-only shortfall, which the code refuses.
  `run_controller.go:513-590`; `quota-semantics.md:127-128`.
- **C4. `[Plan-Later]` reclaims unfunded first.** A funded admission that fails packing runs
  `reclaimForAdmission` (clears the deficit from **unfunded** leases only, when the demand is itself
  fundable) and binds *now* if that succeeds — reserving only if reclaim frees nothing.
  `run_controller.go:154-168,251-296`.
- **C5. Envelope/run fields that gate the model but are undocumented:** `sharing` (`""`/`family`/
  `none` opt-out, `budget_types.go:46-49`), `preActivation.{allowReservations, allowAdmission}`
  (admit before the window; funds when it opens, `budget_types.go:64-72`), `Budget.Spec.Parents`
  (the family DAG the whole proximity/recall order derives from), the surface run `funding =
  {allowBorrow, maxBorrowGPUs?, sponsors}` (without it §4's sponsor path is unreachable), and
  `Run.Status.Funding` (the derived four-class + per-lender breakdown). Add these to the §2 tuples
  and the §9 mapping.
- **C6. Classification is clock-dependent.** Integrals accrue/exhaust and windows open/close, so a
  Running run's class can flip with **no CRD edit** — this is *why* recovery is automatic and status
  is a cache. (Implementation compensates with a resync + budget→run fan-out; that plumbing stays
  out of fundamentals.) `evaluate.go:305-317`.
- **C7. Budget compliance holds only under serialized admission.** Every engine-driving reconciler
  runs `MaxConcurrentReconciles=1` behind one Bridge mutex, reads bypassing the informer cache
  (concurrent admissions from stale snapshots would overspend envelopes — `BudgetConservation.tla`);
  apply is **non-atomic** (the R28 risk, handled by an adopt-open-leases fallback). One sentence
  makes the headline property's precondition explicit. `reconcilers.go:27-31`; `bridge.go:38-81`.
- **C8. `desiredTotalGPUs` + partial funding** *(§2 malleable).* `RunMalleability.DesiredTotalGPUs`
  (defaulting to max) is the real grow target; and a malleable run may be **half-funded /
  half-opportunistic** (funded lowest-group-first up to what quota affords). This is where
  malleability meets demote-not-kill. `run_types.go:51-56`; `evaluate.go:555-581`.

---

## 5. Correctly simplified / gets right — preserve

Not defects; called out so future edits **extend** rather than rewrite these.

- **Backbone that matches the code exactly** (E, finding 33): Cover-before-Pack; `Pack = None →
  Plan-Later reservation`; the attested reproducible lottery seed; aggregate caps bounding envelope
  sums; the proximity walk shared between cover and the ranking; the reservation fallback. Any edits
  should layer the funding classes / ranking / unfunded phase **onto** these, not replace them.
- **Cross-object judgments enforced at runtime, not by webhooks** *(§3 l.48-49).* Selector match,
  C/B bounds, window coverage, and exclusivity are Cover/Pack/resolver concerns — a per-object
  webhook cannot see the payer envelope or other leases. Worth one clarifying clause so readers
  don't expect the CRD validator to reject them. `reservation_types.go:92-106`; `lease_types.go:89-108`.
- **Swap-lease funding provenance and `AutoRenew`** *(§6 l.89; §2).* The promoted swap lease
  inherits the spare's payer facts, but that is a *corollary* of "class is derived from the payer"
  and needs no separate mention once C1 lands. `AutoRenewSchedule` is a declared-but-inert stub;
  window-reopen re-funding falls out of evaluation arithmetic, not AutoRenew. Both correctly out of
  scope — everything does not need to be modeled.

---

## 6. Suggested sequencing (when edits are authorized)

1. **Decide D1 and D2** (§7 no-priorities; §1/§6 ledger framing) — they set the vocabulary.
2. **Add the funding section (C1)** and the four-class model; this is the largest single change and
   most of §4/§6 hangs off it.
3. **Correct the stale claims (S1–S14)** — mechanical once C1 exists.
4. **Slot in the missing fields and rules (C2–C8)** as half-sentences in the existing sections.
5. **Resolve the aspirational block (A1–A4)** — decide per item: mark-as-notation vs file-as-impl
   follow-up. The only one with plausible product value is **A2 checkpoint-restart** (today a
   node failure without a spare is a terminal loss); the rest are best handled by softening the doc.

Net: fundamentals stays small and gains the R14/R15 model; the code gains a short list of candidate
follow-ups (checkpoint-restart, and whether the event-ledger framing is a real target) rather than
silent divergence.
