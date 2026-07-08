# R23 — Workload observability: logs, pods, and an artifacts story

**Priority:** P5 · **Design:** complete (Fable) · **Next:** Opus/Sonnet implements, Sonnet verifies

## Problem (evidence)

There is no way for a researcher to see their workload's output or reach its
results through jobtree. The CLI (`cmd/kubectl-runs/cmd/`) has submit/watch/explain/
eta but **no `logs`, no `pods`, and no artifacts guidance**. Once a run is Running
the researcher must drop to raw `kubectl` and reverse-engineer the pod naming
(`<run>-g<grp>-<role>-<node>-<seq>`), and there is no documented convention for
getting model checkpoints/outputs out. This undercuts index.md's "simple mental
model… without bespoke scripts."

## Root cause

The CLI was built around scheduling/accounting visibility, not workload
operation; the run→pods→logs/artifacts path was never surfaced.

## Design decision

Close the loop from Run to workload with three additions:

1. **`kubectl runs pods <run>`** — list the run's pods (map via `LabelRunName`),
   showing role, group, node, phase, and lease/funding class. This is the missing
   index from the run to its live containers.
2. **`kubectl runs logs <run> [-r role] [--rank N] [-f]`** — resolve the run's pods
   and stream logs (wrapping `kubectl logs`), defaulting to rank 0 / the first
   active pod, with `-f` follow and `--previous` for a crashed container (pairs with
   R8 — a failed run's last logs are exactly what the researcher needs).
3. **Artifacts convention (documented, minimal).** Define and document a convention
   rather than build storage: a role template mounts an output volume (PVC / object-
   store CSI) at a documented path (e.g. `/artifacts`), and `runs explain`/`pods`
   surfaces where it is. Optionally a `runs artifacts <run>` that prints the
   resolved output location(s) from the template. No new storage system — just a
   named convention + surfacing.

## Invariant

A researcher can go from a Run name to its pods, to a specific rank's (or crashed
rank's) logs, and to a documented location for its outputs, using only the jobtree
CLI — no hand-built `kubectl` label queries.

## Implementation spec (Opus/Sonnet)

- `cmd/kubectl-runs/cmd/pods.go` (new): list by `LabelRunName`, join lease/funding.
- `cmd/kubectl-runs/cmd/logs.go` (new): resolve pods, select by role/rank, shell
  out to / reuse client-go log streaming; support `-f`/`--previous`.
- `docs/user-guide/researcher-guide.md`: document the artifacts convention + the new
  commands. Optional `artifacts.go` that reads the role template's volume mounts.
- Reuse the CLI's existing client + `--local` gating conventions.

## Verification spec (Sonnet)

1. **`pods`.** Against a Running run on kind, assert it lists the right pods with
   role/group/node/phase.
2. **`logs`.** Assert it streams rank 0 by default, `--rank N` selects, `-f`
   follows, and `--previous` shows a crashed container's last output (with R8).
3. **Artifacts.** With a template mounting `/artifacts`, assert `explain`/`artifacts`
   surfaces the location.

## Interactions

- **R8** — `--previous` logs of a failed rank are the primary failure-triage tool.
- **R20** — `explain` aggregation of plugin Events; coordinate CLI changes.
