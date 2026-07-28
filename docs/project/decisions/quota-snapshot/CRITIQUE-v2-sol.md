Verdict: do not ratify. Ruling 10’s exhaustiveness claim is false, Ruling 11’s claim that sharding loses only harmless simultaneity is false, and Ruling 12’s skipped-version closure is wrong under v2’s own version-pinned multi-consumer contract.

Code citations are against `origin/main@ba5652e`.

One record problem first: the supposedly binding file does not contain thirteen rulings. It jumps directly from [Ruling 6](/Users/david/mycode/jobtree/docs/project/decisions/p5-p8/OWNER-RULINGS.md:215) to [Ruling 9](/Users/david/mycode/jobtree/docs/project/decisions/p5-p8/OWNER-RULINGS.md:278). Rulings 7 and 8 exist only as the design’s summaries at [DESIGN-v2.md:15](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:15). Until their actual owner text is restored, “Ruling 8 binds quarantine” is not supported by the binding record.

## Claimed closures

- Two epochs: **correctly dissolved for funding, narrowly**. Once no integral is enforced, the scheduler has no depleting balance whose reset epoch must differ from the authority snapshot epoch. My original two-epoch funding finding no longer applies.

  It is not dissolved for metering honesty: Rulings 6/9 still require historical charges not to move, while the current evaluator derives one current graph before replay and uses it for every past interval ([evaluate.go:322](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:322), [evaluate.go:704](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:704)). That is now a reporting/history obligation, not a funding obligation. If Ruling 10 is reversed because of finding 1 below, the two-epoch funding problem immediately returns.

- Skipped versions: **closure wrong as v2 is written**. Current-state convergence would make intermediate versions dispensable only if nothing durable ever referenced them. V2 requires every lease to record its authorizing version and says that fixes disagreement between independently polling consumers ([DESIGN-v2.md:129](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:129), [DESIGN-v2.md:151](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:151)). A controller can prepare an artifact under N+1 while the plugin observes N and then N+2. Without N+1, the plugin cannot validate the pinned authorization. Recording a version the consumer cannot retrieve is an audit label, not coherence.

  Current code demonstrates the lifecycle: payer commitments survive between decision and mint in `gangCommit` ([gang.go:67](/Users/david/mycode/jobtree/cmd/scheduler/plugin/gang.go:67)); restart reconstructs them from lease provenance while consulting current Budgets ([gang.go:397](/Users/david/mycode/jobtree/cmd/scheduler/plugin/gang.go:397), [gang.go:480](/Users/david/mycode/jobtree/cmd/scheduler/plugin/gang.go:480)); Promise minting independently revalidates current Budgets ([gang.go:778](/Users/david/mycode/jobtree/cmd/scheduler/plugin/gang.go:778)). V2 therefore needs bounded past-version addressability until no in-flight commitment references a version, or a self-contained signed authorization. It does not need settlement-horizon retention, but “current version only” is false.

## 1. A real allocation pattern Ruling 10 cannot express

A project receives:

> 10,000 GPU-hours during Q3, may burst up to 128 GPUs, and chooses when to spend them.

Its entitlement is:

\[
0 \le u(t) \le 128,\qquad \int_{Q3} u(t)\,dt \le 10{,}000
\]

No single concurrency×fixed-window grant represents that feasible set:

- `concurrency=128`, window=all Q3 preserves burst width but permits vastly more than 10,000 hours.
- Lower concurrency to `10,000 / quarter-duration` enforces the total only by destroying the 128-GPU burst.
- Shorten the window preserves width and total only by choosing the tenant’s burst time in advance.

This is exactly why concurrency and `MaxGPUHours` are independent API axes ([budget_types.go:63](/Users/david/mycode/jobtree/api/v1/budget_types.go:63)). The validation that hours are no greater than concurrency×window ([budget_types.go:274](/Users/david/mycode/jobtree/api/v1/budget_types.go:274)) proves only that the integral entitlement is a subset; it does not prove the subset can be represented by another rectangle.

The three named alternatives do not cover it:

- Opportunistic work is not an entitlement.
- Lending requires a peer and transfers concurrency, not a fungible time budget.
- Repeated temporary grants require the manager to become an online allocator and choose timing for the tenant.

Therefore Ruling 10’s claim that lowering concurrency or shortening the window says the same thing is mathematically wrong. The owner can deliberately refuse fungible compute-credit allocations, but the design must state that product exclusion. It cannot claim to cover every real allocation pattern.

## 2. Last-good quarantine can preserve revoked authority

Yes, holding last-good can be materially worse than going unbound.

Scenario:

1. Root shard R contains manager M and hostile delegated child C.
2. M reduces or deletes C’s allocation.
3. C concurrently keeps its authored subtree invalid—duplicate binding, bad edge, or unresolved lending reference.
4. Because sharding and quarantine are declared the same root-subtree boundary, R holds last-good ([DESIGN-v2.md:171](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:171), [DESIGN-v2.md:177](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:177)).
5. C’s old allocation continues funding work.

Going unbound would make those leases Unfunded and first-reclaim candidates; that is the current explicit behavior ([evaluate.go:274](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:274), [evaluate.go:280](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:280)). Last-good instead leaves them funded, billed, and senior.

Duration: until the subtree produces an accepted state or each last-good envelope reaches its old `end`. Since v2 imposes no maximum window length, this is arbitrarily long. The producer may be healthy the entire time, so “fate-shared with etcd” supplies no bound.

This also lets a hostile descendant veto an ancestor’s revocation by remaining invalid. That violates Ruling 1’s containment requirement. If quarantine is meant to occur below the root instead, then sharding and quarantine are not the same boundary and the design must specify how a new ancestor cap is combined with a child’s last-good state.

## 3. Remaining dependency on a past snapshot

There are two:

1. The in-flight mint dependency described under the skipped-version closure. A version-pinned Promise, gang commitment, or lease cannot be verified from an unrelated current version.

2. Classed chargeback, unless it is explicitly removed. The API reports Owned, Shared, Borrowed, and Unfunded GPU-hours plus lender hours ([run_types.go:304](/Users/david/mycode/jobtree/api/v1/run_types.go:304)). Those classes depend on the binding, family graph, and lending policy in effect at the time, not merely `paidByPrincipal`. Current replay derives the relationship from the current graph ([evaluate.go:700](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:700)) and attributes historical hours by that derived class ([evaluate.go:1008](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:1008)).

V2 must choose:

- retain referenced historical snapshots;
- store enough immutable mint-time facts to reproduce class and lender attribution; or
- delete classed historical chargeback and report only raw lease GPU-hours.

It chooses none. Thus the unbounded settlement-horizon series is gone, but some bounded history remains necessary.

## 4. Root-subtree sharding is not self-contained

Three things break.

- `INV-REFS-RESOLVE` is impossible per shard as written. It requires every `lending.to` principal to be “present here” ([DESIGN-v2.md:113](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:113)), but cross-root borrowers are deliberately in another shard. Either foreign principals are copied—breaking unique ownership and lineage locality—or validation needs a global cross-shard directory.

- A lending decision crosses two shards. It reads policy from the lender shard and borrower identity/binding from the borrower shard. Current evaluation performs exactly that join when it checks the lender policy against `OwnerOf(run.Namespace)` ([evaluate.go:791](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:791)). A loan therefore needs a composite `(lenderShard, lenderVersion, borrowerShard, borrowerVersion)` authorization, not one scalar `snapshotVersion`.

- Namespace rebinding is a global transaction. Moving namespace UID U from principal A in shard A to principal B in shard B must delete one binding and add the other. Add-first temporarily double-binds U; delete-first makes it unbound. Each shard can pass local injectivity while their union violates global injectivity. The current engine deliberately fails ambiguous bindings to unbound rather than choosing one ([evaluate.go:253](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:253)). Moving a principal or subtree between roots has the same duplicate-or-absent interval.

Consequently, the claim that lending is the only cross-shard edge is false: namespace injectivity, principal uniqueness, trusted roots, subtree moves, and version-pinned authorization are cross-shard surfaces. Ruling 11 needs a global binding index or an explicit two-phase move protocol with a named outage state.

## 5. What only the producer can enforce

The missing invariant is a transition-authorization invariant:

> Given prior accepted state S, authenticated actor A may change only principals, bindings, grants, and edges delegated to A in S; a change may not grant A authority and use that newly self-granted authority in the same transaction.

That includes:

- only root admins may change the trusted-root set;
- a parent must authorize a new child edge;
- a writer may not alter ancestors or siblings;
- namespace binding must be asserted by an authority other than the claimant;
- cross-subtree lending must be authorized by the lender;
- authorization is evaluated against prior accepted authority, not fields supplied by the same change.

No final-document invariant can express authenticated authorship or causality. A malicious producer can emit a perfectly rooted, acyclic, injective document binding the victim UID to the attacker, and every listed invariant passes. Today the raw API is openly self-asserted—`owner` and `parents` are fields on the Budget ([budget_types.go:28](/Users/david/mycode/jobtree/api/v1/budget_types.go:28)) and `NewFamilyGraph` trusts them directly ([funding.go:59](/Users/david/mycode/jobtree/pkg/funding/funding.go:59)).

The design does not state this rule. It names a future “producer-authorization specimen” ([DESIGN-v2.md:160](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:160)) and dismisses Q1 as “RBAC shape only” ([DESIGN-v2.md:188](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:188)). A test name is not a trust model.

## 6. The `maxGPUHours` deletion bill is substantially incomplete

Ruling 11 lists three gates, `nextDepletion`, one validator, and `AggregateCap.MaxGPUHours`. The actual break is broader.

Runtime and API consumers:

- Three spec fields: envelope, lending, and aggregate caps ([budget_types.go:55](/Users/david/mycode/jobtree/api/v1/budget_types.go:55), [budget_types.go:82](/Users/david/mycode/jobtree/api/v1/budget_types.go:82), [budget_types.go:92](/Users/david/mycode/jobtree/api/v1/budget_types.go:92)).
- All three validation paths and `ValidateMaxHoursWindow` ([budget_types.go:219](/Users/david/mycode/jobtree/api/v1/budget_types.go:219), [budget_types.go:257](/Users/david/mycode/jobtree/api/v1/budget_types.go:257), [budget_types.go:299](/Users/david/mycode/jobtree/api/v1/budget_types.go:299), [budget_types.go:313](/Users/david/mycode/jobtree/api/v1/budget_types.go:313)).
- The envelope remaining-hours gate and `fillState.remaining` ([evaluate.go:122](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:122), [evaluate.go:781](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:781), [evaluate.go:842](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:842)).
- Envelope, lending, and aggregate admission lookahead—not listed in Ruling 11 ([evaluate.go:1134](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:1134), [evaluate.go:1167](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:1167), [evaluate.go:1194](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:1194), [evaluate.go:1215](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:1215)).
- Aggregate admission and reporting in [pkg/funding/admission.go:79](/Users/david/mycode/jobtree/pkg/funding/admission.go:79) and [pkg/funding/admission.go:107](/Users/david/mycode/jobtree/pkg/funding/admission.go:107).
- Envelope and aggregate clamps in [evaluate.go:989](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:989) and [evaluate.go:1021](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:1021).
- Budget status’s remaining `gpuHours` fields and controller computation ([budget_types.go:148](/Users/david/mycode/jobtree/api/v1/budget_types.go:148), [budget_controller.go:91](/Users/david/mycode/jobtree/controllers/budget_controller.go:91)). `ConsumedGPUHours` remains; both headroom fields must disappear or acquire a new meaning.
- The `--accounting-period` flag and all Period plumbing become fake no-ops once admission has no integral lookahead ([cmd/manager/main.go:42](/Users/david/mycode/jobtree/cmd/manager/main.go:42), [pkg/admission/admission.go:36](/Users/david/mycode/jobtree/pkg/admission/admission.go:36), [evaluate.go:13](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:13)).
- Generated deepcopy code at `api/v1/zz_generated.deepcopy.go:25,121,518`.

CRD and manifest surfaces:

- `config/crd/bases/rq.davidlangworthy.io_budgets.yaml`: spec fields at `:69`, `:119`, `:130`; status headroom at `:197` and `:279`.
- The duplicate Helm CRD has the same five surfaces in `deploy/helm/gpu-fleet/crds/rq.davidlangworthy.io_budgets.yaml`.
- Samples `config/samples/budgets/budget-vision-lending.yaml:19` and `budget-west-h100.yaml:16`.
- Manifest corpus valid/invalid cases at `internal/manifestcorpus/corpus.go:120,194`.
- Existing live Budget objects and user manifests require a scheduled rewrite before the schema cut. Helm does not automatically upgrade already-installed CRDs merely because the copy under `crds/` changed.

Tests that stop compiling or assert deleted semantics:

- `api/v1/budget_types_test.go:8`
- `controllers/budget_controller_test.go:18`
- `controllers/quota_semantics_test.go:130,156`
- `pkg/cover/cover_test.go:68`
- `pkg/forecast/forecast_test.go:457`
- `pkg/funding/evaluate_test.go`: `TestLedgerCompactionRoundTrip`, `TestIntegralExhaustionDemotes`, `TestWindowReopenRefunds`, `TestAvailableWidthIntegralLookahead`, and `TestPropertyNoOverdraft`
- `pkg/funding/accrual_prefix_counterexample_test.go:82`
- `pkg/funding/tenancy_r7_conflicts_test.go:170`

Formal rails:

- Remove the reduced-cap branch/config from `specs/AccrualPrefix.tla:39`, `AccrualPrefixReducedCap.cfg`, and `Makefile:236`.
- Rewrite the enforced envelope/aggregate/lender hour caps in `specs/LedgerCompactionAccounting.tla:175-179,277-279`, its configs, CI workflow, and proof handoff.
- Update `specs/QuotaEvaluation.tla:19`, `specs/README.md:307`, and the formal coverage/results documents. Metering/compaction proofs remain relevant; cap-depletion proofs do not.

Active documentation that becomes false:

- `docs/project/quota-semantics.md:21-39,71-81,101-107,118`
- `docs/concepts/budgets.md:13,67`
- `docs/fundamentals.md:25,106,210`
- `docs/migrations/kueue.md:105`
- `docs/operator-guide/admin-setup.md:141`
- `docs/user-guide/cofunded-runs.md:29,55`
- `docs/user-guide/researcher-guide.md:24`
- `docs/user-guide/reservations.md:16`
- `docs/project/plan-workload-podspec.md:217`
- `docs/roadmap/design/M0-bootstrap-crd-shells.md:24`
- `M1-budget-accounting-engine.md:31-52`
- `M10-multicluster-aggregate-caps.md:32`
- `docs/roadmap/milestones.md:35`
- `docs/project/DECISIONS-NEEDED.md:25`
- `docs/project/remediation-plan.md:287`
- `docs/project/remediation/R4-pt2-ledger-compaction.md:20-22`
- `docs/project/remediation/R7-tenancy-amendment.md:221,597`

The implementation log, formal campaign/results, prior recommendation, owner rulings, and archived review JSON are historical evidence. They should not be rewritten as though the old system never existed, but they need explicit supersession/disposition so searches do not present them as current requirements.

Bottom line: the design has found a legitimate simplification, but overclaims it. It works only after explicitly dropping fungible GPU-hour entitlements; last-good quarantine creates a revocation-veto attack; root sharding does not contain lending or rebinding; past versions remain needed for pinned in-flight authorization; and the producer’s actual authorization rule is still unstated.
