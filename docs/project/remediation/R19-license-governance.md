# R19 — License + real governance (the project is not legally usable today)

**Priority:** P4 (blocks OSS usability) · **Design:** complete (Fable), **license choice for David** · **Next:** Sonnet adds the files

## Problem (evidence)

There is **no LICENSE anywhere** in the repository, so despite `index.md`
presenting jobtree as an open project with a roadmap and maintainers, it is **not
legally usable, modifiable, or redistributable** — default copyright reserves all
rights. `MAINTAINERS.md` (linked from the front page "to understand ownership") is
a fictional governance document with a dead `.example` security-disclosure contact.
The release pipeline never runs (R15), so nothing forces this to surface.

## Root cause

Project-shell hygiene was never done; the docs assert an open-project posture the
repo does not legally have.

## Design decision

1. **Add a LICENSE. Recommended: Apache-2.0** — the standard for the Kubernetes /
   CNCF ecosystem an out-of-tree scheduler lives in; it grants a patent license
   (relevant for a scheduling/allocation system) and is what contributors and
   downstreams expect. Alternative: MIT (simpler, no patent grant) if the owner
   prefers minimalism. This is the owner's decision — it is a legal/strategic call,
   not a technical one.
2. **Add license headers** to source files if Apache-2.0 (common convention;
   optional but expected in this ecosystem) — mechanical, scriptable.
3. **Make governance real:** either replace `MAINTAINERS.md` with a truthful,
   minimal owners list + a working security-disclosure channel (a real email or a
   `SECURITY.md` with GitHub private vulnerability reporting), or drop the
   front-page "to understand ownership" link until governance exists. A dead
   security contact is worse than none — it implies a disclosure path that fails.

### Decision for David (flagged) — ✅ DECIDED 2026-07-09

License choice (**Apache-2.0 recommended** vs MIT vs other), and whether governance
is real-now (truthful MAINTAINERS + SECURITY.md) or the claims are trimmed until it
is. Both are quick once decided.

> **David ruled: no licence yet.** *"I'm not ready to give this away yet, but I want
> to be able to talk about it."* The repository stays public, and an explicit
> `LICENSE` states that **all rights are reserved and no licence is granted** — an
> absent LICENSE file is ambiguous (did the author forget, or intend to reserve?);
> an explicit one is not. He asked for MIT "or some non-commercial licence", but
> those are opposites — MIT *permits* commercial use — and neither matches "not ready
> to give this away." Governance is made **real and minimal** rather than trimmed: a
> truthful one-person `MAINTAINERS.md`, and a `SECURITY.md` routed through GitHub's
> **private vulnerability reporting** so no email address is ever published. He
> asked explicitly that no email of his appear in the repository; none does.
> Apache-2.0 or another licence may be granted later; that decision is deferred, not
> foreclosed.

## Invariant

The repository's legal status matches how it presents itself: an open project has a
license and a working disclosure channel; if it is not yet open, the docs do not
claim it is.

## Implementation spec (Sonnet)

- Add `LICENSE` (chosen text) at repo root.
- If Apache-2.0 + headers: a script to prepend the header to `*.go` (skip generated
  `zz_generated*`).
- `SECURITY.md` with a real reporting path; fix/replace `MAINTAINERS.md`; fix the
  `docs/index.md` link if governance is trimmed.
- Add license metadata to `Chart.yaml` and Go module docs as appropriate.

## Verification spec (Sonnet)

1. `LICENSE` exists at root; `go` / helm metadata reference it.
2. No dead `.example` contacts remain; the security channel resolves to something
   real (or the link is removed).
3. A license-header linter (if adopted) passes on non-generated sources.

## Interactions

- Independent; do it early and cheaply. **R15** (release pipeline) should also fail
  if LICENSE is missing, so the two reinforce each other.
