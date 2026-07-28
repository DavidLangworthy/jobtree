
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
