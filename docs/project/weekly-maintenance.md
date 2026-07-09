<!-- Internal process doc (docs/project/ is excluded from the built site). -->

# The Tuesday fix-up

> *"I don't want to get spammed with emails saying there are problems. I really
> just want them all fixed every Tuesday or something."*

One review sitting per week. One notification. Everything else is either done
automatically, or held with a reason.

## Where it runs, and why not in Claude

Claude's scheduled routines are **session-only**: in memory, gone when the session
exits, auto-expiring after seven days, and firing only while the REPL is idle. A
routine that silently stops running is exactly the failure this repository spent a
week eliminating. So the durable part lives in **GitHub Actions**
(`.github/workflows/weekly-maintenance.yaml`), which runs on GitHub's
infrastructure whether or not anyone's laptop is on.

What a machine can decide, the workflow decides. What needs judgment, it hands to
a human — or to a Claude session, with the evidence attached.

## The rules it follows

These are not arbitrary. Each was paid for; see
[`working-agreement.md`](working-agreement.md).

**1. Fail closed.** The gate is `make verify` — *the same single definition* CI
uses, not a bespoke list that could drift from what a developer runs. If it fails,
**nothing is merged** and the run goes red.

**2. Never auto-merge a Go module bump.** A `gomod` bump can change funding
behaviour, and the golden oracle captures class **widths and lenders, not the
wall-clock-derived GPU-hour floats**. A green suite therefore does not prove the
accrual is unchanged — that is the same weakness that let a live lease be settled
past the clock in R4 pt2a. Go bumps get a `needs-human` label, and an adversarial
review if they touch `pkg/funding`'s dependency graph.

GitHub Actions and base-image bumps *are* auto-merged, but only when **every
required check has concluded `SUCCESS`**. The workflow verifies that itself rather
than using `gh pr merge --auto`, which depends on a repository setting someone
could switch off and which would merge instantly if the required-checks rule were
ever removed.

**3. Always report, even when green.** A week with no comment means the workflow
did not run, and that must be visible. **Silence is not consent.** The report says
exactly what was merged and what was held, and why — no summaries that could be
mistaken for a clean bill of health.

**4. Distinguish a flake from a regression.** `envtest` is run three extra times
and the failures are counted. There is a known intermittent failure — a stale
node-failure reconcile closing a healthy node's leases (task #36). Counting means a
real regression cannot be waved away as "that flake again," and a worsening flake
is visible as a number that climbs.

**5. Never bump Go automatically.** The toolchain is pinned in **three** places
that must move together — `Dockerfile`'s `FROM golang:X.Y.Z` and the `go-version:`
in `ci.yaml`, `e2e.yaml`, and `release.yaml` — because the e2e fast path compiles
on the runner and wraps the binaries in the production base image, and identical
binaries need an identical toolchain. `hack/ci/assert-build-flags.sh` enforces it.
`golang` is excluded in `.github/dependabot.yml` for exactly this reason. Bump it
by hand, all three at once.

## What lands in your inbox

**One comment per week**, on a single long-lived issue titled *Weekly maintenance
log*. Pin it. That is the whole notification budget.

For this to be true, set your [notification
settings](https://github.com/settings/notifications) so Dependabot alerts arrive as
a **weekly digest** (or web-only) rather than one email per advisory. The workflow
cannot do that for you — it is a per-account setting, not a per-repository one.

## What still needs a person

The workflow does the mechanical part. These need judgment:

- **A `needs-human` PR.** Read the diff. If it touches the funding path
  (`pkg/funding`, `cmd/scheduler/plugin`, or `run_controller.go`'s adoption / mint /
  swap), run the adversarial-review harness at
  `.claude/workflows/adversarial-review.js` before merging. It has found a real,
  merge-blocking defect on four consecutive changes to that path.
- **A gate failure.** `make verify` failing on `main` is not a dependency problem.
  Something regressed, or a fixture drifted. The golden oracle is deliberately
  strict about this.
- **A climbing flake count.** Two of three is the known bug. Three of three,
  repeatedly, is a new one.
- **A borrow decision whose premises moved.** Merging something that changes an
  invariant an earlier decision rested on is the trigger to re-read
  [`borrow-vs-build.md`](borrow-vs-build.md) §11. Not the calendar. That is how the
  JobSet decision went stale without anyone noticing.

## Turning it on

The workflow is scheduled for **Tuesdays at 13:17 UTC** (off the hour on purpose —
everyone who asks for "Tuesday" gets `0 13`, and they all hit the API at once). It
also accepts `workflow_dispatch`, so you can run it now and see what it says.

Prerequisites, both already true as of 2026-07-09:

- The branch ruleset requires the `ci` and `kind e2e (real cluster)` checks. Without
  that, a green-looking merge would mean nothing.
- Private vulnerability reporting is enabled, so a human reporting a real
  vulnerability reaches you immediately. **That one is deliberately not batched.**
  Rare and urgent is the opposite of routine and noisy.
