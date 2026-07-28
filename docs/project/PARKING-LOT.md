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
