# Parking lot — DESIGN-v5 build session

Things found while implementing `docs/project/decisions/quota-snapshot/DESIGN-v5.md` that are
**David's call**, not mine. Nothing here blocked the build; each one was resolved by following the
design and recording the consequence. Policy questions that are genuinely new go to
`DECISIONS-NEEDED.md`; this file is the session's own list, newest phase last.

## From Phase 1 (hours deletion) — also in DECISIONS-NEEDED

- **F-PERIOD: `--accounting-period` is now inert.** Its only consumer was the `width × period`
  admission lookahead that Ruling 10 deleted. It is still threaded from `cmd/manager` into
  `funding.Input` and read by nothing. Kept because DESIGN-v5 does not list it for removal and the
  Phase 6 meter may want a horizon. **Ask:** delete the flag, or give it a documented consumer?

- **F-ACCRUALPREFIX: `specs/AccrualPrefix.tla` models a partly unreachable scenario.** One of its
  four mutations is *reduced `MaxGPUHours`*, a field that no longer exists. §5b demotes P3 but does
  not list this spec for deletion, so it was kept and still checks — but its Go calibration specimen
  was built entirely on the deleted field and had to go. **Ask:** retarget the spec at the surviving
  conflict-erases-accrued-hours behaviour, or retire it with the rest of the integral?

## From Phase 2 (preconditions)

- **The `grants` resource does not exist yet.** Build item 1's RBAC is a *precondition*, so the
  grantor Role is shipped now while the `Grant` CRD lands with the producer (build item 3, Phase 5).
  Kubernetes RBAC is string-based, so the rule is legal and inert until the type exists. This is the
  intended reading of "precondition", but it does mean the chart names a resource the cluster will
  not recognise until Phase 5. **Ask:** confirm that ordering, or pull the `Grant` type forward.

- **Ruling 17's "not guaranteed" clause is unenforced by construction.** The design states plainly
  that nothing stops one human holding two principals from writing `B → A`. The RBAC shipped here
  does not change that and is not intended to. Recorded so a reader does not mistake the new Role
  for a containment guarantee it does not provide. **No action expected** — Ruling 9 calls this
  organisational.

## From Phase 3 (mandatory windows)

- **Nothing parked.** The migration question (what happens to existing open-ended envelopes)
  resolved itself under the clean-break rule: reject them, do not default an end. A defaulted end
  silently picks an expiry nobody chose, which is the opposite of the invariant's purpose.

## From Phase 5 (the producer)

- **`Grant` and `QuotaSnapshot` are new CRDs and need an install/upgrade story.** They ship in the
  chart's `crds/` like the others, so a fresh install is fine. There is no migration because there
  is nothing to migrate from. **Ask:** nothing blocking — noted so the CRD outage that Phase 4's
  lease fields ride on is understood to carry these too.

- **The producer is a Runnable on a 30s tick, not a watch.** DESIGN-v5 does not specify a trigger.
  A tick is simpler, cannot hot-loop, and the document is cheap to recompile; the cost is up to 30s
  of staleness between a Grant landing and the snapshot reflecting it. **Ask:** if that latency
  matters, wiring watches on Budget/Grant/Namespace is a small change — but it is a performance
  choice the design left open, so it is recorded rather than guessed at.

- **`GPULeaseSpec.SnapshotVersion` reads as "has a consumer" to the antifake lint, and does not.**
  The lint matches readers by FIELD NAME, and build item 3 introduced
  `QuotaSnapshotSpec.SnapshotVersion` and `GrantStatus.SnapshotVersion`, which the producer really
  does read. The shrink-only ratchet then forced removing the lease field's allowlist entry even
  though nothing yet performs the in-flight mint verification that justifies it. A note at the foot
  of `crd-fields-allowlist.txt` stands in its place. **Ask:** worth making the lint's reader scan
  struct-qualified? It will mis-clear any future same-named field the same way.

- **Revision-granular quarantine is implemented as far as REPORTING, not as re-compilation.**
  §4.3 says the accepted revision stays authoritative while an invalid candidate is credit-free.
  The producer records that faithfully on `Grant.status` (`acceptedRevision` is left pointing at the
  revision that compiled in, and the `Accepted` condition carries the refusal reason), and the
  refused candidate contributes nothing to the document — which is the credit-free half. What it
  does NOT do is re-compile the *prior* revision's content from a stored copy: it keeps whatever the
  last accepted compile produced. For every case reachable today those agree, because the accepted
  graph IS the last accepted compile. **Ask:** confirm that reading, or say if you want the producer
  to persist per-Grant accepted specs so a rejected update can be re-compiled from the accepted
  revision independently of compile order.
