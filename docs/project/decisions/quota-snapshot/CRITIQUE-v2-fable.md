# CRITIQUE v2 — consequence seat (Fable, `claude-fable-5`)

**Of:** `docs/project/decisions/quota-snapshot/DESIGN-v2.md` at `20d42cd` (branch `design/quota-snapshot`),
under `OWNER-RULINGS.md` (thirteen rulings) and `REVISED.md` (Rulings 7 and 8). **Code cited against
`origin/main` = `ba5652e`** — verified the branch differs from it only in docs and one deleted
conformance test, so working-tree line numbers are main's. Binding text read in full:
`quota-semantics.md` (per `AGENTS.md:176`), both first-pass critiques. Written blind to Sol's second
pass. Nothing executed; every claim is from reading the tree.

**Verdict in four sentences.** The revision is real where it claims to be: Ruling 7 genuinely severs
the two moves my first pass separated, the lease→principal join is a named build item with the right
fields, and the alarms-are-vapour admission (§5) is the honest sentence v1 lacked. Ruling 10's
coverage claim survives my strongest pattern attacks *as a values choice* but not as the engineering
inevitability the ruling presents: its factual inventory of the code is wrong in two checkable
places, its "bounded" claim is conventional rather than structural, and it silently moves the
Ruling-6/9 honesty burden onto a meter the design still computes from current spec — which cannot
satisfy them. The worst finding is a compound nobody has looked at: **Ruling 8's quarantine and
Ruling 10's mandatory windows together produce exactly the innocent-tenant funding loss each ruling
individually forbids** (§3 below). And v2 drops, without flagging it, an explicit owner requirement
that v1 carried: Ruling 3's "you're over by" counter and published odds appear nowhere in the
document (§7).

---

## 1. Ruling 10, attacked where it is load-bearing

### 1a. The pattern search, reported honestly

I tried to break "concurrency × mandatory window + three burst patterns covers every real
allocation pattern" and here is the full result, failures included.

**Covered, genuinely:** guaranteed-rate-by-deadline ("we must finish this run by Sept 1" — the
concurrency over the window *is* the guarantee); harvest/filler workloads (pattern 1); peer loans
(`LendingPolicy.MaxConcurrency`, `api/v1/budget_types.go:87`, real and enforced at
`evaluate.go:855`). The claim holds for everything whose need is a *rate*.

**Not covered by machinery: the enforceable total-consumption ceiling.** A money-denominated grant
("this partner contract pays for at most 50k H100-hours this quarter") or a compliance ceiling
("this project must not exceed N hours of compute") needs total ≤ X with burst rate ≫ X/window.
Under the model, total = concurrency × window, so the grantor must choose: flatten the rate (no
bursts without a human), or shorten the window per burst (a human per burst — pattern 2), or accept
that the granted total is the full concurrency × window (unbounded relative to X). All three burst
patterns route the ceiling through a person. Ruling 10 knows this and delegates it via Ruling 9's
philosophy — "you are burning more than your share" is a conversation (`OWNER-RULINGS.md:354-359`).
Fine as a values choice, but two things must be said about it:

1. **The ruling's supporting argument over-claims.** Its table (`OWNER-RULINGS.md:315-323`) treats
   every hours *enforcement* as inheriting every hours *hazard* — but the recorded hazards
   (two-epochs, zero/250/500, the GitOps refund, the clamp) are all consequences of making an
   accumulated balance *respond to spec edits*. A monotone, non-refundable meter with a
   demote-at-threshold gate has none of them by construction: the meter never decreases, so a
   re-grant answers zero/250/500 identically always (remaining = threshold − consumed); a resync
   re-applies a threshold and touches no state; lowering a threshold below consumed demotes
   prospectively, which is exactly Ruling 2/6-shaped. Its real costs are the replay memory and the
   discipline that renewal = a *new* envelope (moving a window on the same envelope would
   reintroduce the reset question). Enforcement of a ceiling is *prospective* — it stops future
   spend, takes nothing back — so Ruling 9 does not condemn it. The ruling never priced this
   option; if the answer is still no, it should be no for the stated product reason, not because
   every enforcement design allegedly carries F1/F5/P6.
2. **After Ruling 11 there is no number to have the conversation with.** The metered-only story
   depends on someone noticing the burn, and the deletion removes the only field that could declare
   a threshold to alarm on. Ruling 10's own migration text forbade `maxGPUHours` surviving "as a
   field that looks enforcing and is not" (`OWNER-RULINGS.md:386-387`) — correct — but v2 §1 then
   deletes it without replacing the *reporting threshold* reading it had itself offered
   (`DESIGN-v2.md:46-48`), and the alarm surface is admitted vapour (`DESIGN-v2.md:142-144`). A
   meter with no threshold and no emitter is a graph nobody is looking at when the contractual line
   is crossed at 2am.

**Half-covered: bursts do not compose down the tree Ruling 1 mandates.** Pattern 2 (temporary
windowed grant) works cleanly at depth 1. At Ruling 1's depth — root → manager → lead → researcher
— `INV-SUBTREE-CONSERVE` (`DESIGN-v2.md:123-127`) caps funded concurrency at *every* ancestor, so a
researcher's 512-GPU two-day burst runs funded only if 512 of window-active concurrency exists along
the whole path at that instant. Granting the burst therefore means editing every level of the chain
(or every ancestor permanently holding 512 of slack — which is precisely the over-provisioned
"allocated 64, runs 64 forever" world the ruling set out to kill, now at every interior node). In
the hours world a wide-concurrency, hours-capped ancestor could absorb a leaf burst with one edit at
the leaf. The coverage claim has not been tested at depth, and depth is the trust model.

**"Expiry is the default" is an incentive claim, and the incentives point the other way.**
`INV-WINDOW-REQUIRED` requires `start < end` (`DESIGN-v2.md:114-115`); nothing bounds `end − start`.
`end: 2099-01-01` satisfies the invariant and reproduces the forever-envelope exactly. After one
team gets burned by a lapse (§5 below), far-future end dates are the *rational* authoring response,
and the ruling's claim that mandatory windows make the model "bounded rather than merely
conventional" (`OWNER-RULINGS.md:348`) inverts: it is bounded merely by convention, now with an
invariant lending the convention false authority. Either bound window length as cluster policy (a
new, unstated rail) or strike the "bounded" claim.

### 1b. The ruling's code inventory is wrong in two checkable places

Neither error changes the conclusion; both change the migration, and both show the inventory was
asserted rather than verified — which matters when a ruling this load-bearing binds everything else.

- *"Setting hours already requires a window (`budget_types.go:286`)"* (`OWNER-RULINGS.md:337-338`)
  is **backwards**. The branch at `api/v1/budget_types.go:286-290` is the one that *tolerates*
  windowless hours — it checks only non-negativity when `Start` and `End` are both nil. Worse, a
  half-windowed envelope (`Start` set, `End` nil) matches neither that branch nor the
  concurrency×window rail at `:274-285`, so its hours are validated against nothing at all.
- *"Exactly three enforcement sites"* (`OWNER-RULINGS.md:339-341`) undercounts. Beyond `:125`,
  `:858`, `:866` and the acknowledged `nextDepletion` (`:955`, `:960-961`, `:964-966`): the
  admission lookahead in `AvailableWidth` reads hours at **four** more gating sites
  (`pkg/funding/evaluate.go:1167`, `:1171`, `:1194-1196`, `:1215-1217`) — these bound *admitted
  width*, the born-opportunistic protection of `quota-semantics.md:23-26` — plus the accrue clamps
  (`:993-997`, `:1024-1027`) and `pkg/funding/admission.go:89-136`, which exports aggregate hours
  headroom in the admission surface.

### 1c. The meter inherits the lies, and the design's own text says it may not

Ruling 10 closes with: "Rulings 6 and 9 still govern the metered record, demoted from funding
correctness to reporting honesty" (`OWNER-RULINGS.md:378-379`). Hold v2 to that sentence.
`ConsumedGPUHours` is defined as replayed accrual "within the envelope's **current** window", with
the release-on-renewal arithmetic riding on current-spec clamping (`evaluate.go:104-108`), and v2
§9 keeps that machinery, relabelled "metering statements, not funding ones" (`DESIGN-v2.md:208-209`).
But under "not a series" (`DESIGN-v2.md:92-96`) the scheduler holds no past spec — so a window edit
still rewrites the reported per-window hours, which is Ruling 6 violated *in the meter*, the exact
defect class Ruling 9 just ruled a bug in the clamp (`OWNER-RULINGS.md:289-296`). Fixing the clamp
(§6 item 7) fixes one instance of the class and leaves the class. The fork is forced: either
(a) class- and window-attributed metering moves downstream to the audit store that holds the version
series (the demoted Postgres, `REVISED.md:46-49`), and the scheduler's per-window `ConsumedGPUHours`
and its status fields are deleted along with the cap; or (b) the scheduler retains history for the
meter — contradicting "not a series". Note the honest boundary: *raw* per-team hour totals are
spec-free (pure integration of lease intervals) and can stay anywhere; it is funded-vs-unfunded
attribution (`HoursByClass`, the visible unfunded bucket of `quota-semantics.md:35-37`) and window
bucketing that need historical spec. v2 chooses neither branch and currently ships (b)'s output
computed with (a)'s memory.

Related, unstated, and in the design's favour: with no enforced integral there are **no depletion
crossings**, so the segmented replay (`evaluate.go:407-432`) leaves the *funding* path entirely —
classification needs one fill at Now. The admission hot path's replay cost (the signal at
`cmd/scheduler/plugin/gang.go:334`) disappears from funding. That is a far larger simplification
than deleting three gates, it is the strongest engineering argument *for* Ruling 10, and the design
never states it — §6 prices the producer and the join but not the engine becoming memoryless.

### 1d. One more uncovered question the metering-only world makes central

Are unfunded hours charged back? If yes, pattern 1 ("run and hope") bills teams for work that had no
protection; if no, running everything opportunistic is free compute for any cost-sensitive team and
the meter stops meaning anything at the org boundary. `quota-semantics.md:35-37` makes the bucket
visible and separate but does not price it. Under enforcement this was a reporting nuance; under
metering-as-the-product it is the product. Unanswered anywhere.

## 2. The producer is the entire trust boundary — what no invariant can express

Ruling 7 moved the producer in-cluster and my first-pass §7 argued for exactly that; the adoption is
real. But the point I made about the external control plane survives the move in a sharper form,
because **authorization is a property of the write event, not of the document state**, and every §3
invariant checks document state. A compromised, buggy, or merely confused in-tree producer emits a
perfectly rooted, injective, acyclic, window-satisfying snapshot in which the victim's namespace UID
is bound to the attacker's principal. Enumerating precisely what the producer must enforce that no
listed invariant can see:

1. **Edge and envelope authorship containment.** A change inside P's subtree must have been caused
   by a write its grantor was entitled to make. The CRD carries no authorship signal Ruling 1 will
   accept: `Spec.Owner` is self-named (`api/v1/budget_types.go:30-31`), `Spec.Parents` is
   child-asserted (`:36`) — the rulings say so themselves (`OWNER-RULINGS.md:21-23`). The only
   forge-proof in-cluster signal is object *location* plus RBAC — `metadata.namespace` is
   API-server-authenticated, the same argument the promise-provenance check already leans on
   (`cmd/scheduler/plugin/gang.go:768-776`). **Consequence: Q1 is mislabelled.** v2 files
   grantor-side `Grants` versus a binding object under "authoring ergonomics and RBAC shape only"
   (`DESIGN-v2.md:188-190`). It is not ergonomics — grantor-side authoring is the *only* branch
   under which the producer can derive authorization from RBAC at all; child-asserted `Spec.Parents`
   cannot be made safe by any compiler check, because the compiler cannot see who wrote it. One of
   Q1's two branches is the containment mechanism and the other is a hole.
2. **Roots pinned outside the data they authorize.** `"roots"` is inside the compiled document
   (`DESIGN-v2.md:61`). In-cluster the fix costs a producer config flag, but nothing says it, and a
   producer that derives roots from the Budgets it reads makes `INV-ROOTED` circular. Sol's
   first-pass point; survives the move; still unadopted.
3. **Name→UID truthfulness.** `boundNamespace.uid` must be resolved against the live namespace at
   compile time. A document can carry any UID and every invariant passes. Producer-only duty,
   stated nowhere.
4. **Emitter-side version discipline.** `INV-SNAP-MONOTONE`/`INV-SNAP-IMMUTABLE` are consumer-side
   *rejection*; the producer must never mint the same version twice with different content across
   restarts or split-brain (two replicas without leader election are a republication engine). Needs
   durable version state or derive-from-stored-object, and a specimen.
5. **Quarantine adjudication is an authorization question.** When injectivity fails *across*
   subtrees — hostile H binds victim V's namespace UID — the collision is symmetric and the document
   cannot say who is wrong. Quarantine-both means H freezes V's pending updates (renewals included —
   see §3's fuse) by authoring garbage: F2's squat reborn as denial-of-update. Only authorship
   breaks the tie.

Where does the design state any of this? §6 item 1 says the producer "validate[s]"; §6 item 5 names
"a producer-authorization specimen (a grant authored outside the author's subtree must not compile
in)" (`DESIGN-v2.md:160-163`). That is a *test named for a rule the design never states* — there is
no `INV-GRANT-AUTHORIZED`, no definition of "the author" of a compiled edge, no named signal that
makes authorship checkable. A specimen for an unstated rule tests the implementer's guess. My
first-pass sentence stands, unadopted and still required: **the producer's write-authorization is a
correctness dependency of this design; until it exists, with its own invariant and its own
specimen, F2 is open — not closed — under this design.** The fail-closed table (§5) needs the row it
is missing: "producer emits a well-formed wrong document" — currently the answer is
`INV-VERSION-PINNED-AT-MINT` for forensics and my still-absent consumer-side pin (first pass §8.5)
for repair; versions only go forward, so the 3am repair for a valid-but-wrong publish remains
"publish N+2 through the producer that just published the bad one."

## 3. Ruling 8's quarantine, walked on consequences

**Can a quarantined subtree keep funding work it should no longer fund? Yes — for an unbounded
time, and the time is controlled by the party that benefits.** Quarantine holds last-good
(`DESIGN-v2.md:135`; `REVISED.md:178-179`); nothing bounds its duration; it ends when someone
repairs the subtree, and the party with repair access is the subtree's own team — the party whose
funding the pin preserves. To dodge your own cut, break your own subtree: any team can make its own
principals invalid at will, and a well-timed duplicate binding freezes an incoming reduction
indefinitely. Sol's first-pass "last-good is fail-open for revocation" (CRITIQUE-sol §5) is not
fixed by localization; it is *sharpened* from collateral damage into a targetable, self-serve move.

**Who is hurt: the transfer recipient — the exact person Ruling 3 built its lever for.** Director
moves 64 from M to S; anything in M's subtree fails validation; M pins at funded-64 with earlier
admission times. Under `INV-SUBTREE-CONSERVE` at the shared root plus the ranked fill's
admission-time ordering (`quota-semantics.md:73-81`), S's *new* claims rank junior, so the excess
demotes onto S while M's stale claims keep seniority. Ruling 8's stated reason for last-good was
that unbinding "would turn a neighbour's authoring error into a funding loss for an innocent
tenant" (`REVISED.md:178-179`) — and combined with Rulings 2 and 3, last-good delivers that same
loss to a different innocent tenant, while disabling the one human lever (re-granting) the owner
ruled is how humans express choice.

**The fuse: quarantine × mandatory windows self-destructs on the quarantined tenant too.** The pin
freezes *spec*, not the *clock*. A pinned subtree's envelopes carry mandatory end dates; the renewal
that would extend them cannot land, because new versions do not apply to a quarantined subtree. At
the pinned window's end, `windowActive` goes false (`evaluate.go:634-642`), last-good funds nothing,
and the tenant lands unfunded — first-reclaim class (`pkg/resolver/resolver.go:91-99`) — in exactly
the state Ruling 8 chose last-good to prevent. Every quarantine under Ruling 10 carries a fuse of
length (pinned window end − now), and nothing in the design measures, surfaces, or defuses it. A
hostile neighbour who can *cause* a cross-subtree quarantine (§2 item 5) near a victim's window end
weaponizes this.

**The "same boundary" claim in Ruling 11 is false, and conservation is undefined across the pin.**
"Per-subtree quarantine and per-subtree sharding are the same boundary" (`OWNER-RULINGS.md:424-427`)
— no: quarantine is per-*principal* subtree at any depth; sharding is per-*root* subtree. An
interior quarantine creates a mixed-version lineage *inside one shard*, so §7's load-bearing
property — "ancestor walks... always evaluate against one consistent version"
(`DESIGN-v2.md:174-175`) — is broken by the design's own quarantine mechanism, not by sharding.
What `INV-SUBTREE-CONSERVE` means when the walk crosses a pinned boundary (parent at N+4, child
pinned at N) is specified nowhere.

**Operator surface: does anything say "quarantined and stale since T"? No.** Ruling 8's own
consequence bullet requires per-principal status including "the version it is pinned at"
(`REVISED.md:180-181`); v2's schema carries `status` and `quarantineReason` only
(`DESIGN-v2.md:66-67`) — no `pinnedVersion`, no `quarantineSince`. No alarm (all vapour, §5), no
metric, no kubectl surface; the snapshot is cluster-scoped, so the quarantined *tenant* likely
cannot read the only object that says they are quarantined. Minimum repair set: the two schema
fields Ruling 8 already requires; a producer-written `Compiled`/`Quarantined` condition on the
authored Budget (the R11 Conditions machinery exists, `api/v1/budget_types.go:107-115`) so the
failure is visible in the object the author touched; an alarm on quarantine onset **and on
time-to-pinned-window-end**; and a stated duration policy — escalate-to-unbound after T, or a
one-way pierce (a new version that only *extends `End`* of an otherwise-identical quarantined
principal applies; arguably reductions should pierce too, since reductions are the safety-critical
direction and Ruling 2 makes them legal at any time). Whether last-good survives this walk is a
values question — §9.

## 4. Ruling 11's deletion, enumerated (the design gives it one line)

`DESIGN-v2.md:195-196` disposes of this as "deleted outright, not deprecated." What the schedule
must actually contain, from grepping the tree:

- **CRD fields (three, not one):** `BudgetEnvelope.MaxGPUHours` (`api/v1/budget_types.go:65`),
  `LendingPolicy.MaxGPUHours` (`:89` — the ruling names it only by enforcement line, never by
  name), `AggregateCap.MaxGPUHours` (`:103`). Two generated CRD manifests
  (`config/crd/bases/rq.davidlangworthy.io_budgets.yaml`,
  `deploy/helm/gpu-fleet/crds/rq.davidlangworthy.io_budgets.yaml`), deepcopy, two sample manifests
  (`config/samples/budgets/`).
- **Validation:** `budget_types.go:274-285`, `:286-290`, `ValidateMaxHoursWindow` (`:313-326`) and
  callers; the golden corpus case that *asserts the rail exists* —
  `internal/manifestcorpus/corpus.go:194-199` expects `"maxGPUHours exceeds concurrency×window"` —
  fails the moment the rail is deleted, plus the corpus sample at `:120`.
- **Engine:** the full site list from §1b (thirteen reads across `admit`, `AvailableWidth`,
  `nextDepletion`, `accrue`), not the ruling's three. Plus the hours half of the admission lookahead
  (`quota-semantics.md:23-26`) and `pkg/funding/admission.go:89-136`'s exported headroom.
- **Status:** `EnvelopeHeadroom.GPUHours` (`:153`), `AggregateHeadroom.GPUHours` (`:161`) — status
  fields that look enforcing on every `kubectl get budget -o yaml`; written by
  `controllers/budget_controller.go`. `EnvelopeUsage.ConsumedGPUHours` (`:145`) *stays* (metering) —
  the split between deleted-cap and kept-meter fields needs stating or the deletion will take the
  meter with it.
- **Formal artifacts:** `specs/AccrualPrefix.tla`, both accrual-prefix specimens
  (`pkg/funding/accrual_prefix_counterexample_test.go`), and the entire ledger-compaction campaign —
  three TLA modules and `make ledger-compaction-apalache-check` are named as guards in
  `evaluate.go:44-48`, and the `SettlementHorizon`/`PriorAccrual` machinery (`:49-62`) exists to
  carry the hours integral. With hours unenforced, compaction's burden drops from funding
  correctness to reporting; the campaign must be retired or re-scoped, not left standing guard over
  a dead dimension while everyone believes it guards funding.
- **Tests:** `evaluate_test.go`, `budget_types_test.go`, `quota_semantics_test.go`,
  `budget_controller_test.go`, `pkg/cover/cover_test.go`, `pkg/forecast/forecast_test.go`.
- **Docs with real readers:** `docs/concepts/budgets.md`, `docs/fundamentals.md`,
  `docs/operator-guide/admin-setup.md`, `docs/user-guide/researcher-guide.md`,
  `docs/user-guide/cofunded-runs.md`, and — the one that bites strangers —
  `docs/migrations/kueue.md`, which maps Kueue quotas onto this model for people arriving from
  another system.
- **Binding text, the biggest omission in §9:** v2 amends `quota-semantics.md:41-44` and `:64-81`
  but not **Decision 1 itself**, whose *title* is "GPU-hours are enforced by evaluation"
  (`quota-semantics.md:19`), whose admission-lookahead bullet (`:23-26`) is half-dead, whose
  no-overdraft bullet (`:35-37`) loses its subject, and whose four-classes table charges Owned
  against "envelope concurrency **+ GPU-hours**" (`:103`). Ruling 10 falsifies the heading of R14;
  the amendments list must say so or the repo holds binding text contradicting a ratified design —
  the exact state that produced the P8 oscillation.

## 5. `INV-WINDOW-REQUIRED`: the migration, and 2am on a Sunday

**A sequencing trap the one-line build item (§6 item 2) hides.** If the producer ships while any
envelope is still open-ended, its very first compile fails `INV-WINDOW-REQUIRED` for those subtrees
— which quarantine with **no last-good to hold** (there is no prior snapshot), i.e. those tenants
fund nothing on day one. So the order is forced: webhook-warn → mass-edit → webhook-require → then
producer. And the mass-edit has no good default: dates chosen by hand are GitOps churn across every
team at once; an auto-stamped `end = now + 90d` is a revocation timer nobody chose, detonating
simultaneously for the whole fleet one quarter later.

**The lapse, mechanically.** At `end`, `windowActive` goes false (`evaluate.go:634-642`); running
work demotes to Unfunded — which is "not billed and not closed", **not** "left alone": it is the
resolver's first reclaim class (`evaluate.go:280-286`; `resolver.go:91-99`). Ruling 4 means a quiet
Sunday costs nothing — and means Monday's *first funded admission* names the lapsed team's entire
fleet as the preferred victim pool. Fresh admission is refused. And the repair paths close too: a
scheduler-restart `Reconstruct` cannot delta-fund the gang's unminted remainder ("the survivor
holds", `cmd/scheduler/plugin/gang.go:522-525`), and grow cohorts refuse — so a node failure during
a lapse cannot be replaced and elastic work cannot repair. "A grant that nobody renews ends"
(`DESIGN-v2.md:38-40`) is presented as hygiene; for anything mid-gang it is "cannot heal until a
human wakes up and edits YAML."

**The treadmill has no bell.** Mandatory windows put every envelope in the cluster on a renewal
cadence, and the only notification mechanism in the tree is opt-in: an unset `AutoRenew` yields an
empty `PendingRenewals` *by design* (`api/v1/budget_types.go:119-126`; `quota-semantics.md:141-147`,
which also deliberately forbids auto-extension). So the shipped default under this design is: no
reminder, no auto-extend, silent demotion at `end`, eviction on next demand, and a blocked repair
path — the failure mode of *inattention*, made severe, with the protective knob default-off. At
minimum, `PendingRenewals` must become unconditional with a default `notifyBefore`, and
window-end-approaching must be on the R26 alarm surface (§6 item 6 currently wires binding
conflicts only). Otherwise teams defend themselves with `end: 2099` and §1a's bounded-by-convention
point closes the loop.

## 6. The human test v2 still omits — written out, with what fails it

The prior recommendation carried a section answering "what does each human type"; my first pass
flagged its absence (§5); v2 still has none. Here is what it should say, and what fails each line:

- **Researcher, "why isn't my job running":** `kubectl describe run`. Works today, but the message
  text explains Unfunded by Budget-scan ambiguity — "it has no Budget, or its Budgets name more
  than one owner" (`controllers/run_controller.go:1278-1283`) — which under the snapshot is false:
  conflicts become producer-side quarantine, and the new causes (window lapsed at T; subtree
  quarantined since T at version V) have no sentence. Every such message must be rewritten in
  snapshot vocabulary, against objects the tenant can read. Ruling 7 means `kubectl get budgets`
  still answers "what do I have" — a genuine v1→v2 improvement, credit where due.
- **Lead granting a researcher 8 H100s for two weeks:** `kubectl apply` of *what, where* is exactly
  Q1 — unresolved, and per §2 not an ergonomics question. The grant must now carry `start`/`end`.
  And the failure loop is broken: the webhook validates single-object shape synchronously
  (`budget_types.go:187-217`), but every cross-object invariant is producer-time and asynchronous —
  apply succeeds, the compile quarantines *later*, and the author walks away believing it worked.
  Today's engine has the same defect one layer down — `Conflicts()` computed and consumed by
  nothing (`evaluate.go:176-182`) — and the design is poised to reproduce it one layer up. The
  producer writing a `Compiled`/`Quarantined` condition back onto the authored Budget (§3) is the
  minimum honest loop.
- **Manager renewing:** edit `End`. What tells them it is due: `PendingRenewals`, opt-in — §5's
  default-off bell.
- **Director cutting mid-window:** edit concurrency; effective immediately (Ruling 2), *unless* the
  target subtree is quarantined, in which case the cut silently does not bind (§3) — and nothing
  tells the director which of those two happened. The surface that would: Ruling 3's required
  "you're over by" counter and per-job odds (`OWNER-RULINGS.md:85-86`), with its honesty invariant —
  published odds must be the draw's odds (`:104-107`). **v1 carried `overAllocatedBy`; v2 dropped
  it entirely** — no counter, no odds field, no invariant, not mentioned in the change table. That
  is a regression against an explicit owner requirement, unflagged.
- **Operator at 3am:** one command answering "current version, age, quarantined subtrees, oldest
  pin." Nothing in the design provides it; printer columns on the `QuotaSnapshot` object are cheap
  and unstated. (First pass asked for this surface plus a consumer-side pin verb; the pin is still
  absent — §2.)

## 7. Adoption audit — what my first pass became, verified rather than trusted

| First-pass finding | v2 disposition | Verdict |
|---|---|---|
| Severability (§7) | Ruling 7; in-tree producer; Budget CRD, RBAC, GitOps kept; `staleMax` dead | **Real adoption**, not cosmetic. The architecture in `REVISED.md:17-28` is the one argued for. |
| Lease→principal join (§3) | §6 item 3: `paidByPrincipal` + mint `snapshotVersion`; two-consumers named (`gang.go:795`) | **Real**, with three residues: under §7's independently-versioned shards the singular version field is ambiguous — it must be (shard, version), and a borrowed lease's authority spans two shards; the pinned version is a *record*, not a rail, under not-a-series (the consumer cannot re-check a version it no longer holds); and `INV-OWNED-IS-LOCAL` still compares namespace *names* (`DESIGN-v2.md:128`) while bindings are UID-keyed — the recreated-namespace ambiguity half-survives. |
| "Not an engine rewrite" is false (§1b) | Mooted by Ruling 10 — the versioned replay is deleted, not repriced | **Honest**, but the residual engine change is still unpriced in both directions: funding becomes memoryless (unstated, in the design's favour, §1c) and metering keeps a history requirement the design deleted (§1c, against it). |
| Alarm vapour (§2) | §5 names it a prerequisite; §6 item 6 wires R26 | **Adopted in words.** Emitters/consumers still unnamed; quarantine-onset, pin-age, and window-end alarms absent from the list. |
| `effectiveFrom` seam (§1e) | §6 item 8 | **Adopted.** |
| Specimen successors (§1b) | §6 item 5 | **Adopted**; add the quarantine-fuse specimen (pinned subtree crossing its window end) and the producer-restart monotonicity specimen (§2 item 4). |
| Amendments section (§6) | §9 exists | **Adopted, incomplete** — Decision 1's own heading and three of its bullets are missing (§4). |
| `overAllocatedBy` homeless (§8.1) | **Dropped entirely** | **Regression** against Ruling 3 (§6). |
| Consumer-side pin / rollback (§8.5) | Absent | **Not adopted**; §2's missing fail-closed row depends on it. |
| Schema omissions (§3) | Absent | **Not adopted, and Ruling 11 sharpened it**: `AggregateCap.MaxConcurrency` *survives* the ruling (only the hours half dies) and is enforced today (`evaluate.go:862-864`) with no snapshot field; `PreActivation` becomes *more* load-bearing under mandatory windows (every envelope now has a pre-window phase; the pre-window admission stance rides on it, `evaluate.go:631-633`); `AutoRenew` is §5's bell. Three enforced features with no schema home are three unscheduled breaking changes under `AGENTS.md:178`. |

## 8. Where v2 is right — briefly, so the record is fair

Ruling 7's architecture is the correct resolution of both critics' central objection, and v2's
change table says so plainly. Per-subtree quarantine is the right *question* (my first-pass values
question 2), even though §3 shows the chosen answer has unpriced consequences. Cold-start
fund-nothing is still the correct corner and is now correctly grounded in Ruling 4
(`resolver.go:74-76`). "Not a series" is right *for funding* — I searched for a remaining scheduler
dependency on a past version and found none on the funding path; the only survivor is the meter
(§1c). `INV-PRINCIPAL-UNIQUE` promoted from comment to invariant fixes the defect both critics
flagged. And the design's closing attack list asks the right four questions — including, to its
credit, the two this critique answers against it.

## 9. Values questions for David — stated, not answered

1. **Spend ceilings.** Is "this scheduler will never enforce a total-consumption ceiling — ceilings
   are conversations between humans" a sentence you are prepared to say to whoever signs
   partner-compute contracts and compliance attestations? If yes, the meter needs a declared
   threshold and a wired alarm to have the conversation *with* — Ruling 11 deleted the only field
   that could hold the number. If no, the monotone-meter-with-threshold option in §1a.1 is the
   narrow way back that dodges every hazard in Ruling 10's table, at the price of replay memory and
   renewal-as-new-envelope discipline.
2. **Who is quarantine for?** Last-good protects the quarantined tenant's running work by freezing
   the director's cut and demoting the transfer recipient's new work — disabling the re-granting
   lever Ruling 3 named as *the* human mechanism — and under mandatory windows it eventually
   betrays its own beneficiary at the pinned window's end (§3). Unbound protects the tree's
   integrity at the cost of the quarantined innocent. Both allocate harm; Ruling 8 chose silently
   between them without the fuse or the recipient in view. Does last-good survive with a duration
   bound and a renewal/reduction pierce — or does the pin need rethinking?
3. **Whose 2am is the renewal treadmill?** Mandatory windows make expiry the failure mode of
   inattention, and the shipped notification default is off. Default-on renewal warnings and a
   window-end alarm versus today's opt-in is a one-line values choice about whether the system's
   default posture protects the inattentive — decide it explicitly, because §5 shows the failure
   cascade (demote → first-reclaim → blocked repair) is much bigger than "the grant ended."

**Bottom line for the coordinator.** Ratify nothing until: the producer's authorization invariant is
stated in the design with its signal named and Q1 re-classed as its mechanism (§2); the quarantine
schema carries `pinnedVersion`/`quarantineSince`, the fuse is defused by an explicit pierce-or-
escalate rule, and cross-pin conservation is defined (§3); §9 amends Decision 1 itself and the
deletion schedule absorbs §4's list; the window migration is sequenced webhook-first and the
renewal bell is default-on (§5); and Ruling 3's counter and odds come back from the dead (§6).
Ruling 10 itself deserves a corrected code inventory and an explicit owner sentence on spend
ceilings before it binds everything downstream of it — a ruling this load-bearing should not rest
on two misread lines and an unpriced third option.
