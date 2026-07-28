## QUESTION 1

The split blocks direct Budget edits, but it does not establish the claimed property by itself. The design still needs the following complete classes of attack:

1. **Direct self-grant — document-catchable.** `P → P`, including an update that changes an existing Grant’s grantee to P.

   **Invariant:** `INV-NO-SELF-GRANT: grantor(G) ≠ grantee(G)` for every Grant. The grantor must be derived from the principal bound to `metadata.namespace`—never trusted from a writable field. This is the attack the current split overlooks despite promising that the grantee cannot self-assert allocation ([DESIGN-v3.md:64](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:64), [OWNER-RULINGS.md:551](/Users/david/mycode/jobtree/docs/project/decisions/p5-p8/OWNER-RULINGS.md:551)).

2. **Ancestor grant — document-catchable.** If ancestor A already reaches P, `P → A` closes a cycle and lets allocation flow back toward its source.

   **Invariant:** `INV-ACYCLIC`, strengthened as: no Grant may name its grantor or any ancestor of its grantor. The existing proposed invariant already requires acyclicity and at most one parent ([DESIGN-v2.md:110](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v2.md:110)).

3. **Cycle through a cooperating peer — document-catchable only while the cycle exists.** `P → Q`, `Q → P` is the two-node version; longer rings are identical.

   **Invariant:** `INV-ACYCLIC` over Grant edges, plus `INV-SINGLE-PARENT: every non-root principal has exactly one inbound authority edge`. Without single-parent, a peer can simply add an inbound Grant to P instead of forming an obvious cycle.

   A cooperating peer can avoid a simultaneous cycle by having P’s old grantor revoke P and then adopting P under Q. That can increase P’s allocation, but it is an authorized transfer from cooperating principals, not quota creation. No final document can distinguish that from a legitimate reorganization.

4. **Multiple or overlapping inbound Grants — document-catchable.** If caps from several Grants are added, or one per-flavour cap is independently applied to several envelopes, P multiplies its allocation without touching its Budget.

   **Invariants:**

   - exactly one inbound grantor per non-root principal;
   - for every `(principal, flavour, instant)`, the sum across all simultaneously active envelopes is bounded by that one inbound per-flavour cap;
   - duplicate Grants do not compose by `sum`, `max`, or fallback unless that composition is explicitly part of the parent’s single authorization.

   This is necessary because the design says Grants carry “per-flavour caps” but does not define how they compose with the Budget envelopes P “holds” ([DESIGN-v3.md:68](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:68)).

5. **Budget/Grant fallback or union — document-catchable.** The producer could interpret an absent, invalid, or smaller inbound Grant as “use P’s Budget,” or combine the Budget and Grant by union/addition. P can then enlarge itself by making the restrictive Grant disappear or fail validation.

   **Invariant:** for every non-root P, flavour f, and instant t:

   `effectiveAllocation(P,f,t) ≤ inboundGrantCap(P,f,t)`

   and no inbound valid Grant means zero effective allocation, never implicit root status or fallback to the Budget. Roots must be an explicit trusted set. The final document must retain enough Grant provenance to check this rather than publishing only an unexplained effective number.

6. **Spoofed endpoints and namespace-name reuse — partly document-catchable.** A Grant can name P as owner but another namespace, rely on a mutable grantor field, or bind to a namespace name that is later deleted and recreated.

   **Invariants:**

   - `grantor(G)` equals the unique principal bound to `G.metadata.namespace`’s immutable UID;
   - `(grantee owner, namespace name, namespace UID)` resolves to exactly one principal;
   - owner, principal, and namespace bindings are injective;
   - no authority decision uses namespace name without UID.

   Current Budgets expose writable `Owner` and `Parents` strings ([budget_types.go:28](/Users/david/mycode/jobtree/api/v1/budget_types.go:28)); accepting the equivalent from a Grant would preserve the self-assertion defect.

7. **Grant to a principal that does not yet exist — producer-only.** `INV-REFS-RESOLVE` catches it while unresolved, but the same dormant Grant becomes structurally valid when that principal or namespace is created later. A final snapshot cannot tell that the authorization was pre-positioned before the target identity existed.

   **Producer rule:** a Grant may activate only if its grantee existed in the prior accepted state, and it must pin that principal’s immutable ID and namespace UID. An unresolved Grant is rejected, not quarantined until it springs alive. This is a transition invariant, not a final-document invariant.

8. **One authenticated actor controls two namespaces — producer/authorization-only.** An actor controlling P and Q writes `Q → P`; the document sees a valid grant from Q. Distinct owner names and namespace UIDs satisfy every injectivity invariant. Proxy principals and two cooperating actors cross-granting into separately controlled descendants are the same attack.

   No document invariant can identify common human control. Preventing it requires an admission-time rule based on authenticated `UserInfo` and an actor-to-principal authority registry, for example: a non-root actor creating `Q → P` may not also control P or its grant-writing namespace. If such transfers are meant to be legal, the security claim must be narrowed to “no global quota is created”; it cannot promise that one human cannot enlarge the allocation they control. The design itself concedes that authenticated authorship is invisible in the compiled document ([DESIGN-v3.md:239](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:239)).

9. **Deleting a Grant — bounded increase is intentional; unbounded increase is document-catchable.** Deleting `P → child` returns usable headroom to P. That increases what P can run but not what P holds; `INV-SUBTREE-CONSERVE` must keep P’s funded subtree usage within P’s active Budget allocation ([DESIGN-v3.md:159](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:159)).

   Deletion becomes an escalation if the producer promotes the orphan to a root, falls back to its Budget, selects a less restrictive second parent, or retains the deleted edge through quarantine. Prevent that with:

   - roots are explicit and cannot arise from deletion;
   - every non-root has exactly one valid inbound Grant;
   - absent inbound authority means zero, not fallback;
   - a revoked edge is never supplied by last-good quarantine.

The current RBAC does not implement any of this yet: it contains no `grants` resource or lead Role, and the controller has full create/update/patch/delete authority over Budgets ([rbac.yaml:19](/Users/david/mycode/jobtree/deploy/helm/gpu-fleet/templates/rbac.yaml:19)). Therefore the claimed API-server enforcement remains a proposed policy, not an existing security boundary.

## QUESTION 2

Yes. Quarantine is attacker-inducible, and the freeze makes expiring allocation indefinitely renewable by failure.

A subtree can induce quarantine through cross-object failures that CRD validation cannot reject locally: a self/ancestor Grant, a cycle, a second parent, an unresolved reference, or a binding collision. A neighbour can name an already-bound principal and manufacture the same collision. The design expressly accepts two namespaces claiming one owner and says the producer quarantines both ([DESIGN-v3.md:97](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:97)). Current evaluation demonstrates the symmetric-poisoning shape: one owner spanning namespaces marks every touched namespace conflicted ([evaluate.go:206](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:206), [evaluate.go:253](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:253)).

A principal whose window is about to expire can therefore collide deliberately—directly if it controls another namespace, otherwise with a cooperating neighbour. `windowFrozenAt` then prevents its last-good envelope from expiring, with explicitly no destructive timeout ([DESIGN-v3.md:119](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:119), [DESIGN-v3.md:125](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:125)). Maintaining one invalid object makes the allocation effectively immortal. It also preserves stale ancestor grants, revocations, sharing, and lending policy; because last-good remains fundable, this preserves funded seniority rather than merely allowing work to coast Unfunded. Alarm escalation does not bound the security effect, and today the conflict alarm has no production consumer ([evaluate.go:173](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:173)).

The fix is causal, object-level rejection, not frozen time:

1. Validate each Grant transition against the prior accepted graph.
2. On a collision, retain the previously accepted binding and reject/quarantine only the causative new or changed Grant. Never quarantine both principals merely because the aggregate input is ambiguous.
3. Reject unresolved Grants rather than retaining dormant objects.
4. Continue accepting independently valid Budget renewals, ancestor reductions, and revocations while one Grant is quarantined.
5. Remove `windowFrozenAt`; evaluate accepted `[start,end)` windows against real time.

This does **not** reintroduce the neighbour-induced fuse: the neighbour can no longer prevent the victim’s valid renewal from being accepted. A window still expires when nobody supplies a valid renewal, which is the intended mandatory-window rule, not quarantine destroying allocation. If quarantine remains whole-subtree and blocks otherwise valid renewal, however, the requirements are irreconcilable: real-time expiry recreates the fuse, while frozen expiry gives any party able to induce quarantine an immortal allocation. No timeout repairs that contradiction.

## QUESTION 3

The structural claim is only conditionally true and is false as currently specified. An immutable record prevents later spec changes from rewriting that record only if:

- storage actually forbids update, patch, and delete;
- reports aggregate the recorded class and lender directly, without joining against the current graph;
- intervals are complete and exactly once;
- corrections cannot silently replace prior entries.

“Written once and never re-derived” is presently prose, not a storage invariant. Kubernetes CRDs are not intrinsically append-only, and the shipped controller role currently includes update, patch, and delete on every listed jobtree resource ([rbac.yaml:19](/Users/david/mycode/jobtree/deploy/helm/gpu-fleet/templates/rbac.yaml:19)). The current evaluator demonstrates what must disappear: it constructs one graph from current Budgets ([evaluate.go:322](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:322)), uses that graph and current lending policy for historical fills ([evaluate.go:700](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:700), [evaluate.go:791](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:791)), and only then attributes every historical segment by class and lender ([evaluate.go:972](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:972)). Append-only accrual closes Rulings 6 and 9 only after that entire current-state join is removed.

The additional costs are:

1. **Unbounded storage and write amplification.** Record count is proportional not merely to leases, but to class-affecting transitions across open leases. One global topology, concurrency, window, or lending change may split every open lease, producing `O(open leases)` writes. Periodic sealing at one-hour resolution with 10,000 open leases produces 240,000 records per day—87.6 million per year—before indexes and replicas. One-CRD-per-interval is therefore an etcd object-count problem even if individual objects remain small. Current compaction stores only per-envelope totals and class buckets ([evaluate.go:53](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:53)); the proposed record also retains payer, lender, lease, and interval dimensions.

2. **A crash creates either a gap or a duplicate unless interval sealing and the watermark are atomic.** An interval cannot be appended with its final end until the transition occurs. If the writer crashes first, the open tail is unrecorded. If it writes the interval and crashes before advancing its checkpoint, replay duplicates it; advancing the checkpoint first loses it.

   Required mechanism: a durable open-segment state plus an atomic “append sealed interval and advance watermark,” or deterministic record IDs such as `(lease UID, segment-start, authorizing-version)` with idempotent create. The coverage invariant is: for every lease, recorded half-open intervals are non-overlapping and contiguous from lease start through the durable watermark.

3. **Recovery restores a history requirement.** If snapshots or lease events changed while the writer was down, current state cannot reveal where the missing tail changed class. Kubernetes update metadata does not record when each spec field changed, as Ruling 6 already notes ([OWNER-RULINGS.md:246](/Users/david/mycode/jobtree/docs/project/decisions/p5-p8/OWNER-RULINGS.md:246)). Recovery therefore needs either retained ordered snapshots/events until every meter has crossed them, or a producer-owned write-ahead journal of every class-affecting transition. An expired watch resourceVersion otherwise makes the gap unrecoverable. Consequently, “no snapshot retention is required” ([DESIGN-v3.md:145](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:145)) is false unless equivalent history is retained in that journal.

4. **Class transitions must split a lease, not be fixed at mint or close.** An open lease can move Owned/Shared/Borrowed/Unfunded when another lease starts or ends, a window opens or expires, concurrency changes, a Grant or binding changes, sharing changes, or lending eligibility changes. Current replay deliberately refills at each lease/window boundary ([evaluate.go:404](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:404), [evaluate.go:570](/Users/david/mycode/jobtree/pkg/funding/evaluate.go:570)). The new writer must additionally process every prospective snapshot change and seal all affected leases at the snapshot’s `effectiveFrom`. A mint-time authorization record cannot capture these later transitions; the design itself acknowledges that every version spanning a lease may matter ([DESIGN-v3.md:154](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v3.md:154)).

5. **Ordering becomes correctness-critical.** Lease start/end, snapshot activation, window boundaries, and writer failover require one total order and half-open boundary convention. Two replicas observing the same events in different orders can assign different classes. A fenced single writer, monotonically increasing source sequence, and persisted effective time are required; wall-clock observation time is insufficient.

6. **Metering availability enters the control-plane trade-off.** If a class transition is acknowledged before its accrual boundary is durable, a crash loses history. If publication waits for the write, ledger latency or outage delays quota changes and lease processing. The design must choose fail-closed backpressure or a durable journal in front of asynchronous metering; “chargeback only” does not make lost attribution repairable.

7. **Aggregation and compaction become new mutable state.** Querying the entire log for every Run or Budget status is unbounded, so practical reporting needs materialized totals and indexes. Those summaries can drift, duplicate, or omit records and need rebuildable watermarks. Deleting raw intervals after roll-up violates literal append-only history; retaining them in archival/WORM storage preserves the claim but does not reduce total storage.

8. **Corrections weaken the headline claim.** With no correction mechanism, the user-noted metering error is permanent. With append-only compensating entries, the original record remains immutable but reported totals can still change later. Corrections therefore need a distinct record type, reason, author, reference to the erroneous record, and audit policy; they cannot masquerade as ordinary negative accrual.

The necessary structural rails are therefore: immutable deterministic record identity; create-only writer RBAC plus delete protection; contiguous, non-overlapping exactly-once coverage through a durable watermark; attribution pinned to the snapshot/version effective for each interval; and reports that consume recorded attribution without consulting current topology. Without all five, Rulings 6 and 9 remain aspirational.
