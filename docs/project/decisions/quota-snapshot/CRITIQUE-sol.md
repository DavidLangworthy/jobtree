Verdict: do not ratify this draft yet. The consumer/producer separation is sound, but the draft repeatedly treats “the producer will provide correct history” as if that were a structural guarantee. F1, F2, F5, and deletion blindness are mostly relocated into an unspecified control plane and distribution protocol.

Citations to existing files are against `origin/main@2aa507c`. `DESIGN.md` exists only on `7730e30`, so its citations necessarily refer to that commit.

## 1. The claimed dissolutions

| Claim | Verdict | Why |
|---|---|---|
| **F1: replay rewrites elapsed history** | **Not dissolved** | The evaluator still needs an algorithm that splits every lease at every snapshot boundary and selects the effective snapshot for each half-open segment. Retaining documents does not do that. Today `Evaluate` builds exactly one graph and one envelope index before replay (`pkg/funding/evaluate.go:310-337`), while its timeline contains lease and window boundaries, not snapshot boundaries (`pkg/funding/evaluate.go:570-599`). The draft calls this only “who fills Input” (`DESIGN.md:13-19`), but `Input` cannot even represent a series (`pkg/funding/evaluate.go:26-50`). |
| **F2: unrooted assertion changes another namespace** | **Relocated, not dissolved** | `INV-ROOTED` proves reachability from whatever roots the document names; it does not prove that the roots are trusted or that a parent authorized an edge. Worse, `roots` is inside the self-hashed document (`DESIGN.md:52-55`). A hash authenticates bytes, not authority. The control plane’s delegated ACL and edge-authorization rule are explicitly outside the scheduler (`DESIGN.md:242-246`) and completely unspecified. This assumes away the exact trust question Ruling 1 raised (`OWNER-RULINGS.md:19-26`). |
| **F3: interior exemption permits cross-namespace Owned** | **The narrow defect is dissolved, conditionally** | Removing derivation plus enforcing universal binding injectivity does eliminate the interior exemption. `INV-OWNED-IS-LOCAL` is the right end-to-end rail. But the loader list omits uniqueness of `principal.owner`, and the draft does not say how lease payer identity maps to snapshot principals. Until those are specified and wired into the invariant oracle, this is a design intention, not an enforced closure. The present defect is exactly at `pkg/funding/evaluate.go:203-240`. |
| **F5: compaction equivalence is not immutable history** | **Not dissolved** | Immutable inputs do not imply an immutable or exact-once settlement store. The current compaction theorem proves only current-snapshot equivalence (`formal-verification-results-2026-07-27.md:244-252`). The draft gives no append-only store invariant, correspondence proof, deduplication key, partial-interval rule, or recovery rule. Those are still listed as residual gaps (`formal-verification-results-2026-07-27.md:403-416`). |
| **P6 deletion blindness** | **Reduced only if the series is complete** | N retains the deleted grant and N+1 omits it, but the loader accepts any version greater than the current version (`DESIGN.md:129-131`). If it misses N+1 and receives N+2, the revoked interval disappears from local history. There is no contiguous-version or previous-hash requirement. Last-good retention can also deliberately keep the deleted grant active when N+1 is rejected. |

F4 is correctly not claimed as dissolved. It remains unspecifiable for windowed hours across misaligned ancestor windows; the formal campaign explicitly left that product decision open (`formal-verification-results-2026-07-27.md:340-354`).

## 2. SERIES and straddling intervals

`(envelopeKey, snapshotVersion)` is necessary for historical attribution but insufficient for Ruling 6.

There are two different epochs:

- **Authority/spec epoch:** `snapshotVersion`.
- **Integral-reset epoch:** the envelope’s window epoch.

Consider one open lease and three snapshots:

- V10: window W, cap 40.
- V11: same window W, cap reduced to 10.
- V12: window rotated to W2.

At V11, V10’s spent hours must carry forward: if 32 were spent, the new cap is already exhausted. At V12, those hours must not consume W2. If every snapshot version starts its own balance, any unrelated snapshot update resets quota. If all versions of an envelope are summed, rotation fails to release the old window. The prior recommendation correctly required a distinct **window epoch**, not snapshot version (`FINAL-RECOMMENDATION.md:347`). The draft has replaced rather than complemented it.

A lease `[09:00,12:00)` spanning a V10→V11 boundary at 10:00 must become at least:

- `[09:00,10:00) × V10`
- `[10:00,12:00) × V11`

The draft never defines half-open epoch semantics, tie-breaking when two versions have equal `effectiveFrom`—which non-decreasing ordering permits—or the actual replay split. Therefore the attribution is presently ambiguous.

Settlement makes this worse. If the horizon is 11:00 while that lease remains open, the store must settle its prefix and retain its suffix exactly once. Current code cannot do that:

- A lease is settled only if its entire accrual ends by the horizon (`pkg/funding/evaluate.go:471-477`).
- A retained lease that started before the horizon disables compaction under the no-straddle guard (`pkg/funding/evaluate.go:479-519`).
- `SettleAccrual` selects only fully ended leases (`pkg/funding/evaluate.go:523-552`).

Thus a long-lived open lease prevents the horizon advancing and prevents the claimed snapshot release. If partial settlement is added, an envelope/version aggregate alone cannot prove which lease prefix it already contains. Ruling 6’s proposed invariant is over `(leaseUID × payerEnvelopeKey × class)` hour tuples, not merely envelope totals (`FINAL-RECOMMENDATION.md:153-158`).

Required shape: explicit window epoch, half-open version epochs, snapshot boundaries in the replay timeline, a settled-through watermark or per-lease interval facts, and an exact-once/no-straddle proof for partial prefixes.

## 3. Missing invariants and a catastrophic valid snapshot

This snapshot satisfies all eight listed invariants:

```json
{
  "snapshotVersion": 4413,
  "effectiveFrom": "2099-01-01T00:00:00Z",
  "roots": ["org:root"],
  "principals": [{
    "owner": "org:root",
    "children": [],
    "envelopes": []
  }]
}
```

Assume 4413 is greater than the current version and its hash is valid. It is monotone, immutable, injective, rooted, acyclic, fully resolved, window-sane vacuously, and envelope-unique vacuously. Yet the loader immediately swaps it on receipt (`DESIGN.md:127-134`), decades before `effectiveFrom`, and every current payer disappears. All running work becomes Unfunded (`DESIGN.md:206-207`).

Missing load/series invariants include:

- **`INV-ACTIVATE-WHEN-EFFECTIVE`**: future documents must stage, not swap.
- **`INV-SERIES-CONTIGUOUS`**: require `previousVersion` and `previousContentHash`, or backfill every skipped version before activation.
- **`INV-EPOCH-TOTAL-ORDER`**: define equal timestamps and half-open boundaries.
- **`INV-PRINCIPAL-UNIQUE`**: `principal.owner` uniqueness is mentioned in an example comment (`DESIGN.md:59`) but absent from the eight hard checks.
- **Pinned roots / authenticated producer**: roots must not be self-declared by the same document whose authority they establish.
- **Series completeness at the settlement floor**: the scheduler must prove it possesses every effective epoch, not merely retain every epoch it happened to receive.
- **Settlement exact-once/correspondence**: persisted facts cannot be overwritten, duplicated, or recomputed under a later version.

The skipped-version counterexample is equally severe: N grants, unseen N+1 revokes for five minutes, N+2 restores. N+2 passes all eight and the monotonic check, but loading it makes the revoked interval unrecoverable.

## 4. The non-invariants

The two deliberate choices are right:

- **Over-allocation must remain legal.** Ruling 2 explicitly makes it an expected result of a director reducing a manager (`OWNER-RULINGS.md:28-53`). Only funded consumption, separately by concurrency and hours, is constrained.
- **Principal with no envelopes must remain legal.** Identity and allocation are different facts; otherwise normal deletion/recreation turns into an identity outage (`FINAL-RECOMMENDATION.md:114-120`).

A third state should not hard-reject the whole document: **an unresolved `lending.to` target**.

`children` references must resolve because they carry authority. A lending ACL naming an absent principal carries no present capacity and can safely be inert. Under the draft, an unrelated lender’s stale ACL can make a deletion snapshot fail `INV-REFS-RESOLVE`; last-good then retains the supposedly deleted principal and its grants. A harmless stale ACL has become a cluster-wide revocation blocker. Split the invariant:

- unresolved `children`: reject;
- unresolved `lending.to`: ignore/warn, or require the producer to prune it before publication.

If principal IDs may be reused, lending targets also need a principal epoch rather than a reusable owner string.

## 5. Fail-closed attacks

### Cold start

“Fund nothing” is safe only for forward admission. Historical accounting is undefined.

An existing lease may have run funded for ten hours before the scheduler lost its local series. At restart, the scheduler cannot distinguish those ten funded hours from the post-restart no-snapshot interval. If it later replays under the recovered snapshot, it retroactively bills outage hours despite promising “nothing is charged.” If it marks the whole lease Unfunded, it erases the pre-restart charge. Ruling 6 permits neither.

The no-snapshot period itself must therefore be recorded as an immutable effective epoch, and pre-outage history must already be settled durably. Otherwise cold-start policy recreates F1.

Operationally, “nothing is destroyed” is also too soothing. Unfunded work is explicitly the first reclaim target (`pkg/funding/evaluate.go:281-286`; `quota-semantics.md:31-34`). Cold start also halts fresh funded admissions and can strand partial gangs unless the existing completion carve-out survives the migration.

### Last good

Last-good is fail-open for revocation. A malformed N+1 leaves N’s revoked grant funding work. Whole-document rejection also creates global coupling: one bad lending reference or duplicate principal in tenant A freezes an urgent reduction for tenant B.

The sharpest example is:

1. N contains principal P.
2. N+1 deletes P.
3. An unrelated lender still lists P in `lending.to`.
4. `INV-REFS-RESOLVE` rejects N+1.
5. N remains authoritative, so P was not deleted operationally.

That is deletion blindness through the NACK path. `staleMax` option (b) eventually limits new admissions, but before the deadline the revoked grant remains live; afterward every healthy tenant loses new funded admission. The contract remains incomplete while `staleMax` is an owner question (`DESIGN.md:219-225`).

## 6. Does pointer-direction reversal touch no scheduler code?

Narrowly, **yes after the abstraction exists**: if the scheduler always consumes a stable resolved `children` document, changing upstream authoring from child→parent to parent→children is producer-only.

But it does not support the draft’s claim that this migration is small.

Today:

- `Evaluate` accepts `[]Budget`, builds `NewFamilyGraph` from `Spec.Parents`, derives bindings, and creates envelopes itself (`pkg/funding/evaluate.go:26-50`, `310-374`).
- `FamilyGraph` explicitly consumes child-names-parent and materializes both directions (`pkg/funding/funding.go:50-89`).
- The scheduler directly lists Budgets during restart reconstruction, promise provenance, and admission (`cmd/scheduler/plugin/gang.go:419-425`, `778-795`, `883-915`).
- Promise validation relies on payer Budget namespace/name and `OwnerOfNamespace`, not a resolved principal snapshot (`cmd/scheduler/plugin/gang.go:767-817`).
- The scheduler admission API itself carries Budgets and calls `Evaluate` with them (`pkg/admission/admission.go:36-45`, `104-115`).

So the later authoring flip can touch no scheduler code, but adopting this design unquestionably does. It changes `Input`, graph construction, envelope identity, provenance validation, admission, replay, and settlement. It is not “who fills Input plus delete `deriveOwners`.”

Also, direction independence does not require leaving Kubernetes: the existing graph already normalizes one direction into both maps.

## 7. Strongest argument for keeping authority in Kubernetes

The external snapshot recreates, outside Kubernetes, nearly every facility Kubernetes already provides:

- authenticated writers and RBAC;
- namespaced delegation boundaries;
- namespace UIDs;
- durable ordered revisions;
- watch/list recovery;
- audit;
- HA and air-gapped operation;
- admission validation.

The hardest part is delegated authorization. Namespaced grantor-side CRDs naturally express “this lead may write grants from this namespace.” An external registry must invent its own subtree ACL, exactly the concern the prior recommendation identified (`FINAL-RECOMMENDATION.md:365-375`). The draft has not specified that ACL, yet F2’s claimed dissolution depends entirely on it.

It also adds a second correctness-critical availability domain. The scheduler already depends on the Kubernetes API for Runs, Leases, Pods, Nodes, and namespace UIDs. External snapshot availability becomes an additional conjunct: Kubernetes may be healthy while quota admission freezes or stale grants remain active. Envoy’s last-good analogy is weak because quota documents are mutable authorization grants; stale authorization is itself unsafe.

The strongest Kubernetes design is not “make the scheduler derive from arbitrary live Budgets.” It is:

1. Keep authenticated grants/authority in Kubernetes.
2. Have a controller compile them into immutable, resolved, versioned `QuotaSnapshot` objects.
3. Let the scheduler consume only those snapshots.
4. Retain immutable snapshots through the settlement horizon and put high-volume settled accrual in the P3 store.

That preserves the draft’s best property—the scheduler consumes a normalized document and is indifferent to authoring direction—without moving the trust root, distribution history, UID lifecycle, and delegated authorization into a new unverified system.

The external approach may still win, but only if the design explicitly prices and specifies that replacement control plane. Right now it counts those responsibilities as defects dissolved when they are merely off-screen.
