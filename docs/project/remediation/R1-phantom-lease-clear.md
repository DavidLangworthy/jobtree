# R1 — Clear phantom `pending` leases and GC committed gangs

**Priority:** P0 · **Design:** complete (Fable) · **Next:** Opus implements, Sonnet verifies
**Depends on:** R2 (shares the `PostBind` hook + stale-gang sweep). Land immediately after R2.

## Problem (evidence)

When a gang is decided fundable, `gangManager.decide` builds one placeholder
"pending" lease per payer and stores them on the `gangCommit`
(`cmd/scheduler/plugin/gang.go:136`). Every *other* gang's `decide` folds those
pending leases into its funding world so two gangs cannot both fund the same free
capacity (`gang.go:117-122`). That fold is correct **only until the real leases
exist**; the code comment says so (`gang.go:48-51`).

Nothing ever clears `pending`. `forget()` — the sole deletion site — is a no-op
once any pod has claimed a payer (`gang.go:184-191`), and the plugin has no
`PostBind`. So once a gang mints, every later `decide` counts that run's leases
**twice**: the real leases (read from the API by `loadWorld`) *plus* the stale
phantoms. Funding claims key on `{envelope, runKey}` and **sum** their widths
(`pkg/funding/evaluate.go:367,398`), so the phantom and the real lease collapse
into one claim at 2× width. The phantoms outlive even run completion, and
`m.gangs` grows without bound.

**Effect:** a long-running scheduler monotonically under-admits (envelopes appear
exhausted at ~half real usage, then worse) and leaks memory. Live-reproduced in
the audit: envelope concurrency 8, one 4-GPU gang minted → a second 4-GPU gang is
falsely rejected "insufficient capacity"; deleting the stale in-memory gang entry
makes it fundable.

## Root cause

`pending` is a write-once guard with no lifecycle. The gang commit has no "the
real lease now exists, retire the phantom" transition and no terminal cleanup.

## Design decision

Drive phantom lifetime off **in-process mint bookkeeping**, not off API List
timing (the plugin's client is direct/non-cached, but List-after-Create
consistency across the apiserver watch cache is not a contract we should depend
on for a funding-correctness invariant).

1. **Per-pod retirement.** Make `pending` index-aligned with `payers`. When a pod
   claims payer index `i` and its real lease is created at `PreBind`, mark that
   pending entry retired so it is no longer folded. Concretely: replace
   `pending []v1.Lease` with a parallel `minted []bool` (or drop the entry). The
   fold in `decide` (`gang.go:117-122`) skips retired entries.
2. **Gang GC when fully minted.** Add a `PostBind` extension point. On PostBind
   for a pod, if `g.claimed == len(g.payers)` and all pending are retired, delete
   the gang from `m.gangs`. From then on the API's real leases are the sole truth.
3. **Stale-gang sweep** (shared with R2). A gang that never finishes minting (a
   member failed — see R2) must not leak its phantoms forever. Add a periodic
   sweep: drop any `gangCommit` whose run no longer exists, whose leases are all
   closed, or that has been idle past a TTL (default 15m, > `permitTimeout`).

**Why not "just clear on next `decide`":** other gangs' `decide` calls are not
guaranteed to run, so a quiescent-then-busy cluster would still double-count.
**Why not "count real leases instead of pending":** that reintroduces the
decide→mint window the pending fold exists to close. Keep the guard; give it an
end.

## Invariant

For any envelope, at any instant, `funding.Evaluate` counts each committed GPU
exactly once: either as a real lease (post-mint) or as a phantom (post-decide,
pre-mint) — never both, never neither, and never after the run is gone.

## Implementation spec (Opus)

- `cmd/scheduler/plugin/gang.go`
  - `gangCommit`: make phantom retirement explicit (index-aligned `minted []bool`
    or shrink `pending`). Update the fold at `:117-122` to skip retired.
  - In `claimPayer` (or a new `notifyMinted(pod)` called from PreBind after a
    successful `client.Create`), retire the pod's pending entry under the mutex.
  - Add `func (m *gangManager) postBind(pod)` that GCs the gang when
    `claimed == len(payers)`.
  - Add `func (m *gangManager) sweep(now)` implementing the TTL/orphan sweep; add
    a `lastTouched time.Time` to `gangCommit`.
- `cmd/scheduler/plugin/plugin.go`
  - Implement `fwk.PostBindPlugin`; add the `var _ fwk.PostBindPlugin` assertion;
    enable PostBind in the profile/extension lists.
  - Call `m.sweep` from a goroutine started in `New` (ticker; stop on ctx). Keep
    it cheap — it only walks `m.gangs`.
- Ordering note: retire the phantom **after** the `client.Create` succeeds in
  PreBind, not before, so a failed mint keeps the guard.

## Verification spec (Sonnet)

1. **Unit (deterministic, no cluster).** With a fake `client.Reader`: decide a
   4-GPU gang against envelope concurrency 8; simulate all pods minting (claim +
   PreBind Create into the fake); assert a *second* 4-GPU gang for a different run
   now decides fundable. Before R1 this fails (rejected); after, it passes.
2. **Unit — no premature clear.** Decide gang A; claim only 1 of 2 payers; assert
   gang B still sees A's remaining phantom (no overspend).
3. **Unit — GC.** After all of A's pods PostBind, assert `m.gangs` no longer holds
   A. After the run's leases close + TTL, assert `sweep` drops any orphan.
4. **Race.** `go test -race` on the plugin package under concurrent
   decide/claim/postBind for several gangs.
5. **Live (optional, extends `fullstack-smoke.sh`).** Run ≥3 sequential gangs
   through one long-lived scheduler process against a tight envelope; assert the
   3rd still admits (pre-R1 it would be spuriously rejected).

## Interactions

- **R2** introduces the same `PostBind` + sweep; implement them once, shared.
- **R4** must not remove the pending fold when it adds caching — R1 ends the
  phantom's life; R4 must preserve the decide→mint guard.
