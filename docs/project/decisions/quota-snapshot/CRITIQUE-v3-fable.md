# CRITIQUE v3 — consequence seat (Fable, `claude-fable-5`) — targeted pass

**Of:** the three mechanisms new since my second pass: Ruling 16's Budget/Grant split
(`DESIGN-v3.md` §2, `OWNER-RULINGS.md` Ruling 16), the §4 quarantine window freeze, and §5's
append-only classed accrual. Nothing else is re-litigated. **Code cited against `origin/main` =
`ba5652e`** — verified `api/v1/budget_types.go`, `api/v1/run_types.go`, `pkg/funding/evaluate.go`,
and `deploy/helm/gpu-fleet/templates/rbac.yaml` are byte-identical between `origin/main` and the
branch head (`c24a7bc`; the branch diff is docs and specs only), so working-tree line numbers are
main's. Design documents are cited on branch `design/quota-snapshot`. Nothing executed; every claim
is from reading the tree.

**Verdict in three sentences.** Q1: the RBAC split prevents exactly one thing — forging the
grantor-side object in someone else's namespace — and the design's stronger sentence, "may not
enlarge their own allocation," fails on at least four enumerable channels, one of which (the Grant
schema carrying no window) hands the grantee half of the allocation product outright. Q2: the freeze
repairs the fuse I found by handing the fuse's trigger to its beneficiary — quarantine is inducible
by one RBAC-legal `kubectl apply` on your own object, and an about-to-expire principal can freeze
itself into operator-bounded immortality; the repair is real for innocents and a prize for
saboteurs, and it must be guilt-scoped to be shippable. Q3: append-only accrual genuinely makes
Rulings 6 and 9 structural for the ledger record, but it silently trades away recomputation's one
great virtue — convergence — so the design must add an amendment vocabulary (attributed reversing
entries), make the kubectl surfaces projections of the ledger, and state the crash-gap policy, or
it ships structural honesty about an increasingly wrong record.

---

## Q1 — Ruling 16's containment claim: every way a principal can still enlarge its own allocation

**What the API server actually enforces, stated exactly.** RBAC on a namespaced Grant checks the
*verb* and the *location* of the write — never the magnitude of the grant against what the writer
holds, and never the shape of what the grantee then claims to hold. The genuinely new property
Ruling 16 buys is: *a principal cannot author the grantor-side object for itself in someone else's
namespace*, because `metadata.namespace` is API-server-authenticated. That is real and load-bearing
(it is the pointer flip, `DESIGN-v3.md:94-96`). It is much narrower than "may not enlarge their own
allocation" (`DESIGN-v3.md:85-88`, `OWNER-RULINGS.md:569-571`). Everything below fits through the
gap between those two sentences. A structural note that governs the whole enumeration: the model's
atom is the namespace-principal — "its own allocation" is only defined per-principal, and the
system has no notion of the same human or team controlling two principals, so every human-level
self-dealing is invisible to authoring-time checks by construction.

### The enumeration

**(0) Self-grant in your own namespace** (David's finding, restated for the record). A Grant in the
lead's namespace naming the lead as grantee is a 1-cycle. A document invariant CAN catch it —
`INV-ACYCLIC` over the compiled edge graph catches self-loops trivially — but only if two
unresolved things are resolved: grant-derived edges must actually feed the acyclicity check (the
single-parent-`INV-ACYCLIC` versus multi-parent-`Spec.Parents` tension is flagged and unresolved,
`DESIGN-v3.md:261`), and the compiled document must record each edge's *authoring namespace*, which
today's schema does not — principals carry `children`, not edge provenance (`DESIGN-v3.md:103`).
Without provenance, "grantor == grantee" is not expressible in-document and the check is
producer-only. Invariant where it exists: **INV-NO-SELF-EDGE** — no compiled edge whose authoring
namespace UID equals the grantee's `boundNamespace.uid`.

**(1) Cycle through a cooperating peer.** A grants B, B grants A. `INV-ACYCLIC` catches it as a
document invariant — with the same two preconditions as (0). Two things must be said. First, the
benign near-miss: A granting B where both sit under the same parent M is *not* a cycle (it makes B
multi-parent: M and A) and is legitimate delegation, conserved at consumption at M — the check must
not be tightened into forbidding peer grants, or Ruling 1's horse-trading dies. Second, and this is
the finding: for a cycle, being *caught* is not a defense, because the consequence of catching is
quarantine, and under §4 quarantine is a freeze — for an about-to-expire pair, **the catch is the
prize** (see Q2). A document invariant exists; its failure consequence is currently desirable to
the party it catches.

**(2) A principal controlling two namespaces — or any grant from a principal that holds nothing.**
X controls ns-A and ns-B (multi-namespace teams are the norm). X writes a Grant in ns-B granting
512 to X's principal in ns-A. Is that rejected because ns-B holds nothing? **It cannot be** —
Ruling 2 makes over-granting legal, unalarmed, and explicitly not rejectable ("a director's
reduction makes `sum(children) > parent` instantly and legally... rejecting the director's write
would violate 'free to do this at any time'", `OWNER-RULINGS.md:40-46`; conservation moved "from
grant time to consumption time", `:44-46`). So no authoring-time check is *permitted* to catch
this. Containment is exclusively consumption-time `INV-SUBTREE-CONSERVE` — funded concurrency
whose payer lineage traverses ns-B's principal never exceeds ns-B's allocation (zero) — and that is
the design's own "largest piece," unbuilt: "Nothing aggregates descendants today
(`evaluate.go:339`)" (`DESIGN-v3.md:221-222`). **Until build item 5 ships, any Grant enlarges its
grantee with no check anywhere in the system**: the API server checks who may write, the producer
compiles what was written, and nothing compares the amount to anything. Verdict on the claim:
Ruling 16's "enforced by the API server rather than by the producer behaving well"
(`DESIGN-v3.md:86-88`) is an overclaim — magnitude containment was, is, and remains a
producer-plus-engine obligation, and the engine half does not exist. Only the producer/engine can
catch this; no document invariant can, because Ruling 2 forbids the document-level version.

**(3) A grant naming an ancestor.** C grants to its own parent or grandparent P: the edge closes a
cycle through the existing chain, so `INV-ACYCLIC` catches it — same preconditions and same
catch-is-the-prize caveat as (1). The non-cyclic variant — C grants to a principal in a *different
root's* subtree that happens to sit "above" C organisationally — is not a cycle, is legitimate
cross-subtree delegation, and is conserved at consumption on C's lineage. Document invariant:
`INV-ACYCLIC`, already listed; needs the multi-parent resolution at `DESIGN-v3.md:261` to be
meaningful.

**(4) A grant naming a principal that does not exist yet — and the recreated-namespace squat.** A
Grant naming a grantee that doesn't exist dangles, and the design handles the dangle correctly
(per-grant quarantine, `DESIGN-v3.md:90-91`). The hazard is *later binding*: `Spec.Owner` is
self-naming (`api/v1/budget_types.go:31`) and the engine's derivation simply indexes whatever
Budgets exist (`pkg/funding/evaluate.go:206-223`), so the grant is claimed by whoever first
creates a Budget matching the named owner in the named namespace. The grant addresses its grantee
by *name* — `(grantee owner, grantee namespace, per-flavour caps)`, `DESIGN-v3.md:70` — while the
snapshot binds namespaces by *UID* (`DESIGN-v3.md:103-104`). Namespace names are reusable: delete
ns-f, recreate it, and the producer's name→UID resolution binds the standing grant to the new UID
and its self-named claimant. **Inbound grants outlive the principal they were addressed to.** Who
is hurt: offboarding — a dissolved team's inbound grants linger in every grantor namespace until
each grantor remembers to delete them, and "expiry is the default" (`DESIGN-v3.md:40`) does not
help because windows do not live on grants (finding 6). No document invariant can see this — the
recreated world is internally consistent (rooted, injective, acyclic, windowed). **Producer-only**:
UID-pin the grantee binding at first compile — the same forward-only identity anchoring that
answered P6's squatter — and require the grantor to re-assert (touch the Grant) before it binds to
a new UID.

**(5) Deleting a Grant.** Directly, deletion can raise no one: edges only bound, and removing a
bound only lowers. Two indirect consequences matter. (a) Deletion is the *quarantine-release
lever*: deleting the object whose collision quarantined a subtree ends the freeze — a control
surface, not an enlargement, noted for Q2. (b) The dangerous one is a missing sentence, not a
mechanism: **the design never says what happens when a grantee's Budget exceeds its inbound
grants.** `Spec.Parents` is "derived from grants, or validated against them" (`DESIGN-v3.md:94-95`)
— that covers topology and says nothing about magnitude. If magnitude excess is a *validation
failure*, then every grantor reduction or revocation-by-deletion instantly invalidates the grantee
(their Budget now exceeds inbound), which quarantines them, which under §4 **freezes them at
last-good — the cut does not merely fail to bind, it entrenches the allocation it tried to
reduce.** Ruling 2 forces the answer: exceeding must compile as legal over-allocation — compiled
allocation clamps to `min(envelope, inbound grant)`, the excess surfaces as `overAllocatedBy`
(restored at `DESIGN-v3.md:184-187`), and consumption conservation demotes the excess — never
quarantine. This is the single most consequential unwritten sentence in §2. Catchable by: a
*clamp rule* in the producer plus the existing status surface; it must explicitly not be an
invariant whose failure quarantines.

**(6) The window axis — the grantee owns half the product.** The Grant schema is
`(grantee owner, grantee namespace, per-flavour caps)` (`DESIGN-v3.md:70`). No `start`, no `end`.
Windows live on envelopes in the grantee's own Budget (`api/v1/budget_types.go:66-67`), which the
grantee authors. Under Ruling 10 the allocation *is* concurrency × window, both mandatory
(`OWNER-RULINGS.md:344-349`) — so RBAC caps one factor of the product and hands the other to the
party being contained. `end: 2099` on your own envelope is self-enlargement that no RBAC and no
listed invariant sees. It also silently inverts the model's revocation story: "expiry is the
default — a grant nobody renews ends" (`DESIGN-v3.md:40`) assumed the *grantor's* passivity ends
the grant, but what expires is the window the *grantee* wrote and the grantee renews; grantor
passivity revokes nothing, and revocation is always an active deletion. Fix: Grants carry
`(cap, start, end)` per flavour, and a document invariant becomes expressible —
**INV-ENVELOPE-WITHIN-GRANT**: every envelope's `(flavor, concurrency, window)` fits inside an
inbound grant's `(flavor, cap, window)` — evaluated as the clamp of finding (5), never as
quarantine. Catchable in-document *after* the schema fix; inexpressible today.

**(7) Who writes the Budget at all — the sketch contradicts itself.** §2 says both objects are
"written by ordinary users" (`DESIGN-v3.md:65-66`); the RBAC sketch three lines later gives the
lead only `get/list` on budgets (`:82-84`). If leads cannot write their own Budget, holdings have
no author: the grantor has no RBAC in the grantee's namespace (that is the point of namespacing),
and root-admin authoring contradicts Ruling 1's self-service. If leads *can* write their own Budget
— the only workable completion — then a lead raising their own `concurrency` is an RBAC-legal
write, and the thing that stops it from being an enlargement is the producer clamping to inbound
grants, i.e. **the producer behaving well, which is exactly what `:86-88` claims to have escaped.**
The claim must be narrowed to what the API server actually delivers (the location-forgery property
stated at the top), and §2 must say who authors holdings. Consistent with this confusion, §9's
human test still describes granting as "`kubectl apply` on a Budget in the lead's own namespace"
(`DESIGN-v3.md:201-203`) — stale pre-Ruling-16 text that contradicts §2's two-object model in the
same document.

**(8) The chart RBAC is aspirational, and the design says otherwise.** The chart ships exactly one
ClusterRole, granting *cluster-wide* budgets CRUD to the controller ServiceAccount
(`deploy/helm/gpu-fleet/templates/rbac.yaml:19-24`), bound by one ClusterRoleBinding (`:43-54`).
The templates directory contains no Role or RoleBinding for users at all, `api/v1/` contains no
Grant type, and CRDs do not aggregate into the built-in `admin`/`edit` ClusterRoles without
explicit aggregation labels — so a tenant namespace-admin has *no* access to these resources by
default, and nobody else has any either. "Namespaced RBAC that already exists"
(`DESIGN-v3.md:66`; `OWNER-RULINGS.md:546-549`) exists as a Kubernetes *mechanism*, not as any
shipped policy — Ruling 1 already recorded this exact fact for Budgets ("the only RBAC the chart
ships grants `budgets` to the controller ServiceAccount", `OWNER-RULINGS.md:14-17`). The split is
aspirational until the chart ships per-namespace Role templates plus a stated binding story (who
gets the role, and whether via aggregation labels), and that work belongs on §10's build list. Note
also what the split does *not* change: the producer SA keeps cluster-wide write on budgets, so §12's
producer-as-total-trust-boundary is exactly as large after Ruling 16 as before it.

### Q1 summary

| # | Channel | Caught by document invariant? | Otherwise |
|---|---|---|---|
| 0 | Self-grant, own namespace | Yes — `INV-ACYCLIC` / `INV-NO-SELF-EDGE`, **only after** edge provenance is added to the schema and the multi-parent question (`DESIGN-v3.md:261`) is resolved | Producer-only today |
| 1 | Peer cycle | Yes — `INV-ACYCLIC`, same preconditions; but the catch = quarantine = §4 freeze = the prize (Q2) | — |
| 2 | Second namespace / grant from an unfunded principal | **No — forbidden to catch at authoring by Ruling 2** | Consumption-time `INV-SUBTREE-CONSERVE` only, which is unbuilt (`DESIGN-v3.md:221-222`); until then, uncontained |
| 3 | Grant to ancestor | Yes — `INV-ACYCLIC` (cycle through existing chain) | — |
| 4 | Not-yet-existing grantee; recreated namespace | **No — the document is internally consistent** | Producer-only: UID-pin at first bind, grantor re-assertion on UID change |
| 5 | Deletion / Budget-exceeds-grants | Must NOT be an invariant — Ruling 2 forces clamp-and-report (`overAllocatedBy`), never quarantine | Producer clamp rule; the missing sentence in §2 |
| 6 | Self-authored window (`end: 2099`) | Yes — `INV-ENVELOPE-WITHIN-GRANT`, **only after** Grants carry windows; inexpressible against today's Grant tuple (`DESIGN-v3.md:70`) | Schema fix required first |
| 7 | Self-authored Budget concurrency | No — it is an RBAC-legal write under the only workable reading of `:65-66` vs `:82-84` | Producer clamp to inbound grants — i.e. the producer behaving well |
| 8 | (Reality check) | — | Chart ships no user RBAC at all (`rbac.yaml:19-24` is the controller); the split is aspirational |

One constructive addition closes several rows at once: **per-edge provenance in the compiled
document** — authoring namespace UID, grant object UID, `firstBoundAt`. It makes (0) and (3)
document-checkable, gives Q2's adjudication its record, and finally makes my v2 §2
`INV-GRANT-AUTHORIZED` expressible rather than a specimen for an unstated rule.

---

## Q2 — The quarantine freeze: inducible, and as written the fuse's trigger now belongs to its beneficiary

### Can quarantine be induced? Yes — four channels, ranked by cost

**(a) Break your own object — the cheapest, and it needs no neighbour.** Quarantine is triggered by
validation failure; the subtree's own author can produce one at will, fully RBAC-authorized, inside
their own namespace: apply a malformed envelope (the half-windowed `Start`-set-`End`-nil gap the
design itself documents, `DESIGN-v3.md:57-62`, is one ready-made shape; any `INV-WINDOW-REQUIRED`
violation works). One `kubectl apply` on your *own* Budget. The subtree fails validation, holds
last-good, and under §4 its windows freeze (`DESIGN-v3.md:119-121`).

**(b) A second namespace you control claims your owner.** The two-namespaces-one-owner collision
the design explicitly accepts and answers with "the producer detects it and quarantines both"
(`DESIGN-v3.md:97-99`). Today's engine already detects exactly this shape —
`ConflictLeafOwnerSpansNamespaces` (`pkg/funding/evaluate.go:165-171`) — and its own comment states
the symmetry: a colliding owner "poisons every namespace it touches" (`:225-227`). Nothing in the
detection distinguishes incumbent from intruder; the sort at `:238-251` is for determinism of the
*report*, not for guilt.

**(c) A cooperating peer does (b) for you** — collusion; or does it *to* you — attack. Identical
mechanics, different namespace ownership.

**(d) A Grant asserting the victim's binding.** Under Ruling 16 the ownership assertion is
grantor-side: a Grant names `(grantee owner, grantee namespace)` (`DESIGN-v3.md:70`). Any principal
may therefore write, in their own namespace, an RBAC-legal object asserting `(victim-owner,
some-other-namespace)`. Whether that quarantines the *grant* individually (`DESIGN-v3.md:90-91`) or
contests the *victim's* binding and quarantines the victim is unspecified; if the latter, this is a
remote-control freeze of an arbitrary victim at the cost of one object in your own namespace.

### What it buys — the immortality question, answered: yes

A principal whose window is about to expire applies channel (a) the day before `end`. Quarantine
freezes the window (`DESIGN-v3.md:119-121`); the subtree runs funded past its intended expiry —
the design names this residual cost and bounds it by "how long an operator leaves a quarantine
unrepaired" (`:126-127`). Now walk the repair. The broken object is in the tenant's namespace;
under the design's own RBAC the only parties who can edit it are the *beneficiary* and
cluster-admin. The beneficiary will not. So every self-freeze is an operator page in which the
operator must reach into a tenant namespace and fix an object the tenant deliberately broke — and
after the repair, the tenant can re-break it with the next apply. **One YAML per cycle for the
attacker; one human per cycle for the defense.** The bound is real but its cost structure is
inverted, and "effectively immortal" is a fair description of the steady state on any cluster
where operator repair latency exceeds re-break latency — which is every cluster.

Two aggravations. First, the pin boundary is unspecified: v3 never says whether a quarantined
principal's *inbound* grants (authored in the grantor's namespace, outside the quarantined subtree)
are read live or held at last-good — §7 just incorporates v2 by reference (`DESIGN-v3.md:173`), and
my v2 §3 cross-pin question was not answered. If inbound edges are held, a director's cut cannot
bind on a frozen subtree either, so the freeze entrenches allocation on both axes at once —
combined with Q1 finding (5b), a *reduction itself* can trigger the quarantine that then blocks
the reduction. Second, sequencing: the freeze's entire safety case is loudness, and the design says
so ("quarantine must therefore be loud and escalating", `DESIGN-v3.md:127-128`; alarms are "a
PREREQUISITE", `:175-178`) — but `Conflicts()` has no production consumer today
(`pkg/funding/evaluate.go:176-183`), R26 wiring is build item 7 (`DESIGN-v3.md:224`) while
quarantine ships with the producer at item 1. If sequencing slips by one item, the shipped state
is *silent* immortality.

**Who is hurt.** Not the frozen subtree — the new victims are whoever queues behind its GPUs:
funded, named arriving claims (Ruling 4) that find capacity held by a subtree whose window should
have lapsed, and every other tenant in the lottery those claims then force. The freeze reallocates
harm from the quarantined innocent to the compliant majority, in proportion to how long the
quarantine stands — and the party who decides how long it stands is the one holding the GPUs.

### The fix — guilt-scope the freeze; and whether the fix reintroduces the fuse

The repair of my compound finding is real *for the case I named* — an innocent subtree quarantined
by a neighbour's error no longer expires on a clock it cannot stop. The defect is that §4 freezes
*unconditionally*, so the protection extends to the party that pulled the trigger. Four parts, all
producer-side because guilt is a property of the write event, which no document invariant can see
(my v2 §2, still true):

1. **Freeze only on external triggers.** The producer observes which object's write broke
   validation (it is the watch event it just processed). Rule: the window freeze applies only when
   the triggering write originated *outside* the frozen subtree. Principle, stated once: **freeze
   when repair requires someone else; tick when repair is in your own hands.** Self-authored
   breakage leaves your clock running — and at lapse the consequence is demote-and-coast
   (Ruling 2), not destruction, so this is not "a timeout that destroys" (`DESIGN-v3.md:127-128`)
   sneaking back: it is ordinary expiry, preventable at any moment by the author fixing their own
   object with their own RBAC.
2. **Incumbent-wins adjudication.** First-bind UID pinning — the established `(owner ↔
   namespace-UID)` binding survives a contest; the newcomer's object quarantines *individually*
   (the granularity the design already has for grants, `DESIGN-v3.md:90-91`) and the incumbent is
   not quarantined at all. This is the forward-only identity anchoring that answered P6, applied to
   the binding. It closes channels (b), (c), and (d) as freeze factories: colliding with a victim
   no longer touches the victim, and colliding via your own second namespace no longer freezes your
   first. It replaces "quarantines both" (`:97-99`), which should be amended — symmetry was the
   conservative choice only while nothing could break the tie; the producer can (first-seen state),
   and with Q1's per-edge provenance the tiebreak becomes auditable in the document after the fact.
3. **Object-granularity last-good.** Hold and freeze the *broken object* (a grant pinned at its
   last valid content does not expire while pinned), not the whole subtree, so the rest of the
   subtree keeps compiling. This matters more than it looks: the fuse required three conjuncts —
   pinned subtree, blocked renewal, ticking clock. Per-object pins unblock renewal (new versions of
   the subtree's other objects still compile), which dissolves most of the fuse *without any
   subtree freeze at all*.
4. **No credit at repair.** When the quarantine lifts, windows that lapsed during the freeze lapse
   immediately — unless a renewal was authored meanwhile, which lands at repair time. Ruling 12
   makes this principled: the renewal was not skipped, it was observed late
   (`OWNER-RULINGS.md:437-441`). The freeze buys the innocent *continuity*, never *extra duration*.

Plus schema and surface: a `quarantineTrigger` (object ref + write time) beside `quarantinedSince`
and `windowFrozenAt` (`DESIGN-v3.md:107-110`), and the escalating alarm keyed on `windowFrozenAt`
age — so the 3am operator sees not just "quarantined since Tuesday" but "frozen past its own end
since Thursday, broken by a write from ns-X," and repeated self-breakage becomes an attributable
pattern with an organisational answer (Ruling 9's spirit).

**Does the fix reintroduce the fuse?** For externally-broken innocents: no — they keep the freeze,
and part 3 removes most of their need for it. For self-broken subtrees: yes, deliberately, and it
is not the same fuse — the original burned a party who could not repair; this clock pressures the
only party who can, toward a terminal state that demotes rather than destroys. The residual exploit
is guilt-laundering: a colluding peer authors the collider so the trigger looks external. Part 2
closes the binding-collision variant (the incumbent is untouched, so there is nothing to launder).
What remains is an *ancestor* breaking their own objects to freeze the pinned inbound grants of
their descendants — organisationally self-harm, loud, and attributed, which is as far as mechanism
can go; beyond it lies a values choice, stated at the end.

**Verdict on the repair.** Real, not cosmetic — the fuse I found is genuinely gone. But as
specified it traded a defect that burned quarantined innocents for a prize that rewards saboteurs,
with the bill sent to the compliant majority. Guilt-scoped, incumbent-adjudicated, object-granular,
and credit-free, the freeze is the right mechanism. Unconditional, it is the second coming of the
squat: F2 as denial-of-expiry.

---

## Q3 — Append-only accrual: the claim tested, and the full price list

### The claim, tested

Does record-at-write make Rulings 6 and 9 structural? **For the ledger records themselves, yes** —
a fact written when it occurred cannot be rewritten by a later spec edit, which is precisely what
recomputation-from-current-spec structurally could not deliver (my v2 §1c named the fork; §5 chose
a third path, record-at-write, and it is the right family — better than both branches I offered).
But "structural" holds only under three conditions the design does not state:

**1. Late observation makes routine records permanently wrong — Rulings 12 and §5 compose badly.**
The writer classifies an interval from the snapshot version it has *observed*. Ruling 12 blesses
lateness because the consumer "converges to the correct one, and never acts wrongly relative to
what it knew" (`OWNER-RULINGS.md:437-441`). A write-once ledger does not converge: an interval
spanning an unobserved transition is classified under the old version *forever*. `effectiveFrom`
(`DESIGN-v3.md:103`) sharpens it — a version can be effective before it is observed, so a
correctly-functioning writer learns after the fact that a record it already wrote was wrong when
written. Recomputation's one great virtue was convergence — rerun and the answer heals. §5 trades
it away and never says so. Consequence: an amendment vocabulary is required even with zero bugs
(see the price list), and §5 should state that the ledger's accuracy at every class boundary is
bounded by watch latency, structurally.

**2. The kubectl surfaces must become projections of the ledger, or they keep lying.** The classes
operators actually see are status fields recomputed from the current graph on every pass:
`RunFundingStatus`'s per-class hours and lender shares (`api/v1/run_types.go:304-318`) and
`EnvelopeUsage.ConsumedGPUHours` (`api/v1/budget_types.go:145`), both fed by the replay's `accrue`
(`pkg/funding/evaluate.go:975-1034`). §5 deletes the clamp "with the recomputation"
(`DESIGN-v3.md:149`; the clamp is `evaluate.go:992-997`, and its aggregate twin at `:1024-1028`)
but never says the status fields are rewired to project from the ledger. If they stay
derived-from-current, Rulings 6 and 9 are structural in a warehouse nobody `kubectl get`s and
aspirational on every surface an operator actually reads — the frozen-gauge defect class, relocated
one more time. This is a missing clause in build item 4 (`DESIGN-v3.md:220`).

**3. "Append-only" must forbid rewrite-style compaction, explicitly.** The
`SettlementHorizon`/`PriorAccrual` machinery and its three TLA modules exist to summarize the
replayed integral (`pkg/funding/evaluate.go:35-50`); under §5 that seeding path is dead, and my v2
§4 retire-or-rescope item is still unaddressed in §10/§13. More importantly, the *new* ledger will
meet the same pressure that produced compaction, and if anyone ever "compacts" it by replacing
interval records with summaries, the structural guarantee dies silently. The rule must be written:
summaries may only be appended; originals are immutable; deletion under a stated retention policy
is permitted (deleting is not rewriting); **INV-LEDGER-APPEND-ONLY** — no record is ever modified,
and every adjustment names its target, author, and reason.

### The price list — what recomputation gave that this costs

**(a) Errors become permanent — and Ruling 9 as written forbids the repair.** David's item,
completed: it is not only *bugs* — condition 1 makes permanence routine, no bug required. The
sharper half is that the design currently has **no sanctioned fix at all**: Ruling 9 refuses
proposals that reach backwards — "recomputing an old interval, repairing a summary"
(`OWNER-RULINGS.md:300-302`) — so a wrong ledger stays wrong *by ruling*. The operator's actual
recourse today would be editing the warehouse by hand: off-audit, unattributed, the precise
dishonesty Rulings 6 and 9 exist to prevent. The repair is accounting's own: **attributed
reversing entries** — append a correction that references the original, names its author and
reason, and leaves the original visible. Nothing is taken back and nothing is concealed, so it
arguably passes Ruling 9's purpose while touching its letter — which is why it needs David's
explicit blessing rather than a designer's assumption (values question 2).

**(b) Storage, and the store it forces.** Growth is burst-shaped, not linear: one record per open
lease per class boundary — envelope window edges (`windowActive`,
`pkg/funding/evaluate.go:634-642`), lending changes (re-evaluated at every fill, `:797-798`), and
snapshot version bumps, where one publication can reclass every open lease in a subtree at once —
plus periodic checkpoint closes for crash-safety. The volume is Postgres-trivial and
etcd-impossible (Ruling 11 steers well clear of etcd's bounds), which promotes the "demoted"
external store (`REVISED.md:44-49`) into a correctness-of-record dependency — consistent with
Ruling 6's own finding that P3 stops being a feature deferral (`OWNER-RULINGS.md:262-266`), but §5
names no store, no availability class, and not the one rule that keeps it safe: **the ledger must
never gate funding** (it cannot — hours enforce nothing, `OWNER-RULINGS.md:350-353` — but nobody
has written the sentence). Store down ⇒ the writer buffers ⇒ buffer loss ⇒ permanent holes. A
durable local WAL for the writer is an operational component nobody has priced.

**(c) Writer crash mid-interval: the hours survive, the classes do not.** Raw hours are
recoverable — leases are durable and raw integration is spec-free. The *class* series is not:
classing the crashed span needs the version in effect during it, which is exactly what §5 declines
to retain ("No snapshot retention is required", `DESIGN-v3.md:150`; alternatives rejected at
`:154-157`). Honest options: append the gap as a fifth class, **Unknown** — chargeback disputes
will land there, visibly — or retain snapshots for a bounded *writer-lag* horizon. Either way the
design's sentence overclaims: the true statement is "retention bounded by writer lag, not by job
duration" — enormously better than v2's job-duration floor, but not zero, and §5 must pick an
option and say it.

**(d) Mid-lease class transitions are captured only if the writer never misses an edge.** Classes
change over a lease's life — window close demotes Owned to Unfunded, a lending revocation ends
Borrowed, a version bump re-tiers a family — and the design's own rejection of retention admits
this ("class changes over a lease's life... every version spanning a lease",
`DESIGN-v3.md:154-156`). Today the replay computes the class per segment at every boundary,
after the fact, healingly (`evaluate.go:675`, `:975`); the writer must now run that segment logic
once, at the time, unrepairably. And retroactively-effective transitions — `effectiveFrom` earlier
than observation — require splitting an interval that is already written, which append-only
forbids: the reversing-entry mechanism of (a) is therefore not an exotic error path but **how the
ledger absorbs routine lateness**. It must be in the design with its invariant, not improvised at
the first dispute.

**(e) A methodology cost specific to this repo.** Recomputation was oracle-able: any output could
be re-derived from inputs and checked — the golden-oracle guarantee the code advertises
(`evaluate.go:43-45`), and this project's per-PR gate *is* an invariant oracle. A write-once
ledger's past cannot be re-verified after the fact; only its writer can be certified, forward, by
conformance tests. The verification burden moves from checking outputs to trusting the writer —
which joins the producer (§12) on the short list of components whose good behaviour nothing
downstream can re-check. Two entries on that list is where this design now stands, and the specimen
plan (§10 item 9) covers the producer but not the accrual writer.

**Verdict on the claim.** True, and the right mechanism — record-at-write is the only family that
can make Rulings 6 and 9 structural, and it also deletes the clamp bug's entire class rather than
patching it. But as specced it is structural honesty about an increasingly wrong record. With
reversing entries plus INV-LEDGER-APPEND-ONLY, status-as-projection, a named store with a stated
never-gates-funding rule, a crash-gap policy (Unknown class or writer-lag retention), and a writer
conformance specimen, it becomes the strongest part of the design. Without them, R6/R9 end up
enforced against the one party — the operator correcting the record — who was never the threat.

---

## Values questions for David — stated, not answered

1. **The unattributable quarantine.** When guilt cannot be established — a colluding peer authored
   the collider, or an ancestor broke objects that pin a descendant's grants — the freeze must
   default one way. Freeze: continuity for a possibly-complicit subtree, subsidized by everyone
   queued behind its GPUs until an operator repairs. Tick: the clock runs on a possibly-innocent
   tenant whose attacker was clever. Both allocate harm; §4 currently chooses the first for
   everyone, silently. Which default do you want when the producer cannot tell?
2. **The reversing entry.** When the ledger itself is wrong — bug, crash gap, or routine late
   observation — is an attributed, append-only correction entry that leaves the original visible a
   violation of Ruling 9, or is it Ruling 9's bookkeeping done right? The current text refuses it;
   the only alternative anyone will actually use is editing the warehouse by hand, off the audit
   trail.
3. **What the Budget is.** Is the grantee's Budget a *claim* the producer clamps against inbound
   Grants (leads write their own holdings; containment is the producer's clamp), or a *record*
   compiled from Grants (leads write nothing; `:82-84`'s get/list is literal and holdings have no
   user author)? §2 currently asserts both (`DESIGN-v3.md:65-66` versus `:82-84`), and the
   containment story — what the API server enforces versus what the producer must — is different
   in each. One sentence decides it.

## Bottom line for the coordinator

Ratify none of the three mechanisms as written; all three are the right shape and each is one
missing clause from sound. (1) §2 must add the missing sentence: a Budget exceeding its inbound
grants compiles as clamped over-allocation — `min(envelope, grant)`, surfaced via
`overAllocatedBy` — never as a validation failure, or every reduction self-defeats through the
freeze (Q1-5b × Q2). (2) Grants must carry windows, with `INV-ENVELOPE-WITHIN-GRANT`, or the
containment claim is false on the time axis and grantor passivity revokes nothing (Q1-6); add
per-edge provenance to the compiled document; ship actual user RBAC in the chart or stop saying
"already exists" (Q1-8); and narrow Ruling 16's API-server sentence to the location-forgery
property it actually delivers. (3) The freeze ships guilt-scoped, incumbent-adjudicated,
object-granular, and credit-free, with `quarantineTrigger` in the schema — and R26 wiring moves
from build item 7 to a precondition of item 1, because a silent freeze is silent immortality (Q2).
(4) §5 gains reversing entries and INV-LEDGER-APPEND-ONLY, status fields become ledger
projections, the store is named with its never-gates-funding rule, the crash-gap policy is chosen,
and the accrual writer gets a conformance specimen alongside the producer's (Q3). None of this
reopens Rulings 10, 14, or 15, and none of it re-litigates the architecture — it is the last
clause on each of three good sentences.
