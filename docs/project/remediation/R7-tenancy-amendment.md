# R7 amendment — tenancy under David's rulings (2026-07-09)

**Priority:** P1 · **Design:** complete (Fable); revised after adversarial critique — every
objection and its disposition is in §14. All product forks in R7 are now closed; no owner
input is pending. · **Next:** Opus implements, Sonnet verifies.
**Amends:** [`R7-tenancy-envelope-namespace.md`](R7-tenancy-envelope-namespace.md). Its pt1
(namespace the `EnvelopeKey`) is **ratified** — §7 completes its map of lease-stamping sites and
adds a loud rail for missed ones. Its pt2 design-decision section and its invariant's
"cross-namespace funding only via an explicit sponsor ACL" framing are **superseded** by this
document — I am overruling my own earlier text, and §5 says why.

David asked three questions of the tenancy model he stated: *does it make sense, does it simplify
things, and what decisions remain?* Answers: **yes; substantially — R7 shrinks and needs no new
admission machinery at all; and none remain for David** (§12, with two vetoable defaults).

---

## 1. The rulings (verbatim, binding)

**The tenancy model (2026-07-09):**

> "Each researcher has a namespace and a service identity within the cluster. A researcher can
> grant namespace access to another researcher, full or limited, using K8s RBAC. All permissions
> for storage accounts, secrets, etc. hang off that service identity. Big projects can be handled
> by creating a pseudo user for the project and granting researchers access to it. This is
> different than a team with quota sharing. … Anyway, nothing ever runs without a namespace."

**One kind of funding principal (2026-07-09):**

> "A project could lend to or borrow from another project or user, just like a user. And a project
> lives in a team and gets family sharing just like a user."

**The wallet ruling — Alice runs in Bob's namespace (2026-07-09):**

> "If Bob gives Alice his wallet it's his money that gets spent. If he doesn't want this, he should
> not hand over his wallet, or at least he should trust Alice not to blow all his cash."

**The namespace→tier binding is admin-set (2026-07-09):**

> "Let's assume this is set by an admin today. We'll make a nice tool later with workstream owners
> and self service all nice. But worthless if there is nothing to run it, so not for a long time."

## 2. The accountability principle (named, first-class — David asked that this be written into the design)

> "If it's easier to make a team a principal I'm fine with that, but I'd tend to think it's a group.
> And I don't know what to do about giving a team a namespace. I guess that's okay, but I don't want
> members running jobs in their [team namespace] to escape their quota. I think users have more
> accountability than teams, and permissions flow with accountability. Write this down in the design
> somewhere."

Stated as a rule the rest of this document uses to decide:

**Quota that can be spent without an accountable individual is a hazard. A user is a strong
accountability anchor; a team is a weak one. Therefore every path by which team quota is spent must
remain attributable to a specific principal's Run, and no namespace in which work can be created may
be backed by group-only accountability.**

This principle and the wallet ruling are the same rule seen twice. "The namespace pays" means the
wallet is precisely the authorization to create Runs in a namespace. If a *team* namespace existed
and members held create-Run there, every member would hold the team's wallet — team spending with
no accountable individual, exactly the escape David named. So "the namespace pays" forces "teams
are groups, not runnable namespaces" (§5), and vice versa.

## 3. The model, restated precisely — ratified

**One kind of principal.** A researcher and a project are the same sort of thing: each has exactly
one namespace and one service identity; each may lend to or borrow from any other principal; each
sits under a team in the family hierarchy. A **team is not a principal**: it owns no namespace, has
no service identity, runs nothing. It is an interior node of the family DAG.

**Two axes, two namings.** This is the load-bearing simplification, evaluated (not assumed) in §7:

| Axis | Question it answers | Naming | Who asserts it | Authenticated by |
|---|---|---|---|---|
| **Isolation** | who pays / who may act | the **namespace** | the API server (a Run's `metadata.namespace` cannot be forged) | Kubernetes itself |
| **Family** | who shares excess with whom | **owner strings** (`org:ai:rai`) + `Budget.Spec.Parents` edges | the **admin**, on admin-owned Budget objects | RBAC ("no principal writes Budgets") |

The bridge between the axes is the admin-set binding: the Budget(s) an admin places in a
principal's namespace declare that namespace's tier (`Spec.Owner`) and its family edge
(`Spec.Parents`). Nothing a principal can write influences either axis.

**Permission and payment are separated, and that is the elegance.** RBAC and impersonation decide
*who may act* in a namespace; the namespace decides *who pays*. Corollaries, each verified:

- A limited RBAC grant (read pods, view logs) hands over no wallet; only create-Run does. **jobtree
  does not care about RBAC granularity** — which verbs Bob grants Alice is entirely the cluster
  admin's business. The only grants jobtree's correctness depends on are the negative ones in §6's
  precondition, and those go in R18's runbook.
- **Impersonating a project is not a funding mechanism.** Running in the project's namespace spends
  the project's budget *because that is the namespace* — verified: `request.userInfo` appears
  nowhere in the repo's Go code (grep: zero hits), and the mint constructs the Lease from
  `run.Namespace` and the resolved segment only (`pkg/admission/admission.go:205-235`).
  Impersonation buys permission to create the Run and the project's service identity for
  storage/secrets — nothing else. The audit trail: the K8s audit log records both the real user and
  the impersonated identity; jobtree's ledger records namespace + Run. Attribution to a human is
  the audit log's job, and that is acceptable (§8, residual 5).

**What the model fixes.** R7's two halves both dissolve rather than get guarded:
pt1's aliasing (`EnvelopeKey{Budget, Envelope}`, no namespace — `pkg/funding/funding.go:174-177`,
built from `b.Name` alone at `pkg/funding/evaluate.go:177`) is fixed by the mechanical keying
change, ratified as-is. pt2's unauthenticated owner (`run.Spec.Owner` checked only non-empty,
`api/v1/run_types.go:294-297`; family membership a unilateral `Spec.Parents` claim,
`pkg/funding/funding.go:60-73`; `LendingPolicy.To` matched against the spender's self-declared
string, `pkg/funding/evaluate.go:618,905-928`) is fixed by removing the spender-writable inputs
entirely: the owner comes from the namespace (§4), and the family/lending declarations sit on
admin-owned objects (§6). **No new VAP, no new webhook, no consent protocol** (§10).

## 4. `Run.Spec.Owner` is DELETED — the owner is derived from the namespace

The wallet ruling decides this: the namespace pays. A field that restates what the namespace
already determines is redundant, and this particular field is the forgery surface — the R5/R6 VAP
matches only `pods` (`deploy/helm/gpu-fleet/templates/validating-admission-policy.yaml:28-32`), so
today a researcher with ordinary create-Run can set `spec.owner: org:ai:victim` and class Owned
against the victim's envelopes. Deleting it removes the class of forgery instead of guarding it.

**Derivation.** `ownerOf(namespace) :=` the `Spec.Owner` shared by the Budgets in that namespace,
computed once per evaluation from the Budgets already in `funding.Input` (every consumer already
has them — `Evaluate` at `pkg/funding/evaluate.go:150-198`, the plugin's provenance check lists all
Budgets at `cmd/scheduler/plugin/gang.go:413`, the controller and admission helpers pass
`in.Budgets` through). Exposed as `Evaluation.OwnerOf(ns)`. Three cases:

- **Exactly one owner among the namespace's Budgets** — that tier string is the run's owner.
- **Zero Budgets** — the namespace is **unbound**. Precisely (an earlier draft said its runs "coast
  Unfunded", which conflated two paths — §14, C-2): a *fresh* Run there cannot admit at all — the
  derived owner is empty, `cover.Plan` refuses it (`FailureReasonInvalidRequest`,
  `pkg/cover/cover.go:134-136`), and the reservation path fails terminally
  (`opportunisticCoverPlan` finds no payer envelope → `failReservationNoEnvelope`,
  `controllers/run_controller.go:1110-1143`). Only *pre-existing* leases — a Budget deleted after
  the mint — reclassify **Unfunded** and coast: a legitimate state, not an error. That is the right
  fail-safe in both directions: nothing new runs on nobody's quota, and nothing already running is
  silently charged to anyone.
- **Two-plus distinct owners** — an admin error that would silently change who pays. **Fail safe:**
  the evaluation treats the namespace as unbound (same behavior as zero Budgets: fresh runs refused,
  existing leases Unfunded), surfaces the conflict on the `Evaluation`, and R26's auditor alarms.
  Chosen over a client-backed Budget webhook check (new machinery, and the webhook can't see
  cross-object state transactionally) and over a Namespace label as source of truth (a second
  admin-set surface to keep consistent with the first). This is the
  **one-principal-per-namespace invariant**: enforced by fail-safe + alarm, not by admission.
  Note the six golden fixtures never exercise this; `borrow-sponsor-runs` puts two different-owner
  Budgets in one (implicit) namespace and must be restructured (§9).

**An unbound namespace must also be unable to *borrow* — which today it is not.** `lendingAllows`
returns true for `To: []` and for the pattern `"*"` regardless of the borrower string, including the
empty one (`pkg/funding/evaluate.go:905-918`). So a pre-existing Borrowed lease from a wide-open
sponsor would *stay* Borrowed after its namespace became conflicted (`Tier` on the empty owner is
`tierNone` → the sponsor pass at `evaluate.go:611-618` re-admits it), quietly contradicting the
fail-safe just stated. **pt2 adds a one-line guard: an empty borrower is never lendable** (early
`return false` in `lendingAllows`). With the guard, an unbound or conflicted namespace evaluates
Unfunded across *all* claim kinds — family and sponsor alike — and "to participate in funding at
all, the admin must have bound your namespace" is true rather than merely asserted (§14, C-1).

**The converse invariant — one namespace per (leaf) owner.** `OwnerOf` makes namespace→tier a
function; the charge path needs tier→namespace to be one too, because class and cover resolution
are *owner-keyed, cluster-wide*: `cover.NewInventory` buckets envelopes by `acct.Owner`
(`pkg/cover/cover.go:84-89`) and the first phase of the walk is `{req.Owner}`
(`cover.go:102-109`), with no namespace term. If an admin binds the same leaf tier
(`Spec.Owner: org:ai:rai`) in two namespaces — a plausible copy-paste — a Run in `alice` derives
that owner, finds `bob`'s envelope at `TierOwner`, and mints an **Owned** charge across the
namespace boundary: senior, non-recallable, and invisible to the namespaced `EnvelopeKey`, which
only keeps *accounting* separate, not *resolution*. Under the model this is definitionally an admin
error — a principal has exactly one namespace (§3). Same treatment as its mirror image above: the
derivation detects any **leaf** owner (a tier appearing in no Budget's `Parents`) whose Budgets span
two-plus namespaces, treats the colliding namespaces as unbound (fresh runs refused; existing
leases Unfunded via the empty-owner path plus the borrower guard), surfaces it, and R26 alarms.
Interior tiers are exempt: nothing ever classes Owned against a pool (§5), pools are admin-written
at both ends, and R18 recommends one namespace per tier as posture. Considered and rejected:
scoping the `{req.Owner}` cover phase to same-namespace envelopes — it silently *reroutes* the
misconfiguration instead of making it loud, and leaves the equally string-keyed sibling/cousin
edges unguarded. Together the two invariants make namespace↔principal a bijection on bound
namespaces, which is exactly the model's claim in §3 (§14, S-1).

**Every reader, rewired** (checked before recommending deletion):

| Reader | Today | Becomes |
|---|---|---|
| `api/v1/run_types.go:294-297` | `Owner == ""` rejected | check deleted with the field |
| `pkg/funding/evaluate.go:527` (tier), `:618` (lending borrower); same pair in `AvailableWidth` | `run.Spec.Owner` | `ev.OwnerOf(run.Namespace)` |
| `pkg/cover` `Request.Owner` — filled at `controllers/run_controller.go:289,304-305,376,874`, `pkg/admission/admission.go:114` | `run.Spec.Owner` | `ev.OwnerOf(run.Namespace)` (callers all hold the evaluation) |
| `controllers/run_controller.go:1111,1114` (promise cover) | owner equality | budgets in `run.Namespace` |
| `cmd/scheduler/plugin/gang.go:408,419-431` (`promiseProvenanceValid`) | `seg.Owner == run.Spec.Owner` and `b.Spec.Owner == run.Spec.Owner` | `b.Namespace == run.Namespace` — *stronger*: the promise path only ever charges the run's own envelopes, and namespace equality is forge-proof where owner-string equality was two forgeable fields agreeing with each other |
| `cmd/scheduler/plugin/gang.go:363` (`spareLeaseProvenanceValid`) | owner+budget+envelope strings | adds the `PaidByNamespace` term (§7) |
| `pkg/forecast/forecast.go:236,239,272`; `pkg/resolver/resolver.go:298,497` | `Run.Spec.Owner` | `ev.OwnerOf(run.Namespace)` |

`Lease.Spec.Owner` (`api/v1/lease_types.go:26`) **survives** as a stamped fact: it is written only
by the plugin's mint from the resolved segment (`pkg/admission/admission.go:226`), `Evaluate`
ignores it for charging (charges resolve by `PaidBy*` — `evaluate.go:512`), and it is what makes a
lease's payer legible in audit output. `Budget.Spec.Owner` and `Spec.Parents` survive — they *are*
the admin-set binding. `Run.Spec.Funding.Sponsors` survives: naming a sponsor is a request, worth
nothing without the lender's ACL. `kubectl-runs submit` needs only its manifest examples updated —
it never derived owner anyway (`cmd/kubectl-runs/cmd/submit.go:31-42`).

## 5. The team pool — where a shared team budget lives

David separated the project pseudo-user from "a team with quota sharing", and the clarification
makes a team a family node, not a principal. Answering the sharpened questions directly:

**(i) A team CAN hold a Budget, and the existing four-class model already makes it safe.** The
team-tier Budget (`Spec.Owner: org:ai`, i.e. an interior node) lives in an **admin-managed,
non-runnable namespace** — created and edited by admins only, no service identity, no create-Run
grants, no principal bound to it. Members never charge it directly and never class Owned against
it. They reach it exclusively through **family sharing**: a member's Run in namespace `alice`
(child tier of `org:ai`) draws the team envelope's excess and classes **Shared** — attributed to
Alice's Run, junior to Owned, recallable. Since the team node runs nothing, *its entire capacity is
excess by construction*, so "drawing on a parent's pool" degenerates into exactly the mechanism
quota-semantics.md Decision 2 already defines for sharing excess — the cover planner already walks
parents first among family (`pkg/cover/cover.go:104-109`), family excess needs no lending policy,
and `classForTier` gives every non-owner tier Shared (`pkg/funding/funding.go:164-170`). **No new
mechanism.** The distinction Decision 2 draws between a parent's pool and a sibling's idle excess
turns out to be no distinction at all when the parent never runs: it is all excess, shared in
proximity order, recallable in principle — and since the owner never files a claim, recall never
fires, so in practice the pool fills by tier then admission time. This satisfies the accountability
principle by construction: every GPU-hour of team quota is spent by a named principal's Run in that
principal's namespace, visible in the lender's usage (`ClassShared` in `EnvelopeUsage`,
`api/v1/budget_types.go:115-124`).

**Binding a member into the family requires a real envelope — there is no parents-only "membership"
Budget.** `Budget.validate()` rejects empty `Spec.Envelopes` (`api/v1/budget_types.go:169-171`) and
every envelope requires positive concurrency (`:238`). Members with their own baseline allocation
are unaffected — their Budget already carries envelopes. A **pure pool-consumer** (no individual
allocation, team pool only) is bound with a **nominal owned envelope**: matching flavor/selector,
`concurrency: 1`, `maxGPUHours: 0` — the explicit zero caps hours (`MaxGPUHours` is `*int64`; nil
means uncapped, zero means none — `evaluate.go:111-116`), so the member's own tier yields no width
and admission falls straight through to the pool as Shared. Relaxing validation to permit
envelope-less binding Budgets was considered and rejected: the opportunistic reservation path needs
a real payer envelope of the run's flavor to attribute unfunded hours to (`opportunisticCoverPlan` →
`failReservationNoEnvelope`, `run_controller.go:1110-1143`), so an envelope-less binding would
create namespaces that can admit but never reserve. One nominal manifest stanza keeps every path
total; §11.3 verifies it (§14, C-3).

**A pool's reach is exactly its direct children.** The tier walk is owner/child/sibling/cousin
(`funding.go:128-150`; phases at `cover.go:102-109`) — there is no grandparent, uncle, or
sibling-*team* reach. Consequences, stated so they are chosen rather than discovered: a member of
`org:ai:west` can never draw `org:ai:east`'s **pool** for free — `Tier(east-pool, west-member)` is
`tierNone` (the pool is neither the member's parent, sibling, nor cousin) — while west and east
*members* are cousins and do share each other's owned excess. Crossing a team boundary into another
team's pool is **sponsor-lending territory** (a `LendingPolicy` on the pool's admin Budget), not
free family excess. R7 changes nothing about reachability; if David expects cross-team pool
consumption to be free, that is a sponsor edge for the admin to declare, and it is flagged as
vetoable default 3 in §13 (§14, C-5). Corollary for deep hierarchies: a pool must sit at the tier
members' `Parents` actually name — or `Parents`, being a DAG edge list, can name a higher tier
directly.

**What prevents Run creation in the team namespace** — the blocking-defect check: a Run there
*would* derive the team's tier and class Owned against the pool, the exact escape David named. The
enforcement is the same RBAC precondition that already carries the whole design (§6): admins grant
create-Run only in principal namespaces; the in-repo chart grants nothing to anyone but the
controller (`deploy/helm/gpu-fleet/templates/rbac.yaml:12-34` — sole ClusterRole, controller SA
only). Because that is posture rather than code, add the ~free rail: **R26 alarms on any Run whose
derived owner is an interior family node** (a tier that appears as some Budget's `Parents` entry).
A team tier with a Run is definitionally a violated precondition. I considered a VAP on `runs`
gating creation by namespace and rejected it: it would need its own namespace registry, duplicating
RBAC's job — machinery bought against a threat RBAC already blocks.

**(ii) Is the model complete?** Yes, with one thing deliberately inexpressible: a team pool whose
members get **Owned-class** (senior, non-recallable) capacity without an individual allocation.
That is not a gap; it is the accountability principle enforced — Owned-class group spending with no
accountable individual is precisely the hazard. If a member needs guaranteed capacity, the admin
allocates it to *that member's* Budget. Said plainly so David can object: **the model cannot
express "team quota spendable as one's own", and this design treats that as its central feature.**

**(iii) Cross-namespace funding is the COMMON case, and R7's original invariant framing is
OVERRULED.** Under this model every funding edge except Owned crosses namespaces: distinct
principals have distinct namespaces, so all sponsor lending is cross-namespace; team pools live in
admin namespaces, so all family sharing of team capacity is cross-namespace. My original
recommendation ("family/sharing is within a namespace; cross-namespace funding only via an explicit
sponsor ACL", R7-tenancy-envelope-namespace.md:39-44) assumed family co-residence in one namespace;
David's one-namespace-per-principal model makes that impossible, so the recommendation was wrong in
its framing. What survives of it is the actual security content, restated:

**Invariant (supersedes R7's).** Funding crosses a namespace boundary only along edges declared by
someone other than the spender: family edges (`Budget.Spec.Parents`, admin-declared on admin-owned
objects) and sponsor edges (`LendingPolicy.To`, lender-declared on the lender's admin-owned
Budget). The spender's position in the graph is derived from its namespace, which the API server
authenticates. **No spender-asserted edge exists.** Two axes, precisely (§14, C-4): envelope *accounting* is keyed
by `{Namespace, Budget, Envelope}`, so same-named **objects** in different namespaces never
co-mingle; the *family/cover/lending* axis is deliberately keyed on admin-set **owner strings**,
cluster-wide — that namespace-independence is exactly what lets a member's Run in `alice` reach a
pool living in an admin namespace. What keeps string-keying from ever crossing an isolation
boundary as Owned is not the key but §4's owner-injectivity invariant: a leaf owner bound in two
namespaces is a detected, alarmed admin error, not a supported "one principal, two homes"
configuration. Both halves belong in R18's runbook next to the one-principal-per-namespace rule.

## 6. The unilateral family graph — contained by RBAC, not fixed by protocol

`NewFamilyGraph` adds an edge for every `Spec.Parents` entry with no existence or consent check
(`pkg/funding/funding.go:60-73`). Per David's ruling: **no bidirectional consent protocol.**
Budgets are admin-owned; both ends of every family edge are written by the same accountable party,
so consent is meaningless — the admin would be consenting to themselves. Designing a
parent-lists-children handshake would be complexity bought against a threat RBAC already blocks.

- **(a) The precondition, named:** **no principal may write Budgets (or Leases).** The chart
  already conforms — the only RBAC objects shipped grant the controller SA
  (`deploy/helm/gpu-fleet/templates/rbac.yaml:12-34`); researcher-facing RBAC is entirely the
  cluster admin's, and the **required posture** is: principals get create/get on `runs` (and reads
  as desired) in their own namespace, and *never* any write verb on `budgets` or `leases`, anywhere.
  This sentence belongs verbatim in R18's operator runbook.
- **(b) Defense-in-depth at ~zero cost:** R26's ledger auditor gains five alarms —
  (1) a family edge whose named parent tier has no Budget anywhere (an admin typo silently grafting
  a family); (2) a namespace whose Budgets carry two distinct owners (§4's fail-safe fires);
  (3) a Run whose derived owner is an interior family node (§5); (4) a **leaf** owner whose Budgets
  span two-plus namespaces (§4's converse invariant — the cross-namespace-Owned hazard);
  (5) a live Lease with an empty `PaidByNamespace` (a missed mint site — §7's loud rail).
  No admission-path check: the
  Budget webhook is client-less (`controllers/kube/webhooks.go` wires only the self-contained
  `validate()` methods) and giving it a client to do cross-object reads is exactly the gratuitous
  machinery the ruling forbids.
- **(c) Escalation if the precondition is violated:** a principal with write on any Budget can set
  any `Owner`/`Parents`/`Lending` — i.e. mint themselves into any family at any tier and grant
  themselves any lending contract. That is **total quota compromise**, not an incremental leak.
  A Budget- or Lease-write grant to a non-admin must be treated as a security incident; the R26
  alarms are the detection, the K8s audit log is the forensics. Recorded here so the risk is
  remembered, not forgotten.

**`LendingPolicy.To` keeps naming owner strings — but the meaning changes.** I considered switching
`To` to borrower namespaces (forge-proof by construction) and rejected it: the hole was never the
vocabulary, it was that the borrower's owner string was self-declared. With the owner derived from
the namespace and the namespace's tier admin-bound, matching `To` patterns against the *derived*
owner is exactly as sound as matching namespaces — and strictly more expressive: subtree lending via
prefix glob (`To: ["org:ai:rai:*"]`) is already in real use (`controllers/golden_test.go:92`) and
has no namespace-vocabulary equivalent, since namespaces are flat. A principal is therefore
identified, uniformly and everywhere (family tiers, lending, forecasting), by **the admin-bound
owner string of its namespace** — user and project alike, no special-casing. `Parents` names a
**team** (a family node) — a different kind of reference: `To` points at spending principals and is
matched at evaluation time; `Parents` points at an interior node and shapes the DAG. Neither needs
consent, for the same reason: both live on admin-owned objects. `lendingAllows` itself
(`pkg/funding/evaluate.go:905-928`) does not change by a single line.

## 7. Tier strings and what `EnvelopeKey` becomes

**Tier strings remain as-is, and that is the right answer, not a leftover.** Verified: owner
strings are opaque everywhere — no dotted/colon parser exists; the hierarchy lives solely in the
declared `Parents` edge list (`pkg/funding/funding.go:66-69`), and the graph walks
(`funding.go:92-149`) are pure edge traversals. The hypothesis from David's clarification holds:
the tier string is the naming of the *family* axis, namespaces are the naming of the *isolation*
axis, and they must not be merged — namespaces are flat and cannot encode a DAG; owner strings are
unauthenticated and cannot provide isolation. Each axis keeps the representation that fits it, and
the admin-set Budget in each namespace is the sole join point.

**`EnvelopeKey` becomes `{Namespace, Budget, Envelope}`** — R7 pt1 as specified, ratified. Even
with admin-only Budgets this is required: keys must match object identity, and admin name
collisions across namespaces must not co-mingle accounting (today `evaluate.go:177` keys on bare
`b.Name`, and every lease-replay site re-derives the same bare key — `evaluate.go:512,804,861`,
`controllers/budget_controller.go:95`). Consequences threaded end-to-end:

- **`LeaseSpec` gains `PaidByNamespace` (required).** The lease must name the charged Budget's
  namespace because family/sponsor charges cross namespaces (§5): a lease in `alice` charging the
  team pool pays into the admin namespace. Clean break: required field, no fallback for old leases
  (the `PaidByBudget` back-compat comment at `api/v1/lease_types.go:31-34` is deleted rather than
  extended).
- **Every `LeaseSpec` construction that stamps `PaidBy*` sets it — there are THREE, not one.** An
  earlier draft named only the plugin mint; that was wrong (§14, B-1), and the miss matters because
  the field cannot be enforced at compile time and an in-process constructor never meets the CRD's
  `required`: (1) `admission.PodLeaseWithRole` (`pkg/admission/admission.go:229-231`) — the plugin's
  PreBind mint; (2) `binder.materializer.buildLease` (`pkg/binder/binder.go:313-349`) — live via
  `admission.Plan` → `binder.Materialize`, exercised today by the offline simulator
  (`cmd/kubectl-runs/cmd/helpers.go:62-84`) and by any future `Plan` caller; (3) the controller's
  **synthetic hypothetical leases** (`controllers/run_controller.go:423-458`) — never persisted, but
  replayed through the same `Evaluate`, so an empty namespace there would misclass the very
  evaluation that decides reclaim. A missed site fails *silently*: an `EnvelopeKey` with an empty
  namespace matches no account, and the `acct == nil` branch (`evaluate.go:512-518`) quietly
  classes the lease Unfunded. So `Evaluate` gains a **loud rail**: a live lease with empty
  `PaidByNamespace` is surfaced as a defect on the `Evaluation` and alarmed by R26 (§6b, alarm 5) —
  the class stays Unfunded (fail-safe), but never silently.
- **`cover.Segment` gains `BudgetNamespace`** (`pkg/cover/cover.go:41-47`), sourced from the real
  Budget's `ObjectMeta.Namespace` when accounts are built — this feeds all three constructors
  above. The fourth place a Segment's payer fields are written by hand is
  `opportunisticCoverPlan` (`controllers/run_controller.go:1110-1140`), which copies them from the
  resolved `acct.Key` and must copy the namespace too. Carried through the controller's payer
  annotations (`controllers/run_controller.go:1861,1925`).
- **`cmd/scheduler/plugin/gang.go:363`** adds the namespace term to the idempotent-reuse match, or
  it can false-positive on a same-named budget in the wrong namespace.
- **`Reservation.Spec.PayingEnvelope`** is a bare envelope name with no budget *or* namespace
  (`api/v1/reservation_types.go:27`) — the same aliasing class one door over. Scope it
  (`PayingNamespace`/`PayingBudget`) in the same pt1 sweep; verify its readers when touched.
- **CLI:** `kubectl-runs leases` payer column disambiguates with the namespace
  (`cmd/kubectl-runs/cmd/leases.go:62,70`).

## 8. Escalation paths: closed and open

**Closed** (each was concretely exploitable by a tenant with only namespaced create-Run/Budget):

1. **Owner spoof** — Run in own namespace, `spec.owner: <victim tier>`, classes Owned/Shared
   against the victim (`run_types.go:294-297` checked non-empty only; VAP never matches runs).
   Closed by deleting the field: the owner is derived from a value the API server authenticates.
2. **Family grafting** — own Budget with `Parents: [victim]` joins the victim's family and drinks
   `SharingFamily` excess with no consent (`funding.go:60-73`). Closed by the admin-only-Budgets
   precondition; detected by R26 alarms (§6b).
3. **Envelope aliasing** — same-named Budget in another namespace co-mingles into one
   `EnvelopeKey` (`evaluate.go:177`). Closed by the namespaced key (§7).
4. **Lending-glob theft** — `To: ["org:ai:*"]` matched any spender who typed a matching
   `spec.owner` in any namespace (`evaluate.go:905-928`). Closed because the borrower string is now
   admin-derived, not spender-claimed.
5. **Pod-side forgery** — already closed by R5/R6's VAP (pods only, which is now *sufficient*: pods
   are the only funding-relevant object a principal can shape, since Runs lose their funding field
   and Budgets/Leases are admin/controller-only).

**Open, stated honestly:**

1. **Everything rests on the RBAC precondition** (§6a). It is posture, not code. Detection is R26;
   consequence of violation is total (§6c). This is the designed trade, per David's ruling.
2. **Binding conflicts** — a multi-owner namespace, or a leaf owner spanning namespaces — are admin
   errors handled by fail-safe + alarm (§4), not prevented at admission. During the window before
   the admin fixes the Budgets: *pre-existing* leases in the affected namespaces reclassify Unfunded
   and coast; *fresh* runs there are refused admission outright (cover finds no payer; reservations
   fail terminally). Fail-safe direction: nobody is silently charged — including through wide-open
   `To: ["*"]` sponsors, which is only true because of the empty-borrower guard (§4); without it, a
   prior Borrowed lease would survive the conflict against a permissive lender.
3. **Intra-family fairness** — a member can drain the entire team pool (Shared fills by admission
   time within the tier; no per-member cap exists). Not an isolation hole: the admin chose the
   family, and the owner-recall/ranking semantics are working as designed. If per-member caps are
   ever wanted, that is a quota-semantics change, not a tenancy one. Accepted.
4. **Human attribution under impersonation** stops at the K8s audit log; jobtree's ledger records
   namespace + Run only (§3). Accepted — the audit log is the system of record for "who acted".
5. **Denial-of-funding inside one's own namespace** (a principal spamming Runs burns only their own
   quota and their own namespace's rank) — not a tenancy issue; unchanged.

## 9. Clean break — what breaks

No dual-read, no conversion, no migration; there is no production install (R15: `release.yaml`
builds no images). One scheduled outage covers:

- **Run CRD:** `spec.owner` removed. Every stored Run, doc example, and test manifest updates.
- **Lease CRD:** `spec.paidByNamespace` required (and Reservation's payer scoping, §7). Coordinate
  the schema break with R13's `Lease`→`GPULease` rename and R2 pt3's durable-identity fields so the
  lease schema breaks **once** (§10).
- **Budget CRD:** no schema change. Semantics change: `Owner`/`Parents`/`Lending` are admin
  declarations; documented in R18.
- **Golden fixtures are restructured, not just regenerated.** Today every scenario's Budgets carry
  no namespace at all (implicit `""`) while Runs live in `default`
  (`controllers/golden_test.go:63-72`) — under namespace-derived ownership those runs would find
  zero Budgets in `default` and evaluate Unfunded across the board. Fixtures become
  model-conformant: each principal's Budget in that principal's namespace, sponsor Budgets in their
  own namespaces (`borrow-sponsor-runs` currently puts two owners' Budgets in one implicit
  namespace — `golden_test.go:86-104`). **The parity rail is semantic, not byte-level — and it is
  class/width parity only.** Classes, widths, counts, and lenders must be identical before/after;
  the JSON diffs are the deliberate field changes (owner removed, `paidByNamespace` added). The
  golden **cannot witness hour drift**: `goldenFunding` deliberately captures counts and lenders,
  not the wall-clock GPU-hour floats (`golden_test.go:266-270`; established in R4 pt2's design,
  Finding 2), so an accrual shift introduced by re-topologizing `borrow-sponsor-runs` across
  namespaces would pass the golden unnoticed. Hour parity is therefore asserted by a separate
  accrual round-trip test (§11.7), R4-pt2-style, not claimed of the golden (§14, B-2). Note the
  existing suite provides zero coverage of the aliasing bug — every scenario is single-namespace by
  construction — so §11's new fixtures are the first real cross-namespace rail.

## 10. Implementation spec (Opus) — and the honest re-size

R7 was sized **L (pt1 M)** with pt2 blocked on David (SIZING.md:86-90). With the rulings in hand:
**no new admission policy, no webhook, no consent protocol, no CRD guard beyond deleting a
validation line.** R7 drops to **two M PRs (~1 focused day total)** — it shrank, as predicted, and
is not padded back up:

- **pt1 (M, unchanged):** `EnvelopeKey`/`claimKey` gain `Namespace`; five key-construction sites
  (`evaluate.go:177,512,804,861`; `budget_controller.go:95`); `Segment.BudgetNamespace` (including
  `opportunisticCoverPlan`'s hand-built Segment); `LeaseSpec.PaidByNamespace` at **all three**
  stamping sites — `PodLeaseWithRole`, `binder.buildLease`, the hypothetical-lease constructor
  (§7); the empty-`PaidByNamespace` loud rail in `Evaluate`; `gang.go:363`; Reservation payer
  scoping; CLI payer column; golden restructure per §9 plus the accrual round-trip (§11.7).
  Funding engine ⇒ adversarial review.
- **pt2 (M, was unsized/blocked):** delete `Run.Spec.Owner`; add `Evaluation.OwnerOf` with the
  three-case derivation, the conflict fail-safe, and the leaf-owner-injectivity detection (§4); the
  one-line empty-borrower guard in `lendingAllows` (§4); rewire the reader table (§4);
  `promiseProvenanceValid` → namespace equality; fixtures. Funding engine ⇒ adversarial review.

  The critique pass added small guards (borrower guard, injectivity detection, loud rail, nominal
  envelope documentation) but no new machinery — both PRs remain M; the verdict "no new admission
  policy or webhook" stands.

**Interactions** (all touch the same key/reference surface; SIZING already sequences R7's keying
first, which stands):

- **R4 pt2b** (settlement in Budget `status`): settled summaries key by envelope — they inherit the
  namespaced key. Land R7 pt1 first, exactly as SIZING.md:88-90 already orders; no size change.
- **R13** (`Lease`→`GPULease`): carries `PaidByNamespace` through the rename mechanically. Schedule
  R7's lease-schema addition, R2 pt3's identity fields, and R13's rename as **one** CRD outage; no
  size change to R13.
- **R2 pt3** (restart reconstruction): its durable mint-time identity threads through the **same
  set of stamping sites** as `PaidByNamespace` — `PodLeaseWithRole` AND `binder.buildLease` (and
  the hypothetical constructor, if the identity participates in evaluation) — so coordinate the two
  changes to touch that lease-construction surface once. An earlier draft claimed a single shared
  mint site (`PodLeaseWithRole` alone); that undercounted (§14, B-1). No size change.
- **R26**: +3 cheap alarms (§6b). Small addition to its spec.
- **R18**: the RBAC posture section (§6a) and the team-namespace rule (§5).

## 11. Verification spec (Sonnet)

1. **Aliasing closed:** two Budgets named `team-west` in two namespaces (distinct owners); a Run in
   ns-A cannot consume ns-B's headroom (pre-change they alias; post-change they do not). New golden
   fixture.
2. **Owner spoof impossible by construction:** no field to forge — a Run manifest carrying
   `spec.owner` is **silently pruned** by the structural CRD schema: the create *succeeds* and the
   field is absent on read-back (pruning, not rejection — do not assert a rejected create, and do
   not "fix" the pruning by adding a rejecting webhook; none is wanted, §10). Then assert the run's
   effective owner equals `ev.OwnerOf(run.Namespace)` regardless of what the manifest carried.
3. **Team pool:** admin-namespace Budget (`Owner: org:ai`, `Sharing: family`), member namespaces
   with child Budgets; a member's Run classes **Shared** against the pool, attributed to the
   member's Run, visible in the pool's `EnvelopeUsage.SharedGPUs`; a **pure pool-consumer** bound
   with the nominal zero-hour envelope (§5) admits and classes Shared, and can still *reserve*; a
   Run *in* the team namespace (simulating a violated precondition) trips the R26 alarm.
4. **Wallet ruling:** Alice's identity creating a Run in Bob's namespace charges Bob's Budget
   (owner derivation ignores the creator); same for an impersonated project SA in the project
   namespace.
5. **Unbound and conflicted namespaces:** a *fresh* Run in a zero-Budget or multi-owner namespace
   is refused admission (cover rejects; the reservation fails terminally); *pre-existing* leases
   reclassify Unfunded; a prior Borrowed lease from a `To: ["*"]` sponsor demotes to Unfunded when
   its namespace becomes conflicted (the empty-borrower guard — this is the regression test for
   §14, C-1); the conflict surfaces on the evaluation and nothing is charged.
6. **Owner injectivity:** the same *leaf* `Spec.Owner` bound in two namespaces → both namespaces
   unbound, R26 alarm 4 fires, and no Owned charge crosses a namespace (pre-change this silently
   classes Owned against the other namespace's envelope — the regression test for §14, S-1).
7. **Parity rail, two halves:** (a) all restructured single-tenancy goldens produce identical
   classes, widths, counts, and lenders; (b) because the golden is hour-blind (§9), an accrual
   round-trip asserts `Evaluate` on the restructured cross-namespace fixtures equals the pre-change
   single-namespace evaluation on `ConsumedGPUHours`, `HoursByClass`, and lender hours.
8. **Missed-mint rail:** a live lease with empty `PaidByNamespace` surfaces a defect on the
   evaluation (and R26 alarm 5), classing Unfunded rather than silently disappearing into it.
9. **Cross-namespace sponsor:** lending works only through the lender's `To` against the borrower's
   *derived* owner; a borrower namespace bound to a non-matching tier is refused
   (`FailureReasonACLRejected`).
10. **Pool reach:** a member of a sibling team cannot draw the pool (classes fall through to
   sponsor/Unfunded), while sibling-team *members* share owned excess as cousins (§5).

## 12. Not now, and why (recorded verbatim)

Self-service workstream-owner tooling — namespace/tier binding UIs, delegation flows, budget
request queues — is **deliberately deferred, with no hooks left for it**:

> "Let's assume this is set by an admin today. We'll make a nice tool later with workstream owners
> and self service all nice. But worthless if there is nothing to run it, so not for a long time."

## 13. Decisions remaining for David

**None are required.** The four rulings plus the accountability principle close every fork R7
flagged, including its "Decision for David" (family sharing and sponsor lending DO cross
namespaces, along declared edges only; the tenant IS the namespace). Two defaults chosen here are
his to veto, not to answer:

1. **`LendingPolicy.To` stays owner-string patterns** (subtree lending preserved) rather than
   becoming namespace names — §6. Safe either way; this is the more expressive and smaller change.
2. **Binding conflicts (multi-owner namespace, or a leaf owner spanning namespaces) fail safe to
   unbound + R26 alarm** rather than being rejected at admission — §4. The alternative buys a
   client-backed webhook for an admin-error class.
3. **A team's pool is reachable only by its direct children** — crossing into another team's pool
   requires a sponsor edge on the pool's Budget, not free family excess (§5). This is existing tier
   semantics, unchanged by R7 and stated here so it is chosen: if David wants sibling teams to
   consume each other's idle pools freely, the admin declares `LendingPolicy` on the pool Budgets
   (or a shared higher-tier pool); no code change either way.

And one consequence stated so it is chosen rather than discovered: **a team pool cannot confer
Owned-class capacity on a member** (§5.ii). That follows from his own principle; if a guaranteed
carve-out is ever needed, the admin allocates it to the member's Budget.

## 14. Critique and responses

Three adversarial reviewers read the first draft. Every objection, its disposition, and where the
fix landed. None was fatal to the model; three MAJORs forced real repairs. The verdict is
unchanged: the model is ratified, R7 needs no new admission machinery, and both PRs remain M —
the repairs are guards and corrected maps, not new mechanisms.

**S-1 (security, MAJOR): owner-string collision across namespaces mints cross-namespace Owned,
unguarded and unalarmed.** *Accepted — the draft's worst defect; fixed.* The draft asserted one
owner per namespace but never the converse, and the charge path needs the converse:
`cover.NewInventory` buckets by owner cluster-wide, so the same leaf `Spec.Owner` in two namespaces
let a Run in one charge the other's envelope as Owned — senior and non-recallable, exactly the
boundary R7 exists to draw, and the namespaced `EnvelopeKey` is powerless because *resolution* is
owner-keyed, not namespace-keyed. Fix: the **one-namespace-per-leaf-owner invariant** (§4), detected
in the derivation, fail-safed identically to the multi-owner case, R26 alarm 4 (§6b), regression
fixture §11.6. The reviewer's alternative — scoping the owner phase in cover to same-namespace
envelopes — was considered and rejected in §4: it reroutes the misconfiguration silently and leaves
the sibling/cousin string edges unguarded.

**S-2 (security, MINOR): §11 asserted the CRD "rejects" a stray `spec.owner`; structural pruning
accepts and strips it.** *Accepted — reworded.* The reviewer's sharpest point was the second-order
risk: a test asserting rejection fails against correct behavior, and the tempting "fix" is a
rejecting webhook — precisely the machinery §10 forbids. §11.2 now asserts pruning semantics
(create succeeds, field absent on read-back, effective owner is `OwnerOf(namespace)`) and says in
so many words that no rejecting webhook is to be added. Pruned == removed == unforgeable; the
posture was right, the test expectation was wrong.

**C-1 (security, MINOR): the stated fail-safe was false — a conflicted or unbound namespace still
borrowed from a `To: ["*"]`/empty-`To` sponsor.** *Accepted — fixed in code, not just wording.*
`lendingAllows` returns true for open policies regardless of the borrower string, including the
empty one, so a prior Borrowed lease survived its namespace becoming conflicted. Rather than weaken
the documented invariant to match the leak (the reviewer's fallback), pt2 adds the one-line
empty-borrower guard (§4), restoring the invariant as stated: an unbound namespace participates in
*nothing*. Regression test §11.5. The guard also makes S-1's fail-safe coherent — a collided
namespace's live leases demote fully to Unfunded instead of lingering as Borrowed.

**C-2 (coherence, MINOR): "coasts Unfunded" conflated admission with reclassification.** *Accepted
— reworded in §4 and §8.* Fresh runs in unbound/conflicted namespaces are refused (cover rejects
the empty owner; the reservation path fails terminally at `failReservationNoEnvelope`); only
pre-existing leases reclassify Unfunded and coast. The fail-safe direction claim (nobody silently
charged) was correct and stands — now with the C-1 guard actually backing it.

**C-3 (coherence, MAJOR): no parents-only "membership" Budget exists — a pure pool-consumer's
binding fails Budget validation.** *Accepted — fixed with the nominal-envelope pattern, §5.*
`Budget.validate()` requires ≥1 envelope with positive concurrency, so a pure consumer is bound
with a nominal `concurrency: 1, maxGPUHours: 0` envelope whose accrual gate yields no width,
falling through to the pool as Shared. The alternative — relaxing validation to allow envelope-less
binding Budgets — is *rejected*, not deferred: the opportunistic reservation path needs a real payer
envelope of the run's flavor, so envelope-less bindings would create namespaces that admit but can
never reserve. Verified in §11.3.

**C-4 (coherence, MINOR): "never alias" overclaimed — owner-string identity is deliberately
namespace-independent.** *Accepted in part; partially superseded by the S-1 fix.* The two axes are
now stated precisely in §5's invariant: the namespaced key isolates *accounting*; the owner string
deliberately spans namespaces so members reach admin-namespace pools. But the reviewer's reading
that two same-owner Budgets in different namespaces "ARE one principal by design" is now **ruled
out for leaf owners** — under §3's model a principal has exactly one namespace, so that
configuration is S-1's detected error, not a supported merge. Interior tiers may span admin
namespaces (nothing classes Owned against them); R18 guidance says don't anyway.

**C-5 (coherence, MINOR): sibling *teams* do not cross-share pools; only sibling/cousin
*principals* share owned excess.** *Confirmed as intended behavior — documented, not changed.* The
tier walk gives a pool exactly its direct children (§5); crossing into another team's pool is
sponsor-lending territory. R7 changes nothing about reachability. Because the expectation gap is
real, this is surfaced to David as vetoable default 3 (§13): if he wants free cross-team pool
consumption, the admin declares lending on the pool Budgets — no code change either way. Fixture
§11.10.

**B-1 (blast-radius, MAJOR): the mint-site enumeration was wrong — three `LeaseSpec` stamping
sites, not one, and the R2 pt3 "touch the mint once" claim rested on the miscount.** *Accepted —
fixed.* §7 now enumerates all three constructors (`PodLeaseWithRole`, `binder.buildLease`, the
hypothetical-lease constructor) plus `opportunisticCoverPlan`'s hand-built Segment, and §10's R2
pt3 note is corrected to coordinate across the full set. Because a missed site fails *silently*
(empty-namespace key → `acct == nil` → quiet Unfunded), §7 adds the loud rail the reviewer
suggested: `Evaluate` surfaces any live lease with empty `PaidByNamespace` as a defect and R26
alarm 5 fires. The draft's "every reader, rewired (checked)" table was about `Run.Spec.Owner`
readers and was accurate; the miss was on the lease-*writer* side, which had no such table. It
does now.

**B-2 (blast-radius, MINOR): the golden cannot witness the "hours must be identical" half of the
parity rail.** *Accepted — the rail was overclaimed.* `goldenFunding` captures counts and lenders,
never GPU-hour floats (a deliberate R4 pt2 decision). §9 now claims class/width/count/lender parity
of the golden only, and §11.7 adds the R4-pt2-style accrual round-trip (`ConsumedGPUHours`,
`HoursByClass`, lender hours equal before/after the fixture restructure) as the hour rail — needed
precisely because `borrow-sponsor-runs` is being re-topologized across namespaces, the one place an
accrual interaction could shift unnoticed.
