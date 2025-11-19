# Maintainers

This file lists the people and teams responsible for the jobtree project and documents the
review/escalation process. Update it whenever ownership changes.

## Core maintainers

| Role | GitHub handle | Primary scope |
| ---- | ------------- | ------------- |
| Technical lead | @davidlangworthy | Architecture, roadmap, release readiness |
| Scheduler lead | @gpu-binder | Binder, packer, resolver implementation |
| Control plane lead | @budget-controller | Budget, cover planner, forecasting |
| CLI & UX lead | @researcher-experience | `kubectl runs`, docs, tutorials |
| Observability lead | @metrics-squad | Metrics, dashboards, SLOs |

## Review process

* All changes must be reviewed by at least one maintainer responsible for the affected scope.
* API changes (CRDs, CLI surface) require sign-off from the technical lead and the CLI & UX lead.
* Release branches are cut by the technical lead. Emergency fixes require two maintainers.

## Escalation

* **Pager / incidents:** contact `oncall@gpu-fleet.example`.
* **Security reports:** email `security@gpu-fleet.example` (within 24h response).
* **Product feedback:** open a GitHub discussion or reach the CLI & UX lead.

## Becoming a maintainer

We welcome new maintainers. To propose membership:

1. Show sustained contributions (code, docs, ops) over at least two release cycles.
2. Ask an existing maintainer to nominate you via a GitHub issue.
3. Majority vote of existing maintainers updates this file.

## Rotation / PTO

Maintainers should keep an up-to-date rotation schedule in the internal calendar. Update this
file if responsibilities shift for more than one release cycle.
