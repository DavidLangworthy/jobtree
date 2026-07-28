# Formal-verification campaign results — 2026-07-27

## Verdict

The focused campaign found and pinned four production-level violations of the
desired P5/P7/Ruling-6 design:

1. current-snapshot replay rewrites elapsed accrual after delayed `Start` and
   reduced `MaxGPUHours`;
2. an API-legal unrooted squatter Budget changes another namespace's owner
   binding;
3. the interior-owner exemption permits an `Owned` lease whose payer namespace
   differs from its run namespace; and
4. local envelope admission funds 120 descendant GPUs through a manager with a
   100-GPU incoming allocation.

These are executable known-bad specimens, not newly selected product semantics.
The corresponding desired abstractions hold at the finite bounds below, and
every load-bearing condition has an expected counterexample. Production fixes
are not part of this change: Ruling 6 requires the still-unimplemented persisted
settlement store, and authenticated grants require the authority-record decision
described below.

The existing `BlockedFunding` implementation agrees with the new bounded model
and compiled controller specimens for visible/inert blocking, durable onset,
automatic repair, supersession, and scheduler-only minting. This does not close
the separate partial-gang deadline gap.

The follow-up consequence pass also made the deliberate node-failure funding
snapshot executable. It is not a fifth production violation: the controller
documents this as task-#54 policy. TLC and Go now pin the exact tradeoff—an
entry-Unfunded squatter may become Owned after a co-tenant dies in the same
sweep and still be reclaimed, while the entry-funded swap victim progresses.
The stronger fresh-world safety property is an expected counterexample, not a
new desired invariant.

This is a bounded result, not an unbounded proof. The coverage matrix is
`docs/project/formal-verification-coverage.md`.

## Provenance

| Item | Exact value |
|---|---|
| Production behavior/code baseline | `d736ea469ea470f774820ccd288c56bfcce37bc1` |
| Campaign/configuration branch head before uncommitted artifacts | `4497f62bf9f4c0b4225c0de7993b6fe19aff0362` |
| Formal/test artifact state | uncommitted worktree; no result SHA |
| Branch | `codex/tla-smt-codespace` |
| `origin/main` | `d736ea469ea470f774820ccd288c56bfcce37bc1` |
| Merge base | `d736ea469ea470f774820ccd288c56bfcce37bc1` |
| Remote comparison before work | branch ahead 5, behind 0 |
| Machine | 4 cores, 16,379,756 kB RAM |

The requested `git fetch origin main` could not write
`.git/FETCH_HEAD` because this workspace exposes `.git` read-only. A direct
remote query also could not open a DNS socket. The connected GitHub comparison
confirmed the remote `main`, branch head, and merge base above; `origin/main`
had not advanced.

The five commits between `origin/main` and `4497f62` are Codespace/campaign
configuration only. Production behavior assessed here is the code inherited
from `d736ea4`; the formal/test artifacts are an uncommitted layer on top of
configuration head `4497f62`. No result SHA is manufactured for that worktree
state. The sandbox's read-only `.git` is called out again under verification
and handoff.

## Attributed consequence review

Fable (`claude-fable-5`) completed a 53-turn read-only consequence analysis. It
changed no files and ran no tests. Its seven supplied leads were treated as
review input, not as Codex conclusions. Tree inspection confirmed:

- the production pass-2 funding derivation is deliberately frozen while the
  earlier `NodeFailure` model re-derived it;
- physical slot ownership is absent across ordinary admission, reservation
  activation, and swap intent;
- `AccrualPrefix` is a zero-step, two-evaluator abstraction rather than a
  temporal settlement model or execution of `funding.Evaluate`;
- `GrantAuthority` does not reproduce the production interior-versus-leaf
  exemption algorithm;
- outsider-caused binding loss and reservation blocking are only separately
  modeled;
- pod/lease identity coarsening excludes the funded multi-lease machine case;
  and
- compaction equivalence certifies current-snapshot replay, not Ruling 6.

Those verified limits are now explicit in the coverage matrix and residual
gaps. The stale-snapshot lead additionally produced a TLC trace and compiled Go
specimen below.

## Tools

| Tool | Version |
|---|---|
| Java | OpenJDK `21.0.12` |
| TLC | `2.19` (08 Aug 2024, revision `5a47802`) |
| Apalache | `0.58.3`, build `f4ac7ff` |
| Go | `go1.26.5 linux/amd64` |
| Standalone Z3 / cvc5 | not installed; Apalache's bundled backend was used |

Ordinary Apalache checks used `JVM_ARGS=-Xmx5500m`. The known direct universal
ledger-accounting encoding that previously exhausted 10 GB was not rerun.

## Baseline

| Command | Bound | Outcome | Wall time | Peak RSS |
|---|---|---|---:|---:|
| `make ledger-compaction-conformance-check` | all 9,261 canonical three-lease histories | pass | 11.660 s (Go reported 5.511 s) | 162,348 kB |
| `make ledger-compaction-apalache-check` | representative model, length 1 | `NoError` | 35.187 s | 1,041,212 kB |
| `make spec-check` in this sandbox | existing TLC design configs | **environment blocked before exploration** | N/A | N/A |
| `make spec-check` in the outer Codespace shell, after all follow-up edits | `ReservationLifecycle`: 6 distinct; `BudgetConservation`: 31; `QuotaEvaluation`: 1,555 | **PASS reported by orchestrator under TLC 2.19** | not supplied | not supplied |
| `make verify` in the outer Codespace shell, after all follow-up edits | complete repository gate, including race and real envtest | **PASS reported by orchestrator** | not supplied | not supplied |

TLC could not create its local RMI listener, even with one worker:
`java.rmi.server.ExportException: Listen failed on port: 0`, caused by
`SocketException: Operation not permitted`. No nonzero TLC exit is reported as
a model counterexample. Depth-first TLC does not open that listener, so the new
focused `NodeFailure` checks below were run locally with `-dfid`.

## Finite domains and claims

### Accrual prefix

- Hours: `2..5`, denoting half-open intervals `[h-1,h)`.
- Nonzero mutation time: `4`; elapsed prefix: hours ending at or before `4`.
- Mutations: namespace conflict, delayed `Start`, reduced cap, and window
  rotation.
- Tuple fields compared: payer, class, window epoch, and charge.
- Positive design: a persisted prefix is immutable, and a window-epoch key
  starts the renewed window with zero seed.
- Expected counterexample: the production-baseline snapshot replay rewrites the prefix for
  every mutation; omitting the epoch carries old charges into the new window.
- Apalache computation length: 1.

This is a two-evaluator, zero-step design comparison: `Init` chooses a mutation
and `Next` stutters. It does not model a temporal settle-then-edit sequence and
does not invoke `funding.Evaluate`. The fixed Go outputs calibrate selected
points only; there is no direct state-by-state differential seam. An honest
temporal differential was not added because production exposes neither a
persisted prefix nor an effective mutation timestamp to drive
`PersistedAfter`; supplying either in a test would invent the missing
implementation. Comparing two current evaluator snapshots would only duplicate
the existing known-bad specimens and cannot validate the desired persistence
transition.

### Grant authority and instantaneous conservation

- Delegation depth: all values `1..4` in one check.
- Principals: root, four possible chain members, and one outsider.
- Namespaces/branches: 2 each.
- Instantaneous conservation witness: shared ancestor cap 1 and two width-1
  descendant requests.
- Sponsored witness: one unit, charged to the lender lineage exactly once and
  never to the borrower lineage.
- Positive design: authenticated outsider locality, universal owner
  injectivity, Owned-is-local, payer-lineage ancestor conservation.
- Expected counterexamples: accept unrooted claims, exempt an interior owner,
  use local caps only, or key a loan by spender.
- Apalache computation length: 1.

This is representative over two rooted branches, not exhaustive over arbitrary
DAGs. The trusted-edge source is abstract and therefore does not choose the P5
production API.

### Blocked reservation

- One run, one reservation, one two-pod/two-lease gang.
- Clock: `1..3`.
- States: Pending, BlockedFunding, Released; run Pending, Scheduling, Running,
  Terminal.
- Transitions: first block, repeated scan, atomic binding repair/wakeup,
  supersession, and scheduler PreBind.
- Positive design: visible/inert block, original onset, exact state-mirror
  wakeup, release when no longer needed, one gang emission, pod-before-lease,
  and scheduler-only mint.
- Expected counterexamples: frozen gauge, restamped onset, omitted revisit,
  omitted release, and controller mint.
- Apalache computation length: 4 positive; up to 3 for negative traces.

Binding repair and controller wakeup are atomic in this small model. The real
reconciler and envtest specimens are the correspondence rail for the asynchronous
event; fairness does not force the bounded result.

### Node-failure funding snapshot

- Runs: 3; groups per run: 2; nodes: 3; ordinals per node: 2.
- Lease identity: one primary/spare/swap identity per run/group/kind.
- Capacity rank: fixed sequence, capacity 4 in the stale-snapshot witness.
- Pass structure: spare items drain first; `fundingSnapshot` is refreshed after
  each spare casualty and then remains fixed through active-item processing.
- Production-policy positive check: `FreezeFundingForPass2 = TRUE`; all ordinary
  `NodeFailure.cfg` invariants.
- Expected consequence counterexample:
  `PostClosureFundedWorkSurvives` fails when B's first casualty promotes B's
  exact-slot squatter after the fixed snapshot, then A reclaims it.
- Negative-control mutation: `FreezeFundingForPass2 = FALSE` removes that
  counterexample, but is not accepted as policy because it makes decisions
  iteration-order-sensitive and can decline the entry-funded swap.
- TLC exploration is finite and exhaustive for the clean configuration (the
  graph ended at depth 18); no fairness assumption forces the result.

## Measured focused checks

Peak RSS below is the maximum aggregate RSS of the command process tree sampled
every 20 ms. Each negative target runs all named mutation configs serially.

| Exact command | Outcome | Wall time | Peak process-tree RSS |
|---|---|---:|---:|
| `make accrual-prefix-apalache-check` | `NoError`, length 1 | 3.986 s | 269,912 kB |
| `make accrual-prefix-apalache-counterexamples` | 5 intended traces validated | 17.684 s | 277,920 kB |
| `make grant-authority-apalache-check` | `NoError`, depths 1–4 | 3.737 s | 250,492 kB |
| `make grant-authority-apalache-counterexamples` | 5 intended traces validated | 16.824 s | 253,624 kB |
| `make blocked-reservation-apalache-check` | `NoError`, length 4 | 3.379 s | 246,236 kB |
| `make blocked-reservation-apalache-counterexamples` | 5 intended traces validated | 16.352 s | 250,732 kB |
| `java -XX:+UseParallelGC -cp .cache/tla2tools.jar tlc2.TLC -dfid 30 -config NodeFailure.cfg NodeFailure.tla` | no error; 46,932 generated / 2,013 distinct; graph ended at depth 18 | 6.165 s | 807,968 kB |
| `java -XX:+UseParallelGC -cp .cache/tla2tools.jar tlc2.TLC -dfid 20 -config NodeFailureStaleFunding.cfg NodeFailure.tla` | expected exit 12; `PostClosureFundedWorkSurvives` violated at depth 9; 8,811 generated / 1,069 distinct | 2.231 s | 252,116 kB |
| `java -XX:+UseParallelGC -cp .cache/tla2tools.jar tlc2.TLC -dfid 30 -config NodeFailureTopUp.cfg NodeFailure.tla` | no error; 1,049,344 generated / 25,021 distinct; graph ended at depth 20 | 200.923 s | not sampled |

The expected-counterexample wrapper requires Apalache exit 12, the sole named
invariant, an invariant-0 violation, and a trace path. It was also run against a
positive configuration and correctly rejected `NoError`; arbitrary nonzero
solver failures do not satisfy the rail.

## Production correspondence

The compiled tests exercise production behavior inherited unchanged from
baseline `d736ea4`; the tests themselves are in the uncommitted artifact layer
based on configuration head `4497f62`:

| Test | Observed production result | Classification |
|---|---|---|
| `TestCurrentSnapshotDelayedStartRewritesAccrualPrefix` | 32 spent GPU-hours become 0; 32 become Unfunded | known-bad Ruling-6 counterexample |
| `TestCurrentSnapshotReducedCapRewritesAccrualPrefix` | 32 spent GPU-hours become 10 Owned + 22 Unfunded | known-bad Ruling-6 counterexample |
| `TestUnrootedSquatterChangesVictimBinding` | victim owner changes from `org:victim` to empty | known-bad grant-locality counterexample |
| `TestInteriorExemptionAllowsOwnedChargeAcrossNamespaces` | class is Owned with payer `tenant-b`, run `tenant-a` | known-bad Owned-is-local counterexample |
| `TestLocalEnvelopeCapsDoNotConserveAncestorAllocation` | manager cap 100; two descendants remain 120 Owned | known-bad instantaneous-conservation counterexample |
| `TestNodeFailureUsesOneFundingSnapshotAcrossCoTenantDeath` | squatter changes `Unfunded → Owned` after co-tenant closure, but production snapshot reclaims/demotes it and completes the funded swap | executable pin of deliberate task-#54 policy |

Existing compiled specimens supply the namespace-conflict `32 → 0` history,
interior injectivity exemption, blocked reservation lifecycle, scheduler/plugin
mint boundary, and ledger-compaction correspondence. No test claims conformance
to a persisted history or authenticated grant implementation that does not
exist.

## Findings and consequences

The green compaction theorem and the accrual production gap must be read
together:

| Rail | Result | What it does **not** establish |
|---|---|---|
| `LedgerCompaction*` plus 9,261-history Go conformance | compacted current-snapshot replay equals full current-snapshot replay under the stated safety preconditions | elapsed payer/class/window facts are immutable after an effective-dated edit |
| `AccrualPrefix` plus compiled evaluator specimens | the desired persisted prefix is immutable at the finite bound; production replay rewrites delayed-Start, cap, and conflict history | a production settlement store exists, or the zero-step model refines `funding.Evaluate` state by state |

### F1 — current replay violates prospective-history immutability

Changing an existing envelope's `Start` or `MaxGPUHours` rewrites elapsed
charges. The same class already exists for namespace conflicts. This falsifies
`INV-ACCRUAL-PREFIX-IMMUTABLE`; it is not a compaction error and cannot be
repaired by recomputing the current snapshot more carefully.

Operationally, already-spent headroom reappears and historical class/payer
accounting changes. Ruling 6 requires persisted effective-dated facts. Building
that production settlement store requires a separate design/implementation
decision and is an explicit stop condition in the worker brief.

### F2 — unrooted identity assertions are not local

An ordinary API-legal Budget in an unrelated namespace can self-name the
victim's owner. The current global leaf check then unbinds the victim. The
desired rooted abstraction makes the same object inert.

Operationally, an untrusted tenant can block another tenant's new funding and
reservations without changing anything in the victim namespace.

### F3 — the interior exemption creates cross-namespace Owned work

One child naming a principal as `Parent` disables leaf injectivity everywhere
for that string. The same principal may then bind two runnable namespaces, and
the evaluator classifies a cross-namespace payer as Owned.

Operationally, the strongest owner class crosses the namespace boundary that
the conflict rule was intended to protect.

### F4 — descendant consumption does not consume the ancestor allocation

The evaluator admits against each paying envelope and has no grant-lineage cap
path. A manager with 100 incoming GPUs and two 60-GPU descendants funds all 120.

Operationally, a director's quota reduction does not propagate automatically:
no excess demotes, no over-allocation is surfaced, and other tenants bear the
cost. The bounded desired model enforces instantaneous payer-lineage
conservation, but no production grant trace exists to implement it.

### F5 — compaction equivalence is not Ruling-6 history

The existing ledger models remain valid for their stated theorem: compacted
replay equals full replay under current inputs. Recomputing or repairing a
summary under a new window can still reproduce a historical rewrite exactly.
Those results must not be cited as prefix immutability.

### F6 — the node-failure snapshot has a deliberate current-world consequence

`HandleNodeFailure` derives funding once after pass 1. In the executable
witness, B's failed rank closes first and promotes B's healthy exact-slot
squatter from Unfunded to Owned under a fresh evaluation. A's later reclaim
still uses the frozen map, closes the squatter, removes its pod, demotes B, and
completes A's entry-funded swap.

TLC reproduces the same distinction with `fundingSnapshot = Unfunded` and
`closeCurrentClass = Funded`. This violates
`PostClosureFundedWorkSurvives`, deliberately. Setting the model knob to fresh
evaluation removes the trace; mutating Go the same way makes the compiled
specimen fail because A declines the swap. The rail therefore catches both a
silent return to re-derived model semantics and an accidental production-policy
change. It does not endorse fresh per-iteration evaluation or claim task #54 is
an unresolved bug.

## Design decisions required

### Authority-record location

Smallest scenario: a trusted root grants manager M, and M grants lead L. L must
be able to create a researcher grant without gaining authority outside M's
subtree.

- **Grantor-side `Budget.Spec.Grants`:** M writes the grant in M's namespaced
  object. Namespaced RBAC supplies the natural write boundary. A rooted scan
  enables `INV-GRANT-LOCAL`, but Budget lifecycle and identity history are
  coupled and must be persisted for Ruling 6.
- **Cluster authority registry:** one owner-keyed audit/revision surface can
  enforce injectivity transactionally. It enables the same invariants only if
  it adds a delegated subtree ACL; root-admin-only writes defeat self-service,
  while unrestricted registry writes falsify locality.

Operational consequence: the first choice gives subtree-local writes with more
lifecycle coupling; the second gives a central audit surface but needs new
authorization machinery. The abstract model is valid for either and chooses
neither.

### Windowed-hours subtree conservation

Smallest scenario: an ancestor's four-GPU-hour window opens at tick 10; a
descendant's payer window already funded four GPU-hours over ticks 8–12.

- **Intersect with the ancestor window:** only the two hours after tick 10
  consume the ancestor integral. The descendant has two ancestor-hours left.
- **Attribute the descendant's whole immutable payer epoch to the ancestor
  lineage:** all four hours consume the newly active ancestor integral, so the
  subtree demotes immediately at tick 12.

The first needs a rule for consumption before an ancestor allocation existed;
the second can make a newly opened or changed ancestor window arrive already
spent. Each yields a materially different tenant outcome. Until the owner
chooses, `INV-SUBTREE-CONSERVE` is checked only for instantaneous concurrency.

### Partial-gang deadline `U`

Smallest scenario: a two-GPU fixed gang has one scheduler-minted lease and one
missing member, indefinitely.

- A finite `U` enables `INV-BELOW-MIN-BOUNDED`: at `U`, both planes and the
  reservation unwind/requeue, releasing the held GPU.
- Indefinite repair preserves the partial placement but falsifies bounded
  unwind and can hold one GPU forever if the missing mint never lands.

Current binding documents do not select `U`. The campaign therefore does not
encode a deadline or pretend that partial-gang progress is proved.

## Verification

The orchestrator reports the following final outer-shell results against the
uncommitted worktree after all follow-up edits. This worker did not run these
commands inside the sandbox. No wall time or peak RSS was supplied.

| Command | Final outer-shell result |
|---|---|
| `make verify` | PASS, including race tests and real envtest |
| `make spec-check` | PASS under TLC 2.19: `ReservationLifecycle` 6 distinct states; `BudgetConservation` 31; `QuotaEvaluation` 1,555 |
| `make node-failure-spec-check` | PASS: `NodeFailure.cfg` 2,013 distinct states, depth 18; `NodeFailureTopUp.cfg` 25,021 distinct states, depth 20 |
| `make node-failure-spec-counterexamples` | PASS: all nine intended negative traces validated, including `PostClosureFundedWorkSurvives` |
| Six Apalache campaign targets | PASS: 3 positive models and 15 intended counterexamples |

Sandbox-observed focused follow-up checks:

- `TestNodeFailureUsesOneFundingSnapshotAcrossCoTenantDeath`: PASS;
- production mutation `ev = c.evaluate(now)` immediately before the conflict
  classification: the same test failed at the reclaim assertion as intended;
  the mutation was removed and the test passed again;
- clean `NodeFailure.cfg`: no TLC error, complete finite graph at depth 18;
- `NodeFailureStaleFunding.cfg`: expected TLC exit 12 naming
  `PostClosureFundedWorkSurvives` with a depth-9 witness;
- the fail-closed TLC wrapper independently accepted that witness only after
  verifying exit 12, the exact invariant name, and a behavior trace;
- all eight pre-existing NodeFailure negative configs still exited 12 on their
  intended invariant; clean `NodeFailureTopUp.cfg` completed without error;
- model mutation `FreezeFundingForPass2 = FALSE`: no error through the complete
  graph (46,312 generated / 1,953 distinct in 5.094 s); the knob was restored
  to `TRUE`;
- `go test -count=1 ./pkg/funding ./controllers`: PASS with a writable sandbox
  build cache;
- `controllers/run_controller.go` has no residual diff after the Go mutation.

## Residual blind spots

- arbitrary delegation DAGs, cycles, revocation interleavings, and authority
  revision history;
- windowed-hours conservation and exact-once accounting across misaligned
  epochs;
- a production persisted settlement store and model-to-store correspondence;
- partial-gang deadline/unwind and the non-atomic controller/plugin apply seam;
- physical slot ownership spanning ordinary admission, reservation activation,
  in-flight swap intent, and stale scheduler observations;
- outsider grant-locality failure composed end to end with victim reservation
  blocking;
- direct `AccrualPrefix` model-to-`Evaluate` differential correspondence and a
  temporal settle-then-edit model;
- exact production interior-versus-leaf exemption semantics in
  `GrantAuthority` (the Go specimens remain decisive);
- per-machine pod identity spanning multiple leases in `NodeFailure`;
- the stale node-fencing observation across the API read/bridge lock;
- exact closure-writer discovery in TLA (the static AST anti-fake rail remains
  authoritative).

The next highest-value executable experiment that does not require one of the
three product decisions is a small composed physical-slot model spanning
ordinary admission, reservation activation, and pod-before-lease swap intent,
with a double-allocation mutation. A combined outsider-grant-to-reservation
trace is the next production correspondence seam. Arbitrary grant DAG
revocation remains valuable only after the authority record is selected.
