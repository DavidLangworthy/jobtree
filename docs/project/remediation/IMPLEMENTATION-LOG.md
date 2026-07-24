# Remediation implementation log

Running record of implementation decisions made while executing the R-specs, so
they can be reviewed later. (David asked not to be interrupted for decisions
during the work; each judgment call is recorded here with its rationale.)

## Sequencing

**Chosen order: R1 → R2 → R5/R6 → R3 → R4 → P2–P5 (roughly by priority).**

The README compose note lists R5/R6 first. I moved the two P0 correctness bugs
(R1 phantom-lease leak, R2 gang wedge) ahead of R5/R6 because:

- They are the headline P0 defects (one live-reproduced), and delivering them
  first fixes the most-serious correctness problems soonest.
- They are pure-Go changes to the plugin/controller, fully unit-testable in this
  repo with the fake client — no live cluster needed. R5/R6 is a
  ValidatingAdmissionPolicy whose enforcement (userInfo gating) can only be
  truly verified on a kind cluster, so it is a heavier, less-immediately-testable
  first step.
- R1/R2 do **not** depend on R5/R6. Only R3 hard-depends on R5/R6 (its `Promise`
  marker must be forgery-proof), and R5/R6 still lands before R3.
- R1 is done before R2 (swapped from the note's "R2 → R1") because R1 is the
  smaller, self-contained, live-reproduced change; it introduces the shared
  `PostBind` + stale-gang sweep that R2 then builds on.

## Decisions (chronological)

### R26 — the ledger auditor, the runtime backstop (2026-07-23, unattended run)

`funding.Evaluate` is a fuzz-tested pure function that replays a ledger nobody audits:
there was no loop comparing open leases against the pods and nodes that are actually
there. Every known leak on this board is the same shape — an open lease charging a
budget with no live work behind it — and the point fixes (R8, R25, eviction recovery)
each close a KNOWN leak at its cause. R26 is the net UNDER them: a periodic
`LedgerAuditor` that reads the whole world and enforces two invariants, so ledger
integrity is a CHECKED property, not the sum of the leaks we have thought of. This is
a **sole-closer-path change** — flagged for the milestone review as F6.

The judgement is a pure function (`controllers/ledger_audit.go`, `AuditLedger`) so it
is unit-testable against hand-built worlds; the controller (`controllers/kube`) does
the I/O, grace timing, and repair.

**The design is conservative by construction — a wrong auditor is a reaper, which is
worse than the leak.** Every decision below trades coverage for a guarantee of no
false close:

- **The dead-node rule keys off the Node OBJECT's existence, never its readiness.**
  This is the same fencing rule as the swap path (`node-failure-needs-fencing`): a
  NotReady or cordoned node still exists and its lease is not orphaned; only a deleted
  Node is a real loss. Crucially, the auditor does **not** reuse the bridge's world
  load, because that load drops UNUSABLE nodes — a NotReady node would vanish from it
  and look deleted. `loadWorld` lists ALL Node objects unfiltered. Mutation-verified:
  make the existence check fire on present nodes and both the dead-node and the
  present-node-is-not-dead tests redden.
- **The no-pod rule acts only on POSITIVE evidence and defers what it does not own.**
  It fires only for an open Active lease of a run that is PRESENT and NON-TERMINAL,
  whose durably-annotated pod is genuinely absent. An absent run is left to the
  finalizer / cleanupDeletedRun (guessing there rebuilds the R27c orphan-run reaper R12
  deleted); a terminal run's leases are left to SettleLeases (racing it would close the
  same lease with two different reasons); a legacy lease with no pod-name annotation is
  declined rather than matched by a guess. A Pending pod counts as live — it is the
  rank being re-provisioned by recoverEvictedRanks, and closing its lease would race the
  recovery. Mutation-verified: drop the terminal-run defer and the defer test reddens.
- **Repair is budget-safe only, and gated on a GRACE window.** The destructive
  direction CLOSES leases (stops a charge) via the sole closer with the DISTINCT reason
  `"Orphaned"` — never `Completed` (laundering a leak as a completion is how it stays
  invisible) and never `SweptTerminalRun` (that means the run was terminal; the auditor
  closes leases of runs still wrongly alive). It NEVER deletes a pod: a pod running
  without a lease is ALARMED, not killed, because killing it is a policy call (R5/R6)
  the auditor does not own. A violation is acted on only after it has PERSISTED across
  a grace window (default 2× the sweep interval, clamped up if set smaller) so a healthy
  in-flight mint/swap — a failed lease closed one instant, its replacement minted the
  next — is never repaired. The grace window must exceed the swap window; that is the
  clamp. Mutation-verified: drop the grace gate and the auditor closes within the
  window, reddening the before/after-grace test — the reaper the design exists to
  prevent.
- **Invariant 2 is checked at RUN granularity, not pod granularity.** "A live run with
  jobtree pods and ZERO open leases" is the signal, not "a pod with no lease of its
  own" — because a forming gang legitimately has some open leases and a not-yet-minted
  pod (the AwaitingMint transient `pkg/invariant` already documents). Run-granularity
  makes that healthy window invisible where a pod-level check would cry wolf on every
  gang still assembling.
- **The gauge reports SUSTAINED violations, not instantaneous ones.**
  `jobtree_ledger_violations{kind}` is set to the count PAST the grace window, and all
  three kinds are enumerated each sweep so a kind that clears is published as 0, not
  left at its last value. `jobtree_ledger_repairs_total{reason}` counts every close —
  each increment is both a repair and a bug report that a causal path should have closed
  it first. This is a small deliberate departure from the spec's "gauge of current
  violations": an instantaneous gauge flickers on every healthy mint/swap window, which
  is noise for a signal whose whole purpose is "the ledger is actually broken."
- **The auditor runs off its own client, not the Bridge.** It must OBSERVE the engine,
  not run it: going through `Bridge.WithWorld` would execute the whole
  engine/settle/apply pipeline on the auditor's schedule. It is a manager `Runnable`
  driven by a ticker (a periodic sweep, not an object reconciler), invocable as `Sweep`
  for deterministic tests, and its `repair` closes through the sole closer + a
  `Status().Update` with conflict retry — it is not a second writer of the closure
  fields, which keeps `hack/antifake`'s sole-closer lint true.

**Verification.** The pure `AuditLedger` has thorough unit coverage (dead-node vs
no-pod precedence, fencing, terminal/absent-run defers, closed-lease and legacy-lease
skips, spare exclusion, the AwaitingMint non-trip, a clean healthy world). The
controller has fake-client Sweep tests (close-only-after-grace, the sustained-gauge
semantics, alarm-without-touching for pod-no-lease, a healthy world left alone) and a
real-apiserver **read-path** envtest proving `loadWorld` projects live Node/lease/run
objects into the world AuditLedger expects (a present node read as present, a ghost-node
lease flagged). Three load-bearing rules — fencing, terminal-defer, grace — are
mutation-verified. The apiserver's acceptance of the `Orphaned` close is covered by the
fake-client close test and R14's existing closure-monotonicity envtests; a write-path
envtest was dropped deliberately because the shared suite manager actively reconciles
the orphan run and races the lease status, which tests the manager's run-failure timing
(the run fails, the auditor then correctly DEFERS to SettleLeases), not the auditor.
`make verify` green (incl. envtest + the 800-seed eviction fuzzer).

### R23 — from a Run to its pods, logs, and outputs (2026-07-23, unattended run)

The CLI could show scheduling and accounting but never the workload itself: once a run
was Running the researcher had to know the pod-naming scheme and drop to
`kubectl get pods -l rq.davidlangworthy.io/run=<run>` to find their own containers, and
there was no story at all for getting results out. R23 adds `runs pods`, `runs logs`, and
`runs artifacts`, and documents the `/artifacts` convention. **No engine, no plugin, no
funding path** — three new CLI files on the existing live-client surface, so this is not a
sole-committer change and carries no milestone-review flag.

**Judgment calls:**

- **Per-pod "funding class" is reported as the paying ENVELOPE, not a derived class.** The
  spec's `pods` column says "lease/funding class". The funding *class*
  (Owned/Shared/Borrowed/Unfunded) is a run-level DERIVATION `pkg/funding` computes from
  the whole ledger — and the CLI's standing rule (Track G) is that it must never re-run
  funding client-side and race the manager's brain. So `pods` joins each pod to its OPEN
  lease (via the `pod-name` annotation the sole committer already stamps) and shows that
  lease's `PaidByEnvelope`: the honest, recorded, per-pod signal of who is paying, with
  the class left to `explain`, which is where the run-level derivation is surfaced.
- **`logs` is live-only and refuses `--local` rather than fabricating.** A container log
  comes from a real kubelet; the `--local` simulator runs no containers. Inventing lines
  there is the exact fake the `--local` notice exists to prevent, so `logs --local` errors
  with a pointer at the live path. `pods` and `artifacts`, which read the plan/spec, work
  in both modes (under `--local`, `pods` shows planned pods with phase `Planned`).
- **`--rank` counts pods in one stable order, defined once.** `pods` sorts active before
  spare, then by group index, then by name; `logs --rank N` selects the Nth pod in exactly
  that order, so "rank 0" is deterministically the first active member — what a researcher
  means by "show me the logs" unqualified — and the number they see in `pods` is the number
  `logs` selects on. A role filter (`-r`) narrows first, then rank counts within the filtered
  set.
- **`logs` uses a typed clientset, built on demand.** Streaming a log is the `pods/log`
  SUBRESOURCE, which controller-runtime's `client.Client` cannot request (GetLogs lives on
  client-go's typed CoreV1 client). Rather than thread a clientset through every command's
  `RootOptions`, `newLiveClientset` builds one from the same kubeconfig resolution, only in
  the one command that needs it. `streamPodLog` depends on a narrow `logOpener` func so the
  copy/flag-forwarding logic is unit-testable; the actual GetLogs round-trip needs a live
  kubelet and is proven by kind, not unit tests.
- **`artifacts` reads the answer back out of the template the researcher wrote, and names
  the durability trap.** No new storage, no new spec field: the run already carries a full
  `PodTemplateSpec` per role, so `artifacts` walks its writable volume mounts (skipping
  `readOnly` input mounts) and describes each backing volume. A PVC/CSI/NFS volume is
  reported as persisting; an `emptyDir` is flagged **EPHEMERAL** — a checkpoint written
  there is lost on the first node move, which is precisely the failure spares exist to
  survive, so surfacing it before the run is the whole value. An empty result is rendered
  as one explanatory row (how to mount `/artifacts`), not a bare table that reads as "no
  data".

**Verification.** Unit tests: the label selector returns only the run's pods; the
active-before-spare/group/name sort (which `--rank` depends on); the envelope join uses
OPEN leases only (a closed lease no longer names a payer); rank selection defaults to 0,
honors `--rank` and `-r`, and errors actionably out of range / on an absent role;
`streamPodLog` copies verbatim and forwards `--previous`/`-f`/`-c`; `--local` logs refuses;
artifact mounts classify PVC-vs-emptyDir durability, skip read-only inputs, and flag a
mount naming an undeclared volume. The documented `--local` examples (`pods`, `artifacts`)
run under the doc-examples rail that R12 added. `make verify` green.

### R20 — the plugin narrates its own decisions (2026-07-23, unattended run)

The scheduler plugin is where placement and funding are actually decided, and it
emitted **zero** Events. A gang parked in Permit was invisible: the Run said `Pending`,
the controller's Events said it had asked for GPUs, and the reason it was not starting
lived only as a string inside a `Wait` status that a researcher had to know to go read
off individual pods. R20 gives the plugin an EventRecorder and a fixed vocabulary,
emitted on the **pod** (where the framework's own scheduling events live, so they read
together) and mirrored onto the **Run** (so `kubectl describe run` / `runs explain`
answer "why isn't it starting?" without pod spelunking).

**The load-bearing constraint: this narrates, it decides nothing.** No emission site
changes a verdict; the recorder is nil-safe so a plugin built without a handle (every
existing unit test) behaves exactly as before; the funding hot path (R4) gains no
synchronous API write on the decision path. The two caches R20 adds (`runUIDs`,
`lastForming`) are reaped by the SAME gang sweep that bounds `gangs`, so an
observability feature cannot become the unbounded map R1 was about.

**Judgment calls:**

- **Unfundable and unplaceable are preserved as DIFFERENT answers, off the error TYPE.**
  `decide` collapsed both into one string and Permit labelled every refusal "not
  fundable" (`plugin.go`, `gang.go`) — which sends anyone debugging an overcommitted
  cluster to argue with a budget that is fine (funding-model review, 2026-07-08). The
  fix stores a `refusalKind` on the `gangCommit`, classified with `errors.As` on
  `pack.PlanError` vs anything else, so a wrapped pack error is still a pack error and an
  untyped error degrades to the pre-R20 "unfundable" rather than to a confident wrong
  one. Stored, not returned, so `decide`'s signature and its many call sites are
  untouched and a cached verdict carries the same distinction as a fresh one. The Permit
  status STRING keeps its historical wording ("not fundable: …", matched in tests and
  read in logs); only the Event carries the new distinction.
- **`GangTimeout` is measured, not guessed.** Permit stamps `parkedAt` when a member
  parks; Unreserve reports a timeout only when `elapsed >= permitTimeout`. A member
  unreserved for any other reason (a rejected sibling, a bind failure) says nothing,
  because attributing it to a timeout would attribute a cause we do not know. The stamp
  is dropped in `forget`, which the framework calls for every unreserved pod, so
  `parkedAt` is bounded by the pods in flight.
- **`FlavorMismatch` is emitted from PostFilter, not Filter.** Filter runs per NODE and
  cannot know it rejected them all; PostFilter is the once-per-unschedulable-pod hook. It
  fires only when NO node carries the requested flavor — if some node has it, the pod is
  unschedulable for an ordinary reason (full nodes, taints) the framework's own
  `FailedScheduling` already explains better than we could. PostFilter otherwise keeps
  its existing no-op reclaim verdict unchanged.
- **`GangForming` is throttled per gang (30s).** Permit runs per member per attempt, so a
  64-pod gang that re-forms produces hundreds of identical observations; the Event API's
  own aggregation does not collapse them because the changing (k/N) counts — the useful
  part — are in the note. Throttling per gang keeps the signal and a readable Event list.
- **The Run mirror needs the real UID, and that is the whole feature's failure mode.**
  `kubectl describe run` selects Events on `involvedObject.uid`; a reference carrying only
  name+namespace produces Events that exist in the API and are invisible where anyone
  looks. The gang manager caches the UID (one Get per gang lifetime, never per Event on
  the hot path) and returns "" — silently skipping the mirror — when it cannot read the
  Run, because an Event on a made-up reference is worse than no Event.
- **The recorder's scheme is the BINARY's, registered in `main.init`, not the plugin's.**
  The framework's EventRecorder turns the mirrored object into an ObjectReference with
  `reference.GetReference` against the GLOBAL client-go scheme — a Run is not in it, so
  without registration every Run-mirrored Event is dropped with "no kind is registered"
  (logged, not returned: the plugin would look like it was narrating while
  `kubectl describe run` stayed empty). `main_test.go` pins exactly this and is
  mutation-verified: delete the `AddToScheme` and it fails with
  "no kind is registered for the type v1.Run". The plugin's own API-client scheme is a
  different scheme; registering in `New` would not fix the recorder.
- **`explain` shows only LIVE Events, matched on UID.** The `--local` simulator has no API
  server and therefore no Events; inventing plausible ones there would be a fake, so the
  aggregation is live-path only. Events are matched on the Run's UID (a delete+resubmit of
  a same-named Run is a different run; showing the dead incarnation's events would be a
  confident wrong answer), sorted on the best available timestamp (`EventTime` for the
  events.k8s.io path, `LastTimestamp`/`FirstTimestamp` for the legacy path — sorting on
  the wrong one silently reverses the list), newest last and capped at 8 so the current
  answer is the last line and a fifty-times-re-formed gang does not bury it.

**Coordination with R23 recorded, not deferred:** R20's spec says to "coordinate the CLI
aggregation once" with R23. R20 lands the Event-aggregation half in `explain`
(`withRunEvents` / `liveRunEvents`); R23 adds `pods`/`logs` on the same live-client
surface and reuses `liveRunEvents` if it needs Run Events. No second aggregation path.

**Verification.** Unit tests, all mutation-verified: refusal classification is by type
not message (a wrapped pack error stays unplaceable; an untyped error degrades to
unfundable); the two refusals render with different reasons and steer the reader to
different places; forming is throttled per gang and the throttle expires; only a full
`permitTimeout` wait counts as a timeout and the stamp dies with the pod; the UID is
fetched once for ten Events and an unreadable Run silences the mirror; and the recorder's
scheme can reference a Run (the silence-failure guard). `make verify` green (fmt, vet,
generate, antifake, `-race` incl. the 800-seed eviction fuzzer, envtest, golden, helm).
R20 touches no `controllers/` or `pkg/funding` code, so envtest and the fuzzer confirm no
regression rather than exercising R20 directly; the plugin runs inside the scheduler
binary, which envtest does not stand up, so the emission sites are proven by unit tests
and the scheme-registration guard. Flagged for the milestone review in
`DECISIONS-NEEDED.md` (F5) as a sole-committer-path change, even though it is observe-only.

### R15 — the documented install made real (2026-07-23, unattended run)

Four separate things had to be true for `helm install` to work, and none of them were.
Fixing three and leaving one would still produce `ImagePullBackOff`, so they land together.

- **`release.yaml` built no images at all.** Added an `images` job that logs into GHCR
  and pushes both Dockerfile targets (`manager`, `scheduler`) as
  `ghcr.io/<owner>/jobtree-{controller,scheduler}` at `${GITHUB_REF_NAME}` **and**
  `latest`. It runs *before* `package`, and `package` now stamps the chart with
  `--version ${VERSION#v} --app-version ${VERSION}`. That ordering is load-bearing:
  the chart's image tag defaults to `.Chart.AppVersion`, so a chart packaged with the
  tag whose images were just pushed installs correctly with **no flags at all**. The
  job then re-renders the packaged `.tgz` and greps for the two expected references —
  proving the two halves agree rather than assuming it.
- **`--set image.tag=…` was a silent no-op**, and the operator guide told admins to use
  it. There was no `image` key; `controller.image` was one full reference string pinned
  at `:latest`. **Judgment call: wire the flag rather than delete it from the docs.**
  The audit allows either, but the operator's intent — pin a deployed build — is
  legitimate, and the standard Helm shape (`repository`/`tag`/`pullPolicy` per component
  under a chart-wide `image.tag`) delivers it in ~6 mechanical files. `e2e-local` images
  now come in via `--set controller.image.repository/tag` from `hack/e2e/install.sh`
  (derived from the existing `E2E_IMAGE`, so `versions.env` is untouched).
  `imagePullPolicy` is now explicit (`IfNotPresent`): the previous `:latest` default made
  the kubelet's implicit policy `Always`, which would fail on a kind-loaded image the
  moment a tag stopped being `latest`.
- **The `notifier` was a phantom and is DELETED, not defaulted off.** There is no
  `cmd/notifier`, no Dockerfile stage, no job that ever pushed
  `ghcr.io/davidlangworthy/jobtree-notifier` — and it was `enabled: true` by default, so
  `helm install --wait` hung on ImagePullBackOff forever. The e2e harness carried a
  standing accommodation for exactly this. Both options are inside the audit's sanctioned
  set ("default off — or delete it entirely"); deletion is what the repo's own
  fake-features discipline requires, and hiding a nonexistent component behind a flag is
  how it survived this long. Removed from `values.yaml`, `deployment.yaml`,
  `_helpers.tpl`, both kustomize overlays, the e2e overlay, and the chart description.
  Two docs that named `cmd/notifier` as an extension point were corrected in place.
- **The helm repo (`https://davidlangworthy.github.io/jobtree`) 404s.** Chose to *fix
  the docs*, not publish an index: GitHub Pages is not enabled for this repo (the docs
  site is readthedocs), an unattended run cannot verify a Pages deploy, and an unserved
  index is the same broken promise relocated. Every release already publishes the
  packaged chart as an asset, so `README.md` and `admin-setup.md` §3 now install from
  that `.tgz`, and explain what a source-checkout install needs instead.

**Two new rails in `hack/ci/helm-assertions.sh`, both mutation-verified:**

1. **Every rendered container image must be one this repo builds.** The Dockerfile has
   two targets, so the chart may name two repositories. Mutation: repoint
   `controller.image.repository` at `jobtree-notifier` → red. This is the rail that makes
   a phantom image impossible to reintroduce, and it would have caught the notifier on
   the day it was added.
2. **`image.tag` must reach every container.** Renders with `--set image.tag=pin-me-9a1b`
   and requires exactly 2 hits; separately asserts a per-component tag overrides the
   global one, and that the *unset* default is the chart's `appVersion` (the property the
   release job depends on). Mutation: hard-code the manager's image back to a literal →
   "reached 1 of the 2 chart containers". A documented flag the template ignores is worse
   than no flag, because nothing fails.

Not done, deliberately: no Helm repository index, and no change to `Chart.yaml`'s
committed `appVersion` (`0.1.0`) — a source checkout is a development install and the
guide now says so.

### R11 — status conditions, with the phase DERIVED from them (2026-07-23)

The spec's requirement is "`Phase` is a pure function of conditions". The obvious
reading — invert the controller so each decision point sets a condition and the phase
falls out at the end — is a rewrite of a 2400-line, funding-adjacent controller with 25
status-write sites, during a run whose own playbook forbids the adversarial review that
class of change normally gets. The shape chosen instead gets the same guarantee for a
fraction of the blast radius:

**A call site names a STATE, not a phase.** `v1.RunState` is a value in the API package
carrying a reason, the conditions it turns on, and the phase they imply.
`SetRunState` writes every managed condition and then sets
`Phase = DeriveRunPhase(status.Conditions)`. So the phase is genuinely derived — not
written beside the conditions and hoped to match — while each call site still says one
thing. A state that does not exist is not representable (it is a value, not a map key),
so there is no unknown-reason branch to get wrong.

**Three judgment calls:**

- **The phase constants moved to `api/v1` and are re-exported from `controllers`.** The
  spec says "move the phase-string constants into api/v1"; there is now exactly one
  definition, and the CRD/derivation live beside it. Rewriting ~200 call sites to
  `v1.RunPhaseRunning` would have buried the taxonomy under an identifier rename, and
  `RunPhaseRunning` remains the natural spelling inside the engine.
- **A reason may ride on a `False` condition.** The first draft stamped reasons only on
  True conditions — which meant `Unfunded` and `Unschedulable`, the two states a
  researcher most needs explained, had **no reason anywhere**, because in those states
  nothing is True. `whenFalse` fixes it: the reason goes on `Admitted=False`. This is
  standard (`Available=False, Reason=MinimumReplicasUnavailable`), and it is the
  difference between the vocabulary being useful and being decorative.
  Mutation-verified in both the unit and the envtest layer.
- **`Blocked` derives differently either side of admission.** Blocked *before*
  admission is `Waiting`; blocked *while* admitted is `Pending` — the run still holds
  GPUs and a checkpoint deadline is running against it. Collapsing them would report a
  run burning budget as though it were merely queued.

**The oracle, not more review.** `INV-PHASE-DERIVED` was added to `pkg/invariant`: if a
run's persisted phase differs from what its own conditions derive, every engine entry
point panics under test. The projection feeds `v1.DeriveRunPhase` rather than
reimplementing it — an oracle that re-derives the rule it checks only proves someone
wrote the same bug twice. It **immediately caught a real drift**: `seedRunning`, the
plugin stand-in used across the engine tests, set `Status.Phase = Running` directly on a
run whose conditions still said Pending. Fixed at the fixture, which now goes through
the vocabulary as the real adoption path does. The 800-seed eviction fuzzer now checks
this on every state it generates.

**The envtest caught the first draft being INERT — the class AGENTS.md names.**
Conditions were stamped in `admission.PodLeaseWithRole`, at the mint. `Status` is a
subresource, so the plugin's `Create` silently drops it: an open lease persisted **no
conditions at all**, and every unit test passed. Two ways out, and the cheap-looking one
is wrong: making the plugin follow its `Create` with a `Status().Update` puts a second
API write per pod on the sole committer's hot path (the path R4 exists to make faster)
and adds a create-succeeded-status-failed state. The derivation moved instead to
`Bridge.apply`, the controller's existing observation point, where the status diff
already persists lease changes. The committer pays nothing and a minted lease gains its
conditions on the next pass.

**Two allowlist ratchets shrank 4 → 2, and neither by accepting anything.** The lint
flagged the wire-or-delete triage the allowlist had been asking for since it was
written. `BudgetStatus.ObservedGeneration` is now **wired** — the reconciler stamps it,
which is the freshness contract its name always promised. `RunStatus.Generation` is
**deleted**: `status.conditions[].observedGeneration` is the standard, per-condition
version of the same idea, and keeping a second unwritten one is precisely the fake the
lint exists to catch. Also declined: adding a third entry to the terminal-pod-phase
allowlist (capped at 2, shrink-only) just to re-prove `kubectl wait` on `Completed` —
the existing `TestRunCompletesWhenPodsSucceed` owns that injection, and the polling
mechanism is already proven on `Running`.

Conditions also landed on Lease (`Active`/`Closed`, reason = the closure reason verbatim,
written from `CloseLease` — the sole closer — so it can never lag the fact),
Reservation (`Forecast`/`Activated`) and Budget (`Healthy`/`Overcommitted`), each derived
from status the controller already writes rather than tracked independently.

### R12 closed out: the ownership edges verified against a real apiserver (2026-07-23)

The code landed 2026-07-10; what was owed was verification, and the board disagreed with
itself about whether the ownerRefs existed (the header said done, step 2 said "still
owed"). They do exist — `runOwnerReferences` is applied at `buildPod`, the Reservation
create, and `ensureRunService`. So this is verification, not implementation, and the
honest question was *what can envtest actually prove.*

**It cannot prove cascade GC, and no test in `controllers/kube` should pretend to.**
envtest runs an apiserver and etcd and **no kube-controller-manager**, so there is no
garbage collector. "Delete the Run, watch the pods disappear" would pass or fail for
reasons that have nothing to do with jobtree. Stating that at the top of the new file
matters more than the tests below it: the next person to add a GC assertion here needs
to know why it will lie.

**What it can prove, and the fake client cannot: that the reference is one a real GC
would resolve.** `assertOwnedByRun` re-reads the Run through the API and compares the
UID, and compares group/version/kind against the *scheme's own* answer rather than a
string literal — a literal in the test would only repeat whatever typo production made.
Mutation: set the pods' `ownerReference.APIVersion` to `"v1"` (a plausible-looking,
completely inert value) → red on every pod and the Service. The fake client accepts that
mutation silently, which is exactly the gap.

**Three judgment calls:**

- **Pinned the negative: a Lease must have NO ownerReference.** The design refuses to
  owner-ref a funding fact, because cascade-deleting one erases accounting history and
  lets a force-deleted Run escape its charge. That is a *decision to not do something*,
  and nothing was stopping a future "make GC uniform" cleanup from undoing it silently.
  Now it trips a test.
- **The delete tests assert ORDER, not just outcome.** Checking "the leases end up
  closed" would pass even if the finalizer were removed first, because the NotFound
  backstop closes them a moment later. The tests observe the Run's disappearance and
  then read the leases, so the claim is *closed before the object could vanish*.
  Mutation: move `RemoveFinalizer` ahead of the closure → red, deterministically
  (`closed=false reason=""`), on both the graceful and `--force --grace-period=0` paths.
- **Verification item 5 is written as a sampled property, deliberately.** The claim is
  about *every* world load — "at no `Bridge.WithWorld` pass does an open lease exist
  whose Run is absent" — and from outside the only honest check is to look often,
  through the whole deletion window, against the real object graph. It is sound because
  the finalizer is removed only after `WithWorld` has applied the closures: once the Run
  can vanish its leases are already closed, so the sampling has no window to miss. It
  carries a **non-vacuity guard** (it fails if no load ever saw an open lease), because
  a sampled property that never sampled the interesting state is a decorative test.
  Mutation: disable the finalizer install → "load observed 1 OPEN lease(s) for a Run
  that is absent from the world". That is R27c's `orphan-run` premise, reproduced on
  demand, which is the licence for having deleted the rule.

Not done, deliberately: the spec's **optional** Lease finalizer (design decision 3). A
finalizer on a Lease would block CRD deletion during an uninstall — the exact
non-obvious wedge R18's ordered uninstall exists to avoid — and buys nothing the Run
finalizer does not already guarantee, since closure now precedes the Run's deletion.

### R19 settled, and the project shell made honest (2026-07-09)
David: *"I'm not ready to give this away yet, but I want to be able to talk about
it."* Also: *"maintainers is a good place to give yourself credit for doing all this
work... But don't make stuff up and don't put my email in the repo."* And, on the
roadmap: *"milestones is also complete fiction now."*

- **He asked for "MIT or some non-commercial licence" — and those are opposites.**
  MIT *permits* commercial use; a non-commercial licence forbids it, and for a GPU
  cluster scheduler that forbids essentially every real deployment. Surfaced rather
  than silently resolved. What he actually wants is **source-available with no rights
  granted**, which is neither. Ruling: **no licence yet**, stated explicitly.
- **An absent LICENSE file is ambiguous**; an explicit one is not. `LICENSE` now says
  all rights are reserved, notes that GitHub's terms let other users view and fork
  within GitHub, and records that a licence may be granted later. It says plainly that
  it is a statement of intent, not legal advice.
- **`MAINTAINERS.md` was worse than R19 recorded.** Beyond the dead `.example`
  security contact, it invented **four maintainers who do not exist** —
  `@gpu-binder`, `@budget-controller`, `@researcher-experience`, `@metrics-squad` are
  *component names* — plus a pager rotation, a two-maintainer emergency process, and a
  majority-vote membership procedure. The same fabrication was **published on the docs
  site** as `docs/MAINTAINERS.md`. Both replaced with the truth: one maintainer, no
  rotation, no quorum, no vote, and no contributions accepted (there is no licence to
  contribute under).
- **Security reporting with no published email.** GitHub's *private vulnerability
  reporting* gives a "Report a vulnerability" button that opens a private thread. It is
  currently **disabled** and cannot be enabled by the agent's token (403, needs admin):
  *Settings → Advanced Security → Private vulnerability reporting → Enable*, or
  `gh api -X PUT repos/DavidLangworthy/jobtree/private-vulnerability-reporting`.
  `SECURITY.md` links the report form and states scope honestly — in scope: the funding
  path, the R5 trust anchor, tenancy, RBAC; out of scope: anything requiring `budgets`
  write, since budgets are administrator-owned by the R7 design.
- **Authorship, as a checkable fact rather than a flourish.** **97 of 189 commits**
  carry a `Co-Authored-By: Claude` trailer; a reader can run `git log --grep=` and
  verify it. **Claude is explicitly not listed as a maintainer:** a maintainer must be
  able to answer a security report and take responsibility for a merge, which is a
  property of people, not of models — the same principle the project's own tenancy
  design uses, where *permissions flow with accountability*. Credit goes in the
  history; responsibility stays with the human.
- **The milestone roadmap was publishing false claims.** `docs/roadmap/milestones.md`
  ticked M0–M9 and cited two packages that no longer exist (`pkg/budget`, `pkg/policy`).
  M3's "definition of done" described the pre-cutover architecture in which the *run
  controller* admits and binds. M6 claimed *"runs configured with spares survive node
  failures without losing world-size"* while R21 can put **two live copies of the same
  rank** on the cluster, R22 closes co-located runs' leases, R25 leaks an immortal
  charging lease, and R8 never notices a failed pod at all. M9 claimed
  "production-ready" packaging while R15/R16/R17 are confirmed live bugs. Corrected in
  place, with a banner: **a ticked box means "scope built and unit-tested," not
  "correct."** The authoritative status is the remediation board.

### R7 settled: the tenant is the namespace (2026-07-09)
David gave four rulings, each quoted verbatim in `R7-tenancy-amendment.md`. A Fable
design pass turned them into a design; three adversarial critics (security, workflow
coherence, blast radius) raised nine objections; Fable answered every one in §14.
Verdict: **sound-with-fixes, no blocking objections. No decisions remain for David.**

- **The rulings.** (1) There is one kind of funding principal — *"a project could lend
  to or borrow from another project or user, just like a user. And a project lives in a
  team and gets family sharing just like a user."* So a **team is a group**, an interior
  node of the tier tree, not a principal. (2) *"Users have more accountability than
  teams, and permissions flow with accountability"* — quota spendable without an
  accountable individual is a hazard. (3) **The namespace pays**: *"if Bob gives Alice
  his wallet it's his money that gets spent."* (4) The namespace→tier binding is
  **admin-set** today; self-service tooling is deliberately deferred because it is
  *"worthless if there is nothing to run it."*
- **`Run.Spec.Owner` is DELETED.** Today it is a free string checked only for
  non-emptiness (`run_types.go:295-296`) while R5's VAP matches **pods only** — so a
  researcher can set `owner: org:ai:victim` and class Owned against a victim's envelope.
  Deriving the owner from the namespace removes the field, and with it the whole forgery
  class. A subtractive fix beats a guard.
- **R7 shrank: L → 2×M (~1 focused day), and needs NO new admission machinery.** The
  second time a ruling shrank an item rather than merely unblocking it (R13 was first).
  Residual work: namespace `EnvelopeKey`/`claimKey`, delete the field, thread the
  namespace through the derivation, re-topologize the golden.
- **The critique earned its keep three times:**
  - *Owner-string collision (major).* Namespacing `EnvelopeKey` does **not** close the
    aliasing class, because `cover` resolves by owner **string** cluster-wide
    (`cover.go:85-87,102-109`). The tenancy boundary rested on an unstated owner-string
    uniqueness assumption the design never named. Fixed with an explicit invariant —
    **one namespace per leaf owner** — detected during derivation, failing safe to
    *unbound*, plus an R26 alarm and a regression fixture.
  - *The stated fail-safe was false (minor, but real).* `lendingAllows` returns **true**
    when `LendingPolicy.To` is empty (`evaluate.go:909-910`), so an unbound/ownerless
    namespace could still borrow from a permissive sponsor. Deleting `Run.Spec.Owner`
    makes an empty borrower reachable, so pt2 gains a one-line empty-borrower guard.
    A hazard *introduced* by the change, caught before it was written.
  - *Three lease-stamping sites, not one (major).* `PaidByNamespace` must be stamped at
    `admission.PodLeaseWithRole` (the real mint), `binder.buildLease` (legacy), **and**
    `run_controller.go:434`'s *hypothetical* leases — never persisted, but they **do**
    feed `funding.Evaluate`, so a forecast would silently diverge from the derivation.
    This corrects the earlier "R2 pt3 and R13 touch the mint once" sequencing claim:
    all three touch the same surface, so coordinate them.
- **David's project-vs-team distinction is real in the four-class model.** A *project's*
  members run in the project's namespace and get **Owned** (senior, non-recallable). A
  *team's* members run in their own namespaces and reach the team pool through the family
  axis, getting **Shared** (junior, recallable). Same pool, different class — so a team
  pool can never confer Owned-class capacity on a member. That falls directly out of his
  accountability principle rather than being designed in.
- **Impersonation is inert for funding**, as predicted: `request.userInfo` appears in
  **zero** Go files, and the mint builds the Lease from `run.Namespace` alone. Acting as
  a project buys permission and identity, not payment.
- **Two defaults chosen, his to veto:** `LendingPolicy.To` stays owner-string patterns
  (subtree lending preserved) rather than namespace names; and binding conflicts fail
  safe to *unbound* + an R26 alarm rather than being rejected at admission (the
  alternative buys a client-backed webhook for an admin-error class).

### David's ruling: workload failure policy (2026-07-09)
**Per-role, default `Fail`; `Retry(n, backoff)` and `Ignore` opt-in.** David took the
standing recommendation (`R8-pod-failure-handling.md:56-59`, rationale at :32-48).
`Fail` matches real distributed training — a lost rank hangs the collective, and the
surviving members keep charging the budget until someone notices.

Implemented as **phase 9A-3** of the amended R9. Two consequences worth stating:
- The item is absorbed into R9; **the cost is not.** We build the failure edge
  ourselves, to R8's own spec — it is *not* inherited from a JobSet `failurePolicy`,
  because no JobSet will own our pods.
- R8's provision "design the handler so it is a no-op when a JobSet owns the pods"
  (`R8-pod-failure-handling.md:53-54,79`) is **deleted**.

Two owner decisions now remain on the whole board: **R7**'s tenant identity (gates R7
pt2 only) and **R19**'s license. Neither blocks the highest-severity work, which is
still the `HandleNodeFailure` bundle (R21/R22/R25 + the stale-node event).

### R9 re-scoped by a Fable design pass: JobSet as reference, not as controller (2026-07-09)
David, on reading my "Option A collides with CASCADE" note: *"Now I remember, losing
swap was the cost of moving to JobSet. I think we decided to use JobSet as reference
and implement our own primitive."* He asked whether there was new data Fable should
weigh. There was. Fable's amendment: `R9-jobset-amendment.md`.

**Verdict: borrow JobSet's design; do NOT borrow its controller.** Our controller
stays the sole creator of every pod. Everything R9 Option A promised (rendezvous,
stable identity, the failure edge, gang co-termination) is delivered on the emit path
we already own.

- **David's memory was right and the docs were wrong.** No "reference, own primitive"
  decision was ever recorded — `borrow-vs-build.md:164-166` recorded the *opposite*
  (borrow the real controller; fork it if lacking), and `pkg/lowering` encodes it
  (JOBSET-3 vendors `sigs.k8s.io/jobset`). But on **swap** he is right and the doc is
  wrong: `borrow-vs-build.md:118-120`'s "swap survives, the scheduler places the
  recreated pod onto a held spare" was written for a *lookup-steered* swap. What
  shipped (CASCADE-3) is a **pod-carried** swap — `emitSwapPod` stamps the consumed
  spare's payer triple and a required nodeAffinity onto one specific pod. A pod
  recreated from a shared, **immutable** Job template cannot carry a per-incident
  payer triple. The docs record the opposite of his memory; the code proves his
  memory right.
- **The decisive argument is one I had only half-found.** I flagged "a required
  node-affinity doesn't fit a uniform pod template." The deep version: **JobSet
  creates Jobs, not pods.** The *batch Job controller* creates the pods, so the VAP's
  `isController` check (`validating-admission-policy.yaml:50-51`) would have to trust
  an identity that stamps out pods from **every tenant's Job templates**. Any user
  with ordinary `create jobs.batch` writes a Job whose template carries `payer-*` +
  `lease-reason=Swap`, and the trusted controller creates the forged pod for them —
  **R5's cross-tenant charge, reopened with one extra hop.** Containing it needs two
  more VAPs, a `--use-service-account-credentials` assumption about
  kube-controller-manager, and abandoning the rule that *one* SA creates every jobtree
  pod. `spareLeaseProvenanceValid`/`promiseProvenanceValid` would drop from
  defense-in-depth to *the* boundary.
- **And it would force a permanent dual pod-creation path.** JobSet has no funded,
  held, workload-less spare: a sleep-forever holder pod is not a Job that completes,
  and would wreck `successPolicy{All}`. Spares, swap pods, and Promise gangs stay
  controller-emitted *forever*, beside JobSet-created active pods — two creators, two
  identity schemes, two security postures. That is not a migration window; it is the
  clean-break policy's clearest violation, and the rejected alternative was the one
  violating it.
- **Corrected accounting, against my own SIZING text.** R8's *item* is absorbed as
  phase 9A-3; its **L cost is not eliminated** — "its size is 0 under Option A"
  assumed JobSet's `failurePolicy` did the work, and now we build that edge to R8's
  own spec. What *is* cancelled outright is the **JOBSET track's XL**, plus the JobSet
  cluster prerequisite and the VAP rework a real borrow would have needed. Phases
  9A-0 (S) → 9A-1 (M) → 9A-2 (M) → 9A-3 (L) → 9A-4 (L) ≈ **4–6 focused days**.
- **`pkg/lowering` is deleted** in 9A-0 and JOBSET-3/4/5 retired; its documented
  mapping contract survives as the emit-path spec. 9A-0 also fixes R10's false
  rendezvous comments. One thing JobSet does genuinely better — a replacement pod
  keeps the failed member's completion index (**rank-stable replacement**) — we copy
  in 9A-1.
- **Sequencing constraint:** 9A-1 defines the pod ordinal, and **R2 pt3 must record it
  on the minted Lease**. Rank-stable replacement and scheduler-restart reconstruction
  want the same identity. Do 9A-1 first, or design them together.
- **One small ruling still owed:** 9A-3's default failure policy. Recommendation stands
  — per-role, default `Fail`, with `Retry(n)`/`Ignore` opt-in.

### David's rulings: R9 = JobSet, and no side-by-side, ever (2026-07-09)
- **R9 = Option A** — finish the JobSet lowering. It subsumes **R8** and retires the
  **JOBSET** track. Sizing consequence in `SIZING.md`: Option B would have cost
  R9-B + R8 + JOBSET ≈ 8 days; Option A ≈ 6–8 and closes all three.
  **Flagged (disagreement over silence, not a veto):** the R9 spec **predates
  CASCADE**. A JobSet `ReplicatedJob` has one pod template, but CASCADE's swap pods
  carry *per-pod* payer provenance and a **required** node-affinity onto one
  specific spare node — that does not fit a uniform template. Separately, R5's VAP
  gates `payer-*` / `lease-reason` / `schedulerName=jobtree` to the controller's
  ServiceAccount, and under Option A the *JobSet controller* creates the pods, so
  `userInfo` is its SA and the policy would reject them. Both are settled by a short
  design pass (**9A-0**) before any code; the likely answer is that swap remains a
  directly-emitted pod as a documented exception.
- **No side-by-side.** *"Never complicate the implementation to support side by side.
  If there is a breaking change, we'll schedule it, stop the jobs, and restart.
  Clean old, clean new."* This is a **project-wide policy**, not an R13 detail. It is
  cheap to hold because R15 established that `release.yaml` builds no images at all —
  there is no production install to migrate. Consequences:
  - **R13**: hard rename `Lease` → `GPULease`. No dual-read window, no conversion
    webhook, no migration Job. **L → M**, and it pairs with R14 in one pass.
  - **R4 pt2b**: the persistence fork the design doc left open now resolves — the
    settled summary lives in Budget `status`. A dedicated object existed only to keep
    old summaries readable across a change; recompute from the ledger instead.
  - **R2 pt3**: add the cohort label and pod-name annotation to minted Leases freely.
    Reconstruction need not cope with unlabelled legacy leases.
  - Generally: when a spec offers "dual-read window vs hard rename," take the hard
    rename and record it here.

### CI wall clock: measured, then fixed (2026-07-09)
`ci` = 74s (`make verify` 57s) — **not worth splitting**; four parallel jobs would
save ~30s of work and pay ~20s of runner setup each. `kind e2e` = 307s, of which
**210s was one step**: two Dockerfiles each doing `FROM golang`, `go mod download`,
and a full compile, for two binaries out of one module. Fixed with one builder stage
and two `--target`s, `kind-up` run **concurrently** with the build, a BuildKit
`type=gha` layer cache, and cached envtest binaries. **307s → 145s** for a
docs/config PR (full cache hit; `.dockerignore` excludes `docs`/`*.md`), **~240s**
for a Go source change (`COPY . .` invalidates the compile layer).

Two measurement traps worth remembering:
- GitHub Actions caches written on a **feature branch are not readable from `main`**
  (a branch reads only its own and its base's). The first post-merge run on `main` is
  therefore *cold*. I nearly concluded the layer cache did nothing.
- The two binaries are **not** cheap to build together for the reason I first
  assumed. Measured: manager 825 deps, scheduler 1444, sharing 774 — the scheduler
  pulls 670 packages (the kube-scheduler framework) the manager never sees. A cold
  `go build ./cmd/manager` is 88s and `./cmd/scheduler` is **91s even with the
  manager's cache warm**. The consolidation's win came from layer reuse and from
  overlapping `kind-up`, not from a shared dependency graph.

**Not adopting Bazel.** The remaining lever is the ~180s compile, and Bazel + a
remote action cache would attack it. But: one module, 21 packages, 108 Go files,
27.5k LOC, one language. Warm `go test -race ./...` is **5.3s** — affected-target
testing would be optimizing a five-second problem, and 13 of 21 packages transitively
import `pkg/funding`, so the "affected set" is nearly everything anyway. Worse,
affected-target testing is a **silent-pass generator**, which is exactly the class of
bug this repo just spent a session eliminating; and the cross-cutting gates (golden
oracle, antifake, `verify-generate`, helm assertions) are not file-affected. The
non-hermetic parts (envtest downloads an apiserver; kind needs Docker; controller-gen;
helm) are precisely what Bazel handles worst. The transferable idea is **phase 3, a
persistent build cache** — obtainable with `actions/cache` + buildkit cache-dance, no
new build system. Revisit Bazel if the repo gains a second module or language, or CI
passes ~10 minutes.

### Closing the three silent passes (2026-07-09)
R2 pt2 merged over a red CI check. Three separate mechanisms each turned an
absence of evidence into evidence of absence. All three are now fixed.

- **1. The gate and the check had drifted.** CI ran a list of steps; a developer
  ran `go test ./...`. Those are different lists, and only one of them included
  envtest. **`make verify` is now THE gate, and `.github/workflows/ci.yaml` runs
  exactly that one command.** A check cannot exist in CI without being runnable
  locally, and vice versa. The two inline CI shell blocks (helm assertions, krew
  manifest) moved to `hack/ci/*.sh` so both callers share them. `verify` also
  gained a **`golden-clean`** step: the golden oracle is a fixture, and a plain
  test run must never rewrite it — regenerating is a deliberate `UPDATE_GOLDEN=1`
  act whose diff is the review artifact.
- **2. envtest skipped silently and reported `ok`.** `controllers/kube`'s
  `TestMain` skips the whole integration suite when `KUBEBUILDER_ASSETS` is unset,
  so `go test ./...` prints `ok` for a package that ran nothing. `make envtest`
  now sets `JOBTREE_REQUIRE_ENVTEST=1`, and `TestMain` **exits non-zero rather
  than skipping** when that is set — turning the Makefile's own long-standing
  warning comment ("the substitution's failure is discarded and the suite would
  silently skip") from prose into an error. A banner also prints on skip, though
  only under `-v`: `go test` discards a passing package's output, which is exactly
  why the *structural* guard, not the banner, is the fix.
- **3. Branch rules never required the checks.** The `Main` ruleset enforced
  `deletion`, `non_fast_forward`, `pull_request`, and `copilot_code_review` — and
  **no `required_status_checks` rule at all**, so `gh pr merge` succeeded over a
  failing `build`. Requiring it was impossible as configured: the `CI` and `docs`
  workflows *both* had a job named `build`, producing two status contexts with the
  same name. Job names are now unique (`ci`, `docs`, `specs`, `kind e2e (real
  cluster)`), which is the prerequisite for a `required_status_checks` rule. Note
  `docs` and `specs` are path-filtered, so they must **not** be required — a
  required check that never runs blocks the PR forever.

**And the review harness itself: silence is not consent.** One adversarial lens
returned `summary: "test"` with a finding titled `"a"` and scenario `"b"` — pure
schema-filling, zero work — contributing no findings, which read as "clean". Three
skeptics then refuted the placeholder, so the panel looked unanimous. The
malleable-run regression it was assigned to find shipped. `.claude/workflows/
adversarial-review.js` now encodes the fix as a reusable harness:
- every lens must answer **every** assigned question and cite ≥N pieces of real
  evidence (`file`, `line`, verbatim `quote`); output is **validated, not trusted**;
- an independent agent **opens the files and checks each quote** — an agent can
  claim it read the code, but it cannot fake a quote that is not there;
- a lens that cannot produce valid output after retries **BLOCKS** the review; the
  verdict can never be `GREEN` without it;
- skeptics need a full **quorum**. A dead or degenerate skeptic is *not a vote*.
  Under-quorum findings are surfaced as `UNRESOLVED`, never silently dropped —
  otherwise two crashed agents bury a real bug. This direction of the failure was
  latent in the old harness: `confirmed = confirms.length >= 2` let a dead skeptic
  help *refute*.
An honest "I found nothing" report still passes — it just has to show its work.

### R2 part 2 — adopt-at-width IMPLEMENTED; the spec's "Running + Degraded" OVERRULED (2026-07-08)
The controller flipped a Run to `Running` on **any** open lease
(`openLeaseCountForRun(...) > 0`, at *two* sites: `Reconcile` and
`activateReservation`), and that count included Spare, Swap, Promise and
grow-cohort leases indiscriminately. So an N-wide run holding N−1 slices reported
healthy Running while N−1 containers charged budget forever — and a run whose only
open lease was a leftover **Spare** (held standby capacity that does no work) also
reported Running. Now both sites compare `activeGPUsForRun` (GPU-sum of open,
non-spare leases) against `expectedActiveGPUs` (`intentPodShape`'s
`gpusPerPod × width`, which CRD validation pins to `TotalGPUs`).

Judgment calls:
- **OVERRULED the spec's decision item 3** ("set Running but with a `Degraded`
  condition/message"). It contradicts the **same spec's invariant** — "a run is
  reported healthy-Running **iff** it holds open leases for its full active
  width" — and when a doc's decision prose fights its own invariant, the
  invariant wins, because the invariant is the operator-facing promise. Three
  independent judges (honesty / blast-radius / liveness lenses) reached this
  unanimously. Concretely: `RunStatus` has **no `Conditions` array** (R11 adds
  one), so "Degraded" could only be free text, while every control-path consumer
  keys off `Phase`. A Degraded-Running run is indistinguishable from a healthy one
  to `runGangComplete` (`:140`, which would then **complete a partial gang**), to
  `reconcileElasticRun` (`:185`), to the resolver, and to the CLI — reintroducing
  the exact width-blindness the fix removes.
- **Implemented instead: a partial gang never enters Running.** It stays `Pending`
  with `Status.Message` = `"assembling gang: k/N GPUs held"`, `Status.Width.Pending`
  = `"Assemble to N"` (the same channel `reconcileElasticRun` already uses to signal
  in-flight convergence), and a `GangIncomplete` warning event emitted only when a
  pod is actually created (no event storm). `Pending` hides nothing: `Width.Allocated`
  reports the GPUs actually held and `Status.Funding` reports what they charge, and
  both are already written on this path. Convergence is free — the adoption block
  re-runs on every watch event while the run is not Running, so the run adopts the
  instant the last lease lands, with nothing added to the Running path (which has
  *legitimate* partial-width states: the resolver's demote-not-kill shrink at
  `:1345`, and the elastic grow/shrink loop).
- **The partial branch returns early.** Falling through to admission would re-plan
  the run against a snapshot its own leases already occupy, report them as a
  deficit, and evict other runs to cover capacity it is already holding (the R28
  double-count the adoption block exists to prevent). At `activateReservation` the
  partial branch additionally **holds** the reservation — releasing it would hand
  the reserved capacity to another run mid-assembly.
- **`emitCohortPods` was count-based, not name-based** — a latent bug the top-up
  would have hit immediately. It counted surviving pods and created indices from
  that count, so a member lost from the *middle* of a cohort (`0,1,3` present)
  would rebuild index `3` (a duplicate) while index `2` never returned. Now keyed
  by pod name. Regression: `TestTopUpRecreatesTheMissingMember`.
- **The top-up must preserve gang provenance.** A Promise gang (R3) is
  pre-authorized and *skips* the plugin's funding gate — which is expected to
  refuse it until quota returns. Re-emitting one of its members as a plain `Start`
  pod would send it into that gate and wedge the run for good. `gangProvenance`
  recovers the reason + payer triple from a surviving sibling pod, or, if every pod
  is gone, from the open leases — the durable record. Regression:
  `TestTopUpPreservesPromiseProvenance`.
- **Full-width adoption now clears `CheckpointDeadline`** in `Reconcile`, matching
  what `activateReservation`'s adoption already did: capacity is whole again, so
  the node-failure grace no longer applies. The *partial* branch deliberately does
  **not** clear it — that deadline is what bounds how long a broken run may sit
  assembling before it fails.
- **Adversarial review caught a false-Running the width check itself could be
  spoofed by: elastic-GROW leases.** `activeGPUsForRun` sums every open non-spare
  lease, but grow leases are width added *on top of* the base gang, while
  `expectedActiveGPUs` is the base width. A malleable run that grew, then lost all
  its base nodes to failure, keeps its grow leases open — `allocated (4 grow) >=
  expected (4 base)` — so it adopts to `Running` holding **zero** base-gang GPUs,
  and (because full-width adoption clears it) loses the `CheckpointDeadline` that
  was supposed to bound its recovery. The pre-fix `open > 0` gate had the same
  hole, so three skeptics refuted the finding as "not introduced here" — correct,
  but shipping a width check a grow cohort can spoof would have missed the point of
  the fix. **Fixed:** adoption now uses `baseGangGPUsForRun`, which additionally
  skips `Spec.Reason == "Grow"`. A Lease records **no cohort**, so `Spec.Reason` is
  the only durable signal separating grow width from base width — the same missing
  lease identity that blocks pt3's restart reconstruction. Swap and Promise leases
  *do* count: each stands in for a real base-gang member. Regressions:
  `TestReconcileDoesNotAdoptOnGrowLeasesAlone`, `TestSwapLeasesCountTowardGangWidth`.
  `activeGPUsForRun` stays on the resolver path, which *should* see total width.
- **Adversarial review caught a real regression I introduced: malleable runs were
  being killed at their checkpoint grace.** Gating adoption on `expectedActiveGPUs`
  (= `TotalGPUs`) is right for a fixed-width gang and *wrong* for a malleable run,
  which may legitimately run anywhere in `[MinTotalGPUs, MaxTotalGPUs]` —
  quota-semantics' **demote-not-kill**. Scenario: a malleable run (Min 4, Total 8,
  `checkpoint: 10m`) loses a node with no spare; `HandleNodeFailure` parks it
  `Pending` with a `CheckpointDeadline` while it still holds 6 GPUs. Pre-fix, the
  `open > 0` gate adopted it straight back to `Running` and the elastic loop regrew
  it. Post-fix it sat in the partial branch (6 < 8) until the grace expired and
  `failRun` (`:161`) **terminally failed a run that was running fine at a valid
  width**. Fixed with `minRunnableGPUs(run)`: `Malleable.MinTotalGPUs` when set,
  else the full emitted width. Regressions: `TestMalleableRunAdoptsAtMinWidth`
  (verified to FAIL against the `TotalGPUs` gate) and
  `TestMalleableRunBelowMinDoesNotAdopt`. The partial-branch message now names the
  deficit against the *runnable* width (`"assembling gang: 2/3"`), not the emitted
  one.
- **PR #55's CI `build` job was RED when it merged, and that is a process failure worth naming.**
  Branch protection does not require the CI checks, so `gh pr merge` succeeded over a failing
  `build`. The failing step was **`make envtest`** (real API server) — which
  `go test ./...` **silently skips** unless `KUBEBUILDER_ASSETS` is set (`controllers/kube`'s
  `TestMain` skips the suite), so nothing in the verification sweep ever ran it. The full sweep must
  be: `go build`, `gofmt -l`, `go vet`, `go test -race ./...`, **`make envtest`**,
  `make verify-generate`, `make antifake`, `helm template`, golden + `git status` on the golden dir.
  - **Diagnosis: pre-existing envtest race, not an R2 pt2 regression.** The three failing scenarios
    (`train`, `finish`, `prep`) are exactly the three runs that received a
    `NodeFailureNoSpare` warning ("node node-a failed without spare coverage"): `HandleNodeFailure`
    closed their leases, active width fell to 0, and they fell back to re-admission
    ("scheduling 4 GPUs") forever. `"adopted"` and `"assembling"` appear **zero** times in the log,
    so the new partial-adoption branch never executed. Each of these is a 4-GPU run on one 4-GPU node
    with exactly **one** seeded lease, so adoption is a single lease-create event under both the old
    `open > 0` gate and the new width gate — there is no partial-width window for the new branch to
    sit in, and with zero open leases the old gate would have failed identically. The same tree passes
    `make envtest` locally (3×, including `GOMAXPROCS=2 -race`) and passed CI on `main`
    (run 28988169284, envtest green in 26s).
  - **Suspected mechanism (tracked, not reproduced):** `resetWorld` does `DeleteAllOf(&corev1.Node{})`
    (a delete event → the `anyDelete` predicate) and the next test re-creates `node-a` with *no
    status* — NotReady until a follow-up `Status().Update` → the `unusable` predicate. Both enqueue
    `node-a`. `NodeReconciler.Reconcile` re-reads the node and returns early when `nodeUsable`
    (`reconcilers.go:328`), so a stale enqueue is normally harmless — but under CI load one can be
    processed in the window where `node-a` exists-but-is-NotReady while the current test's leases
    already reference it, and `HandleNodeFailure` then closes leases for a node that is healthy *now*.
    That is a **real robustness bug**, not merely a fixture bug, and it belongs with R21/R22/R25,
    which all touch this same swap path.
- **Process note: one review lens failed silently and its "green" was worthless.**
  It returned `summary: "test"` with a finding titled `"a"` and scenario `"b"` —
  pure schema-filling — and three skeptics then earnestly refuted the placeholder.
  The panel looked unanimous because a member never showed up. The malleable-run
  regression above is exactly what that lens was assigned to find, and it surfaced
  only on a re-run with an explicit output contract plus a degenerate-output
  detector in the workflow. **Never read an agent panel's consensus without
  checking that every agent actually produced work.**
- **Known, tracked, NOT fixed here: `runGangComplete` is width-blind** (`:460-481`).
  It requires only that every *existing* active pod has Succeeded, with no
  comparison against expected width, so a run with fewer pod objects than its true
  width can be reported `Completed` at partial width. This is reachable
  **independently of adoption** — the resolver's `applyResolution` closes a locality
  group and removes its pods (`:1323-1335`) for any run, malleable or not. Option B
  does not create partial-width Running states, so it neither causes nor worsens
  this; but it is a real honesty bug of the same family and belongs with R8's
  failure semantics.
- **Not in scope: spare top-up.** `topUpActiveGang` refills Active members only.
  `emitSparePods`' count-based scan is self-consistent today (a swap-consumed spare
  decrements both `existing` and `count`), and spares do not gate the
  start-together promise. Tracked with R25.

### R4 pt2a — ledger-compaction primitive IMPLEMENTED (2026-07-08)
Design first (`R4-pt2-ledger-compaction.md`, merged #53), because the investigation
showed the spec's option (a) is subtler than an input filter. Then landed the
Evaluate-side primitive. Judgment calls:
- **Investigation reframed the problem.** `funding.Evaluate`'s accrual has no
  rolling `Now-Period` clamp (accrues from the first lease ever, bounded only by an
  envelope's explicit `Start`), so closed leases DO gate funding via `MaxGPUHours`
  caps (envelope/aggregate/lending) — "drop old closed leases" is not correct. And
  the golden oracle captures class *widths*, not GPU-hours (`goldenFunding`), so it
  would pass an accrual regression silently → pt2 needs its own accrual round-trip
  test, not just golden parity. Both written into the design doc.
- **Additive, bit-identical when off.** New `Input.SettlementHorizon` +
  `PriorAccrual`; a zero horizon disables compaction, so `Evaluate` is bit-identical
  to pre-pt2 (golden confirmed unchanged). Nothing turns it on yet (pt2b does), so
  zero production behavior change.
- **Provably-safe settlement condition, enforced by `settlementSafe`.** Compaction
  applies only when (1) the horizon is non-zero, (2) no budget has aggregate caps,
  and (3) the no-straddle invariant holds (every retained lease starts ≥ horizon).
  Under (3) the settled and retained epochs never co-occur in the fill, so the
  settled accrual is independent and can be seeded. Any violation → full replay
  (correct, uncompacted). A test poisons `PriorAccrual` and confirms a straddle
  forces the fallback.
- **pt2a seeds envelope-level accrual only; aggregate caps deferred.** Seeding
  `ConsumedGPUHours` + `HoursByClass` covers the envelope and lending caps (lending
  reads `HoursByClass[ClassBorrowed]`). Aggregate caps need per-aggregate,
  per-envelope attribution the Evaluation doesn't expose cleanly yet, so pt2a
  guards them onto the full-replay path and pt2b adds them — a perf limitation, not
  a correctness gap.
- **Per-run hour reporting semantic (adopted, flagged).** Settled leases' hours roll
  to the envelope, not the run; a run reports hours for its currently-retained
  leases ("current consumption"). Per-run history is report-only (not gating, not in
  the golden), so keeping it would force a per-run summary growing with run count.
- **The rail is the round-trip test, not the golden.** `TestLedgerCompactionRoundTrip`
  proves `Evaluate(full) == Evaluate(summary + retained)` on the gating outputs
  (ConsumedGPUHours, HoursByClass, WidthByClass, retained class) with a `MaxGPUHours`
  cap the settled hours exhaust — and a no-seed variant proves the drop is real and
  the summary load-bearing. Full suite green under `-race`; golden bit-identical;
  antifake + helm OK. `SettleAccrual` (the summary computation) ships too — pt2b's
  budget controller will call it.
- **Adversarial review caught a real bug before merge (the third time this has paid
  off, after R3 and R4 pt1): `settlementSafe` was missing a `horizon ≤ Now` guard.**
  "Settled" means `effectiveEnd ≤ horizon`, and `effectiveEnd` honors a lease's
  *scheduled* `Interval.End`, not just an observed `Status.Ended`. So a horizon past
  `Now` settles a lease that is still **live at `Now`** (`Now < End ≤ horizon`): the
  replay drops its width while `SettleAccrual` integrates it past the clock. The
  no-straddle loop inspects only *retained* leases, so it structurally cannot catch
  this. Reproduced at 16 → 24 GPU-hours with `Owned` width 4 → 0 — both gating, and
  silent under the golden (which captures widths, not hours), exactly the Finding-2
  weakness the design doc predicted. It falsified this file's own claim that "a
  wrongly-chosen horizon degrades to correct-but-uncompacted, never to a wrong
  funding decision." **Fix:** `settlementSafe` refuses `horizon > Now`, and
  `SettleAccrual` *returns nil* for such a horizon rather than clamping `Now` — a
  clamped summary would be under-integrated, and pt2b persists summaries, so it
  would silently under-charge forever once the clock passed the horizon. Regression
  test `TestLedgerCompactionRefusesFutureHorizon`. Latent-only today (no production
  path sets `Interval.End`, no caller sets a horizon), but pt2b's natural horizon
  choice — `min` open-lease start — is `+∞` with no open leases, which is precisely
  how it would have been reached.
- **Re-verified after the fix.** A 500k-case seeded property fuzz (random ledgers
  over windows / lending / sponsors / spare roles / multi-envelope / binding caps,
  with lease ends deliberately weighted past `Now`, and horizons drawn before / at /
  after `Now`) found **zero divergences** between `Evaluate(full)` and
  `Evaluate(summary + retained)` on ConsumedGPUHours, HoursByClass, WidthByClass,
  SpareWidth, and every open lease's class. ~4% of cases were genuinely
  `settlementSafe`, so the exact-arithmetic branch — not just the fallback — got
  real exercise. A second adversarial review (9 attack surfaces) filed no findings
  and produced the identity argument: with `horizon ≤ Now` plus no-straddle, the
  settled prefix `[earliest, horizon)` has an identical live set, ranking (which
  depends on Run creationTimestamp, not `Now`) and segmentation in both runs, so the
  seed equals the full replay's envelope state at the horizon bit-for-bit; the
  `MaxGPUHours` clamp is monotone and per-charge, so clamping early equals clamping
  late; and aggregate caps are the only cross-envelope coupling, which
  `settlementSafe` excludes outright.
- **Two pt2b caller contracts, recorded not fixed** (both unreachable in pt2a
  because nothing persists a summary — one `Evaluate` call always summarizes under
  the budgets it replays): pt2b must pick `H = min(Now, min open/pending start)`,
  and must add a `WindowStart` to `SettledAccrual` and invalidate on window
  movement, since a renewal *releases* pre-window hours in a live replay
  (`TestWindowReopenRefunds`) that a stale summary would keep charging. Both are now
  written into the design doc's pt2b section.

### R4 — SPLIT; part 1 (observability) IMPLEMENTED, caching reverted (2026-07-08)
R4's spec lists two "composable changes" (cached reads + snapshot; ledger
compaction). I split them (as R2 was split; David confirmed the split when asked),
then an adversarial review split pt1 again. Judgment calls:
- **Split rationale.** The caching/mutex half looked small and safe; the compaction
  half is a funding-engine-core change (new `Evaluate` accrual-summary input + a
  budget-controller settlement store) needing a bench + the golden oracle as its
  rail and touching the crown jewel. Landing them together would gate the cheap win
  behind the hard one.
- **The caching half was NOT actually safe — an adversarial review (workflow)
  caught a critical double-fund, and I reverted it to a deferred pt1b.** My first
  draft moved `loadWorld` before `m.mu` and backed `m.reader` with a controller-
  runtime informer cache. The review proved (two independent findings) that this
  breaks the cross-gang pending fold's **read-your-write** invariant: the fold
  retires another gang's phantom the instant its `minted[i]` flips, *assuming* the
  snapshot already shows that gang's real lease. (a) Taking the snapshot before the
  lock lets `minted[i]` flip between snapshot and fold; (b) an eventually-consistent
  cache lags the direct-client `Create`. Either way a gang can be funded against
  capacity another already holds — an overspend by the sole committer. `PostBind`'s
  GC leans on the same assumption. It also flagged a startup goroutine leak and that
  the sync-wait raced the cache `Start`. **So pt1b (safe caching) must first make
  the fold + PostBind staleness-robust (skip/fold by whether the real lease is
  actually in the snapshot, not by the `minted` flag) and then get a kind live-proof
  — reverted from pt1, tracked separately.** Lesson: the fold is load-bearing and
  read-your-write is a real precondition; do not swap the reader under it casually.
- **pt1 shipped = observability only.** `jobtree_plugin_decide_latency_seconds`
  (histogram, fundable/unfundable/error) and `jobtree_plugin_evaluate_input_leases`
  (gauge = ledger size fed to the replay) — measure-before-optimize, and the signal
  that will show pt1b's caching / pt2's compaction actually working. Reads stay on
  the direct, read-your-write client. Green under `-race`; antifake + helm OK.
- **Why compaction (pt2) genuinely needs a maintained accrual store (not just "drop
  old input leases").** Verified in `pkg/funding/evaluate.go` that the accrual
  replay has **no rolling `Now-Period` lower clamp**: `eventTimes` starts at the
  earliest lease event, and accrual is bounded only by an envelope's *explicit*
  `Start` (`windowActive`) — a no-window envelope (the common case) accrues from the
  first lease ever. So filtering ancient leases out of `Evaluate`'s input would
  change classification → not golden-safe. pt2 must carry a per-envelope rolling
  accrual the budget controller maintains. Deferred with this pinned.

### R5 + R6 — provenance trust anchor + mandatory scheduler (merged #TBD)
- **VAP, not a webhook.** The mandatory-scheduler + controller-only-fields rules
  ship as one `ValidatingAdmissionPolicy` (CEL, GA in the cluster's 1.36) rather
  than a webhook server — less code, no serving cert, no availability tail.
- **Two CEL rules, one binding.** (R6) a pod requesting `nvidia.com/gpu` must set
  `schedulerName: jobtree`; (R5) any pod setting a jobtree-owned field
  (`schedulerName: jobtree`, an `rq.davidlangworthy.io/*` annotation, or the role
  label) must be created by the controller SA (`request.userInfo.username`). The
  binding exempts the release namespace + operator-listed infra namespaces.
- **Default OFF (`podPolicy.enabled: false`).** Mirrors `scheduler.enabled: false`
  — a bare install must not suddenly gate every GPU pod in the cluster. Documented
  that *enabling it is what closes the opt-in-budget hole*. This is the one place I
  chose availability-of-the-default over closing-the-hole-by-default; flip the
  value (and it's in the operator guide / R18 break-glass) when ready.
- **failurePolicy `Fail`** (per the R6 recommendation), release namespace always
  exempt so the control plane comes up even under Fail.
- **Plugin defense-in-depth (the *tested* security win).** PreBind now refuses a
  swap whose carried provenance matches no real Spare lease the run held
  (`spareLeaseProvenanceValid`). This closes the sharpest exploit (mint against an
  arbitrary victim envelope) at the plugin level *even if the VAP is not enabled*,
  and it is unit-testable here; the VAP's CEL enforcement itself needs a kind
  cluster to verify (templating is checked; enforcement is a Sonnet live-verify
  follow-up).
- **OwnerReference on emitted pods** (`buildPod`): the Run is now the pod's
  controller owner — the provenance anchor R5 wants and the GC edge R12 needs
  (done once, here). Requires the Run UID (real path always has it; pure-engine
  Runs without a UID get none, backward compatible).
- **Tests:** `spareLeaseProvenanceValid` (accepts a matching spare, refuses a
  forged victim envelope, rejects an Active lease); `buildPod` owner reference (+
  no-UID fallback). Green under `-race`; full suite + antifake + helm template OK.

### R3 — refined scope (NOT yet implemented; recommendation logged)
On starting R3 I found the spec under-framed it. The "opportunistic / promised-
but-unfunded start" is **not** an incidental behavior to drop — it is a
**documented quota semantic** (`quota-semantics.md`, the source of truth) with
pure-engine tests that assert it: a shortfall run starts **Running, Unfunded**,
and is **re-funded when quota returns**
(`reservation_semantics_test.go:TestActivateReservationBudgetOnlyShortfallAdmitsOpportunistically`,
`quota_semantics_test.go` window-close/coast cases). So:
- **"Drop it" is OFF the table** — it would delete a documented semantic. My
  spec's earlier "drop it" fallback is withdrawn now that this is clear.
- **The fix is the `Promise` path** (spec's primary rec): the controller stops
  Materializing the opportunistic lease and instead emits intent pods marked
  `lease-reason=Promise` + payer provenance; the **plugin** mints the (naturally
  Unfunded) lease from that provenance, skipping the funding gate like a swap.
  The `Promise` marker is already forgery-protected — the R5/R6 VAP gates every
  `rq.davidlangworthy.io/*` annotation (incl. `lease-reason`) to the controller
  SA. Add a plugin owner cross-check (provenance owner == run owner) as
  defense-in-depth for when the VAP is off.
- **Why not rushed here:** this is a controller cutover of the opportunistic mint
  that must **migrate the pure-engine quota-semantics tests** to the intent-pod +
  simulated-plugin-mint pattern (as the PLUGIN-2 cutover did the others via
  `seedRunning`) and regenerate the affected golden scenarios. It touches the
  quota source-of-truth, so it deserves a careful, dedicated pass — not the tail
  of a long batch under a token budget. Left as the next unit of work with this
  design pinned. Nothing about it is blocked; it is scoped, not stuck.

### R3 — Promise path IMPLEMENTED (2026-07-08)
Executed the pinned design. The controller's opportunistic mint is gone; the
budget-only activation now emits a promised intent gang and the plugin is the
sole committer. Judgment calls made without interrupting, per standing
instruction:

1. **Promise branch keeps the run Pending; adoption flips Running.** The
   opportunistic branch (`activateReservation`) emits Promise pods and releases
   the reservation, but does **not** set `Phase=Running` and does **not** clear
   `CheckpointDeadline` — exact parity with the *funded* activation path, which
   also lets the plugin's leases land and the adoption block flip Running. Setting
   Running here would resurrect the old "Running with zero bindable pods" lie.
2. **New Reconcile guard `runHasPromisePods` short-circuits admission.** A
   promised run's cover is *expected* to keep failing until quota returns (that is
   why the promise fired), so re-entering `planPlacement`/`planReservation` would
   plan a spurious **second** reservation on every tick. The guard parks it Pending
   with a "promised start: scheduling N GPUs" message instead. It sits **after** the
   open-lease adoption block, so once the plugin mints the leases the run adopts to
   Running normally and never reaches the guard again.
3. **Per-pod leases replace the old per-group `Materialize` lease** — a pure
   mint-site move. The legacy Roles-less path emits one 1-GPU pod per requested GPU
   (`intentPodShape`), so a 4-GPU run now yields four per-pod Promise leases where
   the old `binder.Materialize` minted one 4-wide group lease. `funding.Evaluate`
   classes by envelope quota, not lease count, so the classification is identical
   (all Unfunded until quota returns); the golden oracle is unchanged.
4. **`promiseProvenanceValid` charge validation (defense-in-depth for VAP-off).**
   The plugin refuses to mint a Promise lease unless the **charged** envelope
   belongs to the run's own owner. First cut of this check compared only
   `seg.Owner == run.Spec.Owner` — an adversarial review (workflow, 2026-07-08)
   caught that this pins the wrong field: `funding.Evaluate` resolves every charge
   by `EnvelopeKey{PaidByBudget, PaidByEnvelope}` and takes the owner from the real
   Budget object, never from the lease's cosmetic `Spec.Owner`. So a pod that owns
   its own run could set `payer-owner` to itself (passing the naive check) while
   pointing `payer-budget/envelope` at a **victim's** budget, minting a gate-free
   cross-tenant charge. Fixed: resolve the named Budget, require `b.Spec.Owner ==
   run.Spec.Owner` **and** that it carries the named envelope — the exact invariant
   `opportunisticCoverPlan` upholds (it only attributes a promise to an envelope the
   run's owner owns). This matches the rigor of the swap's `spareLeaseProvenanceValid`
   (owner AND budget AND envelope); both flow through one PreBind carried-provenance
   branch that picks the check by marker (`Swap` vs `Promise`). With the R5/R6 VAP
   on, the payer annotations are already controller-only; this holds even with it off.
5. **Deleted the controller's orphaned `leaseSeqBase` copy.** It was dead after the
   mint-site move; the canonical copy stays in `pkg/admission/admission.go`.
6. **Test-migration scope was far smaller than feared.** Only one pure-engine test
   drives the controller's opportunistic mint
   (`TestActivateReservationBudgetOnlyShortfallAdmitsOpportunistically`); migrated
   it to the intent-pod + simulated-plugin-mint pattern with a new
   `seedPromiseLeases` helper (mirrors `seedSwapLease`). It now asserts the full
   promise lifecycle: controller mints nothing → 4 Promise pods carrying payer
   provenance → run stays Pending → re-reconcile does **not** re-reserve (guards the
   new guard) → plugin mints → adoption flips Running at 4 Unfunded → hog completes
   → **re-funded to 4 Owned with no new mint** (R14). Added `TestPromiseProvenanceValid`
   (plugin). **No golden scenario exercises opportunistic activation**, so the oracle
   needed no regeneration — verified it passes unchanged. Full suite green under
   `-race`; antifake + helm template OK.

This makes index.md's "sole committer" claim TRUE — R24 should drop its "false
until R3 lands" caveat when it does the doc-honesty pass.

> **Sequencing note (after R2 part 1):** I proceeded to **R5/R6** rather than
> immediately doing R2 part 2 (adopt-at-width). Rationale: part 1 already fixes the
> actual wedge *mechanism* (a lost member re-assembles and recovers on its own), so
> part 2's marginal value is honest-status + recovering *deleted* pod objects — and
> its re-emit is a no-op in the common case (part 1 recovers the still-existing
> pods). It also needs golden regen + a Degraded-status-clearing path. R5/R6 is a
> live, exploitable cross-tenant billing bypass (P1), so it is the better next unit
> of value. R2 parts 2 & 3 remain tracked follow-ups.


### Funding-model review (2026-07-08) — David's design challenge

David asked whether funding-class-on-the-GPU is the right design, whether the
ledger's allocs/frees can be trusted, and pointed out quota and capacity are
independently variable, reconciling only at scheduling instants. Ran a four-way
evidence sweep (funding engine, ledger lifecycle, quota↔capacity coupling, doc
claims); full analysis pinned in `../funding-model-review.md`. Outcomes:

- **Class is derived, never stored — confirmed clean.** Exhaustive grep: status
  class fields are write-only cache; no control path reads them back. The
  design's Decision 3 holds in the code.
- **Frozen-payer consequence documented** (re-funding is arithmetic within the
  minted envelope only; no re-point path exists). Accepted as a feature
  (predictable attribution); now written down instead of implicit.
- **New bug → [R25](R25-spare-node-lease-leak.md):** deleting a node hosting
  only a held spare leaks an open lease forever (`HandleNodeFailure` skips
  spares before node-match; caller swallows the error). Lands with R21/R22.
- **New structural item → [R26](R26-ledger-auditor.md):** runtime ledger
  auditor (open lease ↔ live pod on live node; jobtree pod ↔ open lease;
  `Orphaned` closure reason; violation metrics). Decision made without asking,
  per standing instruction: destructive repair is limited to closing leases
  (budget-safe direction); pod-without-lease only alarms.
- **R20 gains `GangUnplaceable`:** Permit currently labels pure physical
  failures "not fundable" (pack/cover errors collapsed to one string).
- **R24 expanded:** index.md budget-as-gate framing + "sole committer" claim
  (false until R3), dead `Fail` enum in leases.md, role/class conflation,
  and an explicit three-plane / quota-may-over-or-under-commit statement.
- **R3 spec note added:** opportunistic lease bakes `Slice.Nodes` from the pack
  plan while the pod gets only soft affinity → ledger/placement divergence; the
  Promise path fixes it by minting from the actual bind node — verify that.

### Leftover test fix (before P0) — `make e2e-image` scheduler image
Fixed the pickup-notes "Monday item #1": `e2e-image` now builds+loads the
scheduler image too. Done by a Sonnet agent; merged as #45. Not a remediation
spec, just the outstanding item.

### R1 — phantom lease clearing + gang GC (merged #TBD)
- **Retirement point:** a pod's phantom `pending[i]` is retired in **PreBind,
  right after the real lease `Create` succeeds** (`notifyMinted`), not at claim
  and not at PostBind. Rationale: the double-count window opens the instant the
  real lease exists in the API (another gang's `decide` would then see real +
  phantom), so it must close there. Retiring at claim would be too early (a failed
  mint must keep the guard); at PostBind too late (bind can lag).
- **GC point:** the whole `gangCommit` is dropped in **PostBind, only when every
  pod is fully minted** (`fullyMinted`). PostBind fires only after a *successful
  bind*, so a gang with a bind-failed / still-unbound member is deliberately kept
  alive — that surviving state is exactly what R2's recovery will read. This is
  why GC is in PostBind and not folded into `notifyMinted`.
- **Sweep backstop:** a `sweep(now)` drops any gang idle past `gangTTL = 15m`
  (> the 2m Permit timeout so an actively-forming gang is never reaped), driven by
  a ticker (`sweepInterval = 5m`) started in `New` off the scheduler context. This
  reclaims abandoned commits (member never bound, unfundable gang nobody retried,
  deleted run) that PostBind never reaches. TTLs are consts for now; make them
  config if a deployment needs it.
- **Extension point:** `postBind` was not enabled in the scheduler profile;
  added it to both `config/scheduler/jobtree-config.yaml` and the helm ConfigMap.
- **Tests:** double-count-after-mint (the headline, mirrors the live repro),
  guard-held-pre-mint (overspend still prevented before mint), PostBind-GC, and
  TTL-sweep. All green under `-race`; full suite + antifake + helm template green.

### R2 — gang recovery: SPLIT into three increments
R2's spec has four pieces; I split it so each lands small, green, and testable
rather than as one large controller+plugin change.

**R2 part 1 (this PR — pieces 1 + 4, plugin-side):**
- **Piece 1 — Permit counts committed siblings.** The gate now passes when
  `waiting + committedCount(gang) >= expected`, where `committedCount` = pods that
  already claimed a payer (`g.claimed`). This de-wedges the *common* failure: a
  member whose PreBind/bind fails transiently re-enters Permit alone; its bound
  siblings are gone from the waiting set, so the old `waiting >= expected` gate
  could never re-form and the gang looped to timeout forever at N-1 width.
  `committed` is 0 until a gang funds, so the *first* funding decision is
  unchanged (still needs the whole active set waiting).
- **Piece 4 — ABA lease-name nonce.** `buildPod` stamps `run-nonce` (a 12-char
  prefix of the Run UID); PreBind folds it into the lease name. A delete+resubmit
  of a same-named Run (new UID) now mints a fresh OPEN lease instead of colliding
  with the prior incarnation's closed lease and being swallowed by
  `IsAlreadyExists`. Same-incarnation retries keep the same nonce → still
  idempotent. No UID (pure-engine tests) → legacy name, backward compatible.
- Tests: `committedCount` accounting; `buildPod` nonce stamp (+ empty-UID
  fallback). Green under `-race`; full suite green.

**R2 part 2 (next PR — piece 3, controller-side):** adopt-at-correct-width —
the controller currently flips a run to Running on *any* open lease > 0
(`run_controller.go:197`), so a partial gang reports healthy while charging
budget. Will compare open leases to expected active width, mark Degraded + re-emit
missing active pods when short. Deferred here because it needs golden regen and
controller-test updates — kept as its own increment.

**R2 part 3 / R2b (documented follow-up — piece 2):** full scheduler-restart
reconstruction (rebuild gang commits from open leases on startup and delta-fund
un-minted survivors). Rarer than the transient-failure wedge that part 1 already
fixes, and the most complex sub-part (needs cohort-labelled leases + delta
re-funding). Left as a precise design note in the R2 spec for a later pass;
part 1's in-memory committed-count does NOT survive a process restart.

---

## R21 + R22 + R25 + #36 — node failure (2026-07-09)

Landed as one PR, because all four are bugs in `HandleNodeFailure` and its caller.
Five judgment calls worth explaining.

**1. The R21 spec's own premise was wrong, so the spec was amended.**
It called a node failed when "deleted, or NotReady past a grace window (kubelet
gone)". NotReady does not mean the kubelet is gone — it means the control plane
cannot hear it, and a partitioned kubelet keeps its containers running. Kubernetes
marks a node NotReady at 50s, then taint-eviction issues a *graceful* pod delete at
+300s that the unreachable kubelet never acts on. Implementing the spec literally (a
2-minute grace) swapped a rank onto a spare **before Kubernetes had begun to evict**,
while the original was almost certainly alive — reintroducing R21's own two-live-ranks
corruption through a different door.

`nodeFailed` now requires a **fencing assertion**: the Node object is gone, or it
carries `node.kubernetes.io/out-of-service`. Both make Pod GC force-delete the pods.
A NotReady node is logged and nothing else. The full argument, the upstream citations,
and the peer comparison are in the [R21 amendment](R21-cordon-not-failure.md#amendment-notready-is-not-a-failure-signal-fencing-is).

Cost: a dead on-prem node whose object is never deleted and never tainted stalls its
run instead of losing data. In cloud the CCM deletes the object automatically. For a
system whose worst outcome is two live copies of one rank, stalling is correct.

Two things fall out for free: `nodeFailed` takes no clock, so the engine clock and the
wall clock never meet; and it no longer trusts `LastTransitionTime`, which kubelets
write themselves and a compromised one could backdate to manufacture a failure.

**2. The adversarial review caught a high-severity defect I introduced.** Sixth
consecutive catch on this path. Declining the swap (because a *funded* run held the
spare's exact slots) closed the failed active lease but left the run's **own spare
lease open forever** — charging its budget, keeping healthy GPUs marked occupied.
Precisely the immortal-lease class R25 exists to kill. Nothing downstream closes a
terminal run's leases. A judge reproduced it: twenty reconciles over twenty hours,
still open, still deriving `Owned`.

Worse, `node_failure_test.go` **asserted** it — "the spare must not be consumed" —
which is how it would have shipped past a green suite. The assertion is inverted now,
and the test names why.

**3. `failRun`'s comment was an assertion nothing enforced.** It read "It never holds
leases at this point, so there is nothing to close." A run parked in checkpoint grace
still holds its other groups' leases on healthy nodes. Made true rather than deleted:
`failRun` sweeps them, and `HandleNodeFailure` sweeps any run it drove to Failed —
*after* the lease loop, so the outcome cannot depend on slice order.

**4. Run phase was last-writer-wins (pre-existing).** Each group wrote
`run.Status.Phase` directly, so the last group in `c.State.Leases` won: a run with one
group swapping and another dead without coverage reported whichever came last. A run
with a dead, uncovered rank could report `Running`. The review's judges found this,
reproduced it against *both* old and new code, and correctly refuted it as a
regression — it is not one. Fixed anyway (`runPhaseTracker` keeps the worst outcome);
a status field that lies is exactly what this backlog exists to remove.

**5. The test suite encoded the R21 bug as its own mechanism.** The envtest scenarios
and `swap-smoke.sh` both triggered the swap by `kubectl cordon`. A green suite was
proof the corruption worked. They now assert cordon *and* NotReady are no-ops, and
fence the node to trigger the swap.

Every fix is mutation-tested: reverting each one individually makes its test fail.

**#36 is now closed, not narrowed.** A replayed NotReady event is no longer a failure
at all, and a replayed delete is re-confirmed against the uncached `APIReader`. The
weekly workflow's 3× envtest probe keeps measuring it.

**Residual, owned by R26:** a node deleted while the manager is down produces no watch
event, so its leases are not closed. No predicate can fix that; it is the ledger
auditor's job. Noted in `SetupWithManager`.

**Follow-up not taken:** when a multi-group run fails, `emitSwapPod` may already have
emitted a swap pod for a group that swapped before a sibling group failed. The lease
ledger is correct (the sweep closes everything), but the orphaned pod is cleaned up
only when the Run object is deleted. Pod-lifecycle cleanup for terminal runs belongs
with R8's pod watch, not here.
## R16 + R17 — the deploy bundle (2026-07-09)

Both were confirmed live bugs, both mechanical, both in files disjoint from the
node-failure PR. Landed together.

**R16 — the ServiceMonitor matched nothing.** It selected Services on
`app.kubernetes.io/name`, and the `gpu-fleet.labels` helper never emits that key —
only `instance`, `managed-by`, and `component`. So it selected zero Services and no
jobtree metric was ever scraped. Nothing failed; a ServiceMonitor that matches
nothing looks exactly like a healthy one until you go looking for a metric.

Fixed by giving the Service the label. Two more found next to it:

- A ServiceMonitor placed in `monitoring.serviceMonitorNamespace` selects Services in
  **its own** namespace by default. Without a `namespaceSelector` it would find
  nothing even after the label fix. Added.
- The ServiceMonitor rendered unconditionally under `monitoring.enabled` (default
  `true`), so a bare `helm install` on a cluster without the Prometheus Operator
  failed outright: *no matches for kind "ServiceMonitor"*. Now gated on
  `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"`. Monitoring is not a
  hard dependency of a GPU scheduler.

Silence is not consent, so `NOTES.txt` says out loud when the ServiceMonitor was
skipped, rather than letting a quiet install read as a working one.

**R17 — production ran three concurrent engines.** `values-prod.yaml` set
`controller.replicas: 3`. There was **no `controller.leaderElect` key at all**, and
`deployment.yaml` never passed `--leader-elect`, so `cmd/manager`'s default of
`false` won. Three managers, each emitting intent pods and writing Run status against
its own view of the ledger — while the engine serializes admission on a single worker
on purpose (`specs/BudgetConservation.tla`). The RBAC already granted the
`coordination.k8s.io` leases and `main.go` already set `LeaderElectionID`; only the
flag and the value were missing.

Also: the scheduler plugin was off in **both** overlays. It is the sole committer of
GPU funding — with it off, nothing mints leases and no run is ever funded. On in both.

**The ratchet matters more than the fix.** Both bugs are the kind that a rendered
manifest would have caught instantly and a green test suite never will, so
`hack/ci/helm-assertions.sh` now fails the build when:

- the ServiceMonitor's selector and the Service's label disagree;
- a ServiceMonitor lands in another namespace with no `namespaceSelector`;
- the ServiceMonitor renders without the Prometheus Operator CRDs;
- **any** overlay sets `replicas > 1` without `--leader-elect=true`;
- the manager Deployment omits `--leader-elect` entirely (which is how R17 hid);
- the prod overlay ships without the committer.

Each assertion is mutation-tested. The first attempt at the R16 check **passed
against the bug**: it read `spec.selector` (which carries the same label key, to find
pods) instead of `metadata.labels`, so it compared the selector with itself and was
true no matter how broken the chart was. Caught only by trying to make it fail.

---

## Follow-up to R21: a fenced node was still capacity (2026-07-09)

Found by reading, while diagnosing an unrelated CI failure. Shipped in PR #72; fixed
before anyone ran it.

`nodeFailed` (has something asserted this machine is dead?) and `nodeUsable` (may we
place work here?) are deliberately different questions. But a fence answers both, and
`nodeUsable` only ever looked at `spec.unschedulable` and the Ready condition. So a
node tainted `node.kubernetes.io/out-of-service` whose last heartbeat still said
`Ready=True` stayed in the capacity pool.

The consequence is a funding error, not just a scheduling one. The engine would admit
a run against those GPUs and **charge its budget**, the `NoExecute` taint would stop
anything from actually running there, and the next node event would close whatever
leases had been minted. Nothing corrects it, because a fencing taint is not
transient — it outlives the failure it reports, until an operator removes it.

One line, one test, mutation-tested.
## R9 phase 9A-0 + R10 — retire the JobSet seam (2026-07-09)

No behavior change. `make verify` green; the only code motion is a deleted package
and comments, but the comments were the point: they described a system that does not
exist.

**`pkg/lowering` is deleted.** It had **zero Go importers** — a skeleton whose one
function returned `ErrNotImplemented`, guarded by a TODO blocked on `JOBSET-3` and
`JOBSET-4`, tasks that the R9 amendment retired. The package existed to hold a seam
for a substrate we decided not to use. A skeleton that returns `ErrNotImplemented` is
a claim, not a contract.

**The mapping contract it carried is preserved**, moved onto the path that actually
renders pods (`controllers/kube.buildPod`), and split honestly into two lists: what
is honoured today (schedulerName, never pinning `nodeName`, `restartPolicy=Never`,
gang labels, the `nvidia.com/gpu` request==limit on the GPU-target container), and
what is not (stable rendezvous identity → 9A-1; rendezvous env → 9A-2; the failure
edge → 9A-3). That is the JobSet shape kept as a reference contract, without the
dependency.

**R10 was larger than its one-line finding.** The audit named
`api/v1/run_types.go:67` — a comment claiming `buildPod` overlays "rendezvous env",
which it does not. Five more false claims sat next to it, all of them rendered into
the **CRD's OpenAPI descriptions**, which means `kubectl explain run.spec.roles`
was telling researchers that a role "lowers to a single JobSet ReplicatedJob" and
that its width is "the JobSet parallelism/completions". None of that is true, and it
is the sort of thing a user reads and believes.

Fixed all six, regenerated the CRDs, and stated the negative explicitly where it
matters: a role with `width > 1` **cannot form a process group yet**, and the comment
now says so and names the phase that will fix it.

`hack/e2e/versions.env` said installing JobSet was "Track B's prerequisite to add …
once a Run actually lowers to a JobSet". It never will. The note now says the
prerequisite is permanently retired, and why.

## R13 + R14 — the `GPULease` clean break, and validation that survives the webhook (2026-07-23)

Landed as one change because R14's CEL rules attach to the very Kind R13 renames;
splitting them would have meant writing the immutability rule twice.

### The name was taken, not asked

R13's spec carries a heading titled "Decision for David (flagged)" naming two things:
the kind name and the migration mode. The migration mode has since been decided —
clean break, project-wide (`SIZING.md`, 2026-07-09). That left the name, and the
autopilot's park list forbids making an owner decision.

I took **`GPULease`** and did not park it, for three reasons on the record:

1. The design layer already made the call: *"Recommended: `GPULease`"*, and
   `README.md` says the Fable specs' design decisions **are made** and should not be
   reopened without a stated reason. `SIZING.md` records the same recommendation.
2. The playbook's own suggested order lists `R13`+`R14` under the heading
   **"Suggested order (all unparked, no decisions)"** — it classifies this pair as
   containing no owner decision.
3. A kind name is not a policy question. Parking it would have blocked two of the
   milestone's remaining items on a naming preference with a written recommendation.

If David wants `FundingLease`, the change is another mechanical pass of the same
shape — and the clean-break rule already says breaking changes get scheduled rather
than accommodated.

### What "rename the kind" actually touched

73 Go files, and four things that would each have failed silently:

- **RBAC.** `apiGroups: ["rq.davidlangworthy.io"] resources: ["leases"]` is still a
  syntactically valid rule after the rename, and it grants **nothing**. The scheduler
  plugin is the sole committer; its PreBind mint would 403, no lease would ever be
  created, and nothing would ever be funded — with no error anywhere in the chart. The
  same string one line down, under `coordination.k8s.io`, is the leader-election
  resource and must NOT change. `hack/ci/helm-assertions.sh` now fails on a `leases`
  grant in our group, asserts the `gpuleases` grants exist, and asserts the
  coordination grant survives. Mutation-verified: put `leases` back and the rail reddens.
- **The validating webhook.** controller-runtime derives the served path from the GVK,
  so `NewWebhookManagedBy(&v1.GPULease{})` moved to
  `/validate-rq-davidlangworthy-io-v1-gpulease`. A stale `resources: ["leases"]` rule in
  the `ValidatingWebhookConfiguration` matches nothing: every GPULease would be admitted
  unvalidated, quietly. Marker, generated manifest and helm template all moved together.
- **The anti-fake CRD-fields allowlist** keys entries by `StructName.FieldName`, so
  `LeaseSpec.CompPath` had to become `GPULeaseSpec.CompPath` or the ratchet would have
  read as a *stale entry* — which that lint fails on, correctly.
- **The e2e smoke scripts** all say `kubectl get leases.rq.davidlangworthy.io`. Fully
  qualified, so they were never ambiguous — and after the rename they select a resource
  that does not exist, which `kubectl` reports as an error, not as zero results.

Two Go-identifier judgment calls, both toward the smaller diff:

- The **API types** are renamed (`GPULease`, `GPULeaseSpec/Status/Slice/Interval/List`)
  because controller-gen derives the Kind from the root type name and the rest read
  incoherently otherwise. **Function names keep the English word**: `CloseLease`,
  `SetLeaseConditions`, `LeaseConditionActive`. The collision was between two API kinds,
  not between two uses of a common noun, and renaming the sole closer would have churned
  `hack/antifake`'s anchor for nothing.
- The CLI subcommand stays **`kubectl runs leases`**. It is already namespaced under
  `runs`, so it is unambiguous, and `kubectl runs gpuleases` reads worse.

### R14: the webhook is not where an invariant should live

The spec asks for two things — markers mirroring `validate()`, and **CEL** for lease
immutability — with the rationale that the webhook is `failurePolicy=Fail`, so a webhook
outage either blocks every write or (flipped to `Ignore`) drops every check.

CEL now carries the cross-field rules the webhook alone used to hold: malleable
`min <= max`, `totalGPUs` within min/max and **on the step grid**, desired likewise;
Reservation's `intendedSlice` must set nodes or domain; both Reservation and GPULease
specs are immutable; and lease **closure is monotone** (`!oldSelf.closed || self.closed`),
which is `INV-CLOSED-MONOTONE` made unrepresentable rather than merely detected.

**I did not trim `validate()` down to cross-object checks**, which the spec suggests as
step 2. The invariant R14 states is "*field-level and immutability invariants hold at the
apiserver regardless of webhook availability*" — that is achieved by **adding** the CRD
rules, and deleting the Go copies would only remove validation from the pure-engine
tests, which never see an apiserver. The real cost of duplication is drift, and this
repo's own rule about that (three implementations of one obligation is three chances to
drift, per `CloseLease`) is answered by pinning them together: the new envtest asserts
the apiserver rejects exactly what `validate()` rejects, so a future edit to one without
the other reddens.

The verification is the part that matters. `controllers/kube/crd_validation_envtest_test.go`
**deletes the ValidatingWebhookConfiguration**, runs its assertions against a real
apiserver with no webhook at all, and restores it in `t.Cleanup` (loudly — a failed
restore would leave every later test in the package passing for the wrong reason). Both
CEL rules are mutation-verified: drop `self.spec == oldSelf.spec` and the payer-rewrite
test goes red; drop the step-grid rule and the off-grid Run is accepted.

R13's own invariant is asked of **discovery**, not of Go: `rq.davidlangworthy.io/v1`
serves `gpuleases` (kind `GPULease`, short name `gl`) and serves nothing called `leases`,
while `coordination.k8s.io/v1` still serves its own. A rename that missed the CRD would
compile perfectly and fail that test.

## R18 — the day-2 runbook, and why it had to be executable (2026-07-23)

R18's spec is unusual: the *procedures are the design*, already written by Fable. The
implementation is turning them into `docs/operator-guide/runbook.md` plus two scripts.
The interesting part is that writing them down surfaced errors in them.

**The spec named an object that has never existed.** Break-glass step 1 says
`kubectl delete validatingadmissionpolicybinding jobtree-gpu-mandatory`. The chart's
binding is `{{ .Release.Name }}-jobtree-gpu-guard` — release-prefixed, so *any*
hardcoded name is wrong for every install but one. That is exactly the failure mode a
runbook has: it is read once, under pressure, by someone who cannot debug it. Both
scripts therefore find every object by the chart's own label
(`app.kubernetes.io/instance`), and take `--release` only to disambiguate multiple
installs.

**Break-glass never touches the controller manager, and that is a decision.** The spec
says to leave it running so accounting stays honest; the reason is worth stating
plainly in the doc, because the instinct under pressure is to scale everything to zero.
The manager is what runs the Run finalizer, which is what CLOSES leases. Scale it down
and every open lease stays open, charging its budget and holding its GPUs, for as long
as the incident lasts.

**The `crd-writes` lever is survivable now in a way it was not before R14.** Flipping
the webhooks to `failurePolicy=Ignore` used to mean *no validation at all*. It now means
losing only the genuinely cross-object rules (an aggregate cap naming an envelope that
does not exist; a run that follows itself) while every field-level rule and lease
immutability keep holding in the apiserver. The runbook says so, because an operator
deciding whether to pull this lever needs to know what it costs.

**Two judgment calls in `uninstall.sh`, both toward not destroying the ledger:**

- **CRDs are kept by default.** `helm uninstall` already leaves `crds/`-installed CRDs
  alone; the script makes that the explicit default and needs `--delete-crds` to
  remove them. The lease set *is* the audit trail — "who ran where, when, and who
  paid" — and nothing else in the cluster records it.
- **It refuses to delete the CRDs while any lease is still open**, unless `--force`.
  An open lease at teardown means a Run did not drain, and deleting the ledger at that
  moment makes its last statement a fiction: work still running, still charging.
  Relatedly, the drain step tells the operator *not* to strip the finalizer by hand,
  and says what it costs, because that is the obvious wrong move when a delete hangs.

### The verification is a live proof, not a review

`make e2e-runbook` (kind, real chart, `podPolicy.enabled=true`) runs the documented
procedures verbatim and asserts the promised outcome: a GPU pod on the default
scheduler is denied; the first lever un-denies it; the committer scales to 0 and back;
a Run is written while the webhook endpoint is dead; Runs drain through the finalizer;
no lease is left open; the CRDs survive an uninstall that did not ask to delete them.

The `crd-writes` step is built to be falsifiable, and the first draft was not. It
originally broke `webhooks[0]`'s service and then wrote a Run — but `webhooks[0]` is
`vbudget`, which a Run never reaches, so the assertion would have passed against a
lever that did nothing. It now breaks **every** webhook's service, asserts the Run is
refused while `failurePolicy=Fail` (the situation the lever exists for), pulls the
lever, and asserts the same write succeeds.

It is a separate target from `make e2e` on purpose: it ends by uninstalling the chart
and deleting the CRDs, so it cannot share a cluster with the other smokes.

### What the live proof caught (and prose would not have)

Three real defects, none of which any amount of re-reading would have found. This is the
argument for making a runbook executable, so it is worth being specific.

**1. The most important lever appears not to work.** Deleting the R6
`ValidatingAdmissionPolicyBinding` is not instantaneous: the apiserver evaluates
admission policies from a cached snapshot that refreshes on its own schedule. Measured
on kind — the `delete` returns, the very next GPU-pod create is refused *quoting the
binding that was just deleted*, and a moment later the same create succeeds.

An operator in an incident pulls the lever, retries, sees the identical denial, and
concludes break-glass is broken. That is the worst thing this script could do. It now
dry-runs a probe pod in a loop and does not claim success until one is actually
admitted — and it distinguishes "still our policy" from "refused for some other reason",
because an unrelated refusal will not resolve itself and waiting on it would hide it.

**2. The lag runs both ways.** Re-enabling via `helm upgrade` also takes a few seconds
to bite, which failed the smoke's *precondition* on the second run: a GPU pod was still
being admitted right after install. Both directions are now documented, and the smoke
polls rather than asserting instantly — asserting instantly would have been flaky *and*
would have hidden a fact operators need.

**3. `helm upgrade` breaks admission until the manager restarts.** With
`webhook.generateCert=true` (the default) the chart mints a fresh self-signed CA on every
upgrade. The webhook configurations get the new CA at once; the running manager serves the
old certificate until its pod restarts, and in between **every** Run/Budget/GPULease write
fails `x509: certificate signed by unknown authority`. The chart's values file knows the
cert is regenerated; nothing said that the window exists or what it does. The runbook's
upgrade section now leads with the rollout-restart, points at cert-manager for long-lived
clusters, and notes that the `crd-writes` lever unblocks writes while the rollout happens.

The additive-upgrade check (verification item 3) is a real additive change, not a no-op
upgrade: it adds an optional property to a live CRD's schema **while an object of that
kind exists**, and asserts the object reads back unchanged.

## R7 pt2 — the owner is the namespace (2026-07-24)

**Owner decision:** David APPROVED and UNPARKED R7 pt2, overriding the park-list entry
(Codex-2 / #63). Implemented per `remediation/R7-tenancy-amendment.md` §4 exactly. Judgment
calls, recorded rather than interrupting:

**1. `nsForOwner` — one namespace per owner, threaded through every fixture.** The amendment's
model is one principal per namespace, owner derived from the namespace. The old test corpus put
several distinct budget *owners* in one namespace (`default`) and let the run name its own owner;
under derivation that namespace is now *conflicted* (two owners → fail-safe to unbound → every
lease Unfunded). So every migrated test maps an owner tier to a namespace with a single convention
— `owner` with `:`/`/` → `-` — applied in the package's `budgetOf`/`runOf` helpers so budgets,
runs and leases for one owner co-reside and `OwnerOf(ns)` round-trips. Where a lease is *borrowed*
(run in owner A's namespace, paid by sponsor B's envelope) the run/lease namespace is A's and only
`PaidByBudgetNamespace` points at B (a `forRunOwner` lease option). The golden and the envtest
package are the exception: their runs stay in `default` with the single owner's budget co-located
there, and only a genuine sponsor's budget moves to its own namespace — envtest namespaces are real
apiserver objects and are not worth creating for single-tenant scenarios.

**2. Pure borrowers must be admin-bound.** The empty-borrower guard means a run whose namespace has
no Budget derives owner `""` and can borrow from nothing — including `To:["*"]` sponsors. Two funding
tests relied on a bare borrower declaring its own owner; they now bind the borrower's namespace with
a nominal one-envelope Budget (amendment §5's pool-consumer pattern). This is the guard working as
designed, not a test workaround.

**3. `PaidByNamespace` field semantics left as pt1 shipped them.** The amendment §7 wants a *required*
`PaidByNamespace` plus a *loud rail* (empty payer-namespace surfaced as a defect). pt1 already shipped
the field as `PaidByBudgetNamespace`, *optional* with a silent legacy-empty fallback (Codex #1). This
PR stamps it at all three mint sites (already done by pt1) and adds the *binding-conflict* surfacing on
the `Evaluation` (`Conflicts()`), but does NOT flip the field to required or add the empty-namespace
loud rail — that would reclassify legacy-empty leases, an owner-facing change to an already-ratified
pt1 decision. Flagged as sub-question F7(4) in `DECISIONS-NEEDED.md` for the pre-merge review, not
parked (it does not block pt2).

**4. R26 alarms not wired in this PR.** The engine now exposes `Evaluation.Conflicts()` (multi-owner
and leaf-owner-collision namespaces) and the amendment asks R26 to alarm on those plus interior-owner
Runs and empty-payer leases. Wiring the auditor to consume this is a separate small change on R26's
spec; surfacing the data on the Evaluation (which the fail-safe already needs) is in scope here, the
auditor consumption is not. Flagged in F7.

**5. `promiseProvenanceValid` → namespace equality.** With `Run.Spec.Owner` gone the plugin's promise
provenance check drops the two owner-string agreements and gates on `b.Namespace == run.Namespace`
(forge-proof: the API server authenticates `metadata.namespace`). `seg.Owner` is now cosmetic; the
security test was rewritten to prove the cross-namespace charge is still refused and that the cosmetic
owner introduces no laundering path.

Gate: `make verify` + envtest + the eviction fuzzer; the three load-bearing engine fixes
(empty-borrower guard, leaf-owner injectivity, multi-owner fail-safe) were mutation-verified.

## 2026-07-24 — R7 pt2 milestone review PAUSED; interior-exemption finding parked (not fixed)

The owner-launched fail-closed adversarial panel (runId `wf_cd9db73e-4d3`, `git diff main...HEAD`
@ `f52d3cf`) was **interrupted by a usage limit at 13:30Z, mid-Review** — Attest and Judge never
ran. The prior turn intended to resume it after the quota reset, but workflow `resumeFromRunId` is
same-session-only and this turn is a new session, so cache-replay resume is unavailable; a fresh
full panel is a day's-quota run reserved for David (AGENTS.md, playbook).

**Judgment call: do NOT autonomously fix the one confirmed finding; park it.** The completed lenses
(+ the cached codex-sol finding + my own compiled reproduction on `f52d3cf`) converge on a CRITICAL
defect: the interior-tier exemption at `pkg/funding/evaluate.go:220` lets an owner that is both
directly leaf-bound in ≥2 runnable namespaces AND interior evade `ConflictLeafOwnerSpansNamespaces`,
minting a non-recallable cross-tenant Owned charge. I reproduced it: with an interior child,
`OwnerOf(alice)==OwnerOf(bob)=="org:ai"` and `conflicts==[]`; without it, the fail-safe fires.

Why parked rather than fixed: the exemption is **intentional** per `R7-tenancy-amendment.md`
§4/§5/C-4 ("interior tiers may span admin namespaces"), with the residual hazard deliberately booked
to the RBAC precondition + R26 alarm 3. Narrowing the exemption is a **tenancy design decision** that
risks breaking the legitimately-allowed multi-namespace-pool case (a reaper), and the panel's
reaper-veto lens never ran to vet any fix. Recorded as PARKED item **P5** in `DECISIONS-NEEDED.md`;
review archived (PAUSED) under `reviews/2026-07-24-r7-pt2-owner-from-namespace-f52d3cf/`. Two LOW
findings (diagnostic map nondeterminism at `evaluate.go:225`; `resolver.ownerOf` namespace fallback)
noted UNRESOLVED for the same decision. Working tree left untouched per the owner directive
("committed alongside the fixes once the panel lands" — it did not land). Not merged; no
`.autopilot-done` (milestone review mid-flight).
