# Jobtree

Kubernetes-native gang scheduling for **job forests** that need to honor **time-scoped organizational budgets**. Jobtree coordinates researchers and operators so large fleets stay busy without breaking allocation promises.

## What Jobtree does

- **Real GPU workloads** – A Run's pods run the researcher's own container image and command
  (`spec.roles[].template`) and request real `nvidia.com/gpu`. The **jobtree scheduler
  plugin** — a kube-scheduler-framework plugin (`schedulerName: jobtree`) — places every pod
  and is the sole committer of GPU funding: it mints the pod's Lease at bind time.
- **Budget-aware orchestration** – Tracks spend commitments for labs, projects, and teams, ensuring scheduled work stays inside each budget window.
- **Gang scheduling with dependencies** – A *run* is a set of pods that start together or not at all; runs chain into workflows with `follow` (start after the runs it follows complete), and are funded by a hierarchy of budgets.
- **Smart packing and borrowing** – Packs workloads onto heterogeneous clusters, supports short-term borrowing across orgs, and gracefully shrinks elastic runs when pressure rises.
- **Fast starts and reservations** – The scheduler plugin binds and funds work the moment it's feasible; forecasted Reservations keep long queues predictable when it isn't.
- **Observability first** – Ships dashboards, event streams, and a CLI (`kubectl runs`) to explain scheduling decisions and surface bottlenecks.

## Why it is useful

- Keeps **GPU fleets saturated** while preventing surprise overruns against shared budgets.
- Gives researchers a **simple mental model** (runs, trees, budgets) instead of bespoke batch scripts or per-cluster rules.
- Reduces operator toil through **automation of admission control, failure handling, and hot spares**.
- Provides **clear accountability**: who owns a run, what it costs, when it can start, and how it will adapt under contention.

## Key concepts

- **Run**: a set of pods running the researcher's real container, scheduled together (or not at
  all) by the jobtree scheduler plugin; may request elasticity or co-funding.
- **Job forest**: runs joined by `follow` edges into workflows (e.g., data prep → training → evaluation), funded by a hierarchy of budgets. A follower waits until every run it follows completes; if one fails it waits a grace period (so you can fix and resubmit just that stage) and then fails honestly.
- **Budget window**: time-scoped allowance (hours, credits, GPUs) that governs what can be scheduled and when.
- **Borrowing & oversubscription**: policies that let teams temporarily exceed their caps without starving critical work.

## Quick links

- Start with the [overview](concepts/overview.md) for goals, architecture, and terminology.
- Learn how researchers submit and track work in the [researcher guide](user-guide/researcher-guide.md).
- See how operators install and tune clusters in the [operator setup guide](operator-guide/admin-setup.md).
- Explore [roadmap milestones](roadmap/milestones.md) and [maintainers](MAINTAINERS.md) to understand ownership.
- Dive into the [RQΛ fundamentals](fundamentals.md) for the quota- and topology-aware calculus behind the control plane.

## How it is different

- **Budget-native scheduling**: budgets are first-class primitives, not side-band alerts.
- **Workflow-shaped workloads**: runs express ordering with `follow` and scale with elastic stages, not just flat jobs.
- **Predictable starts**: blends immediate binding, reservations, and shrink-to-fit to minimize idle time.
- **Transparent UX**: every scheduling decision is auditable via CLI and dashboards.

## Where to go next

- Try an offline dry run with the local simulator, which models both the controller's and the
  scheduler plugin's decisions: `kubectl runs --local --state cluster.yaml plan <run>`.
- Deploy the observability stack via Helm at `deploy/helm/gpu-fleet/` or use the Kustomize overlays in `deploy/kustomize/`.
- Review the [directory tree plan](architecture/directory-tree.md) to see how the codebase will evolve.

If you only remember one thing: Jobtree keeps your shared GPU clusters busy, fair, and budget-correct—without making researchers think about schedulers.
