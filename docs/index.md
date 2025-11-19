# Jobtree

Kubernetes-native gang scheduling for **job forests** that need to honor **time-scoped organizational budgets**. Jobtree coordinates researchers and operators so large fleets stay busy without breaking allocation promises.

## What Jobtree does

- **Budget-aware orchestration** – Tracks spend commitments for labs, projects, and teams, ensuring scheduled work stays inside each budget window.
- **Gang scheduling with hierarchy** – Treats related pods as *runs* grouped into trees, so dependent stages start together or not at all.
- **Smart packing and borrowing** – Packs workloads onto heterogeneous clusters, supports short-term borrowing across orgs, and gracefully shrinks elastic runs when pressure rises.
- **Fast starts and reservations** – Combines immediate binders with forecasted reservations so urgent work launches quickly while long queues remain predictable.
- **Observability first** – Ships dashboards, event streams, and a CLI (`kubectl runs`) to explain scheduling decisions and surface bottlenecks.

## Why it is useful

- Keeps **GPU fleets saturated** while preventing surprise overruns against shared budgets.
- Gives researchers a **simple mental model** (runs, trees, budgets) instead of bespoke batch scripts or per-cluster rules.
- Reduces operator toil through **automation of admission control, failure handling, and hot spares**.
- Provides **clear accountability**: who owns a run, what it costs, when it can start, and how it will adapt under contention.

## Key concepts

- **Run**: a set of pods that must start together; may request elasticity or co-funding.
- **Job tree**: a hierarchy of runs representing a workflow (e.g., data prep → training → evaluation) with shared quotas.
- **Budget window**: time-scoped allowance (hours, credits, GPUs) that governs what can be scheduled and when.
- **Borrowing & oversubscription**: policies that let teams temporarily exceed their caps without starving critical work.

## Quick links

- Start with the [overview](concepts/overview.md) for goals, architecture, and terminology.
- Learn how researchers submit and track work in the [researcher guide](user-guide/researcher-guide.md).
- See how operators install and tune clusters in the [operator setup guide](operator-guide/admin-setup.md).
- Explore [roadmap milestones](roadmap/milestones.md) and [maintainers](MAINTAINERS.md) to understand ownership.
- Check the [first-class readiness checklist](project/first-class-readiness.md) if you are evaluating production adoption.

## How it is different

- **Budget-native scheduling**: budgets are first-class primitives, not side-band alerts.
- **Tree-shaped workloads**: understands dependencies and elastic stages, not just flat jobs.
- **Predictable starts**: blends immediate binding, reservations, and shrink-to-fit to minimize idle time.
- **Transparent UX**: every scheduling decision is auditable via CLI and dashboards.

## Where to go next

- Try a dry run with the simulated cluster: `kubectl runs --state cluster.yaml plan <run>`.
- Deploy the observability stack via Helm at `deploy/helm/gpu-fleet/` or use the Kustomize overlays in `deploy/kustomize/`.
- Review the [directory tree plan](architecture/directory-tree.md) to see how the codebase will evolve.

If you only remember one thing: Jobtree keeps your shared GPU clusters busy, fair, and budget-correct—without making researchers think about schedulers.
