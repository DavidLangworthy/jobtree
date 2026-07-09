# Security

## Reporting a vulnerability

**Use GitHub's private vulnerability reporting:**

> **[Report a vulnerability](https://github.com/DavidLangworthy/jobtree/security/advisories/new)**
> — or go to the repository's **Security** tab and choose *Report a vulnerability*.

That opens a private thread visible only to you and the maintainer. It does not
create a public issue, and it does not require anyone to publish an email address.

**Please do not open a public issue for a security problem**, and please do not
include exploit details in a public discussion, pull request, or commit message.

## What to expect

jobtree is a **pre-release, single-maintainer** project with no published
releases and [no licence granted](LICENSE). There is no support contract, no
response-time commitment, and no security-release process yet. Reports are read
and answered on a best-effort basis.

If a report describes a real issue, the fix and its reasoning are recorded in
[`docs/project/remediation/IMPLEMENTATION-LOG.md`](docs/project/remediation/IMPLEMENTATION-LOG.md),
as every other correctness fix in this repository is.

## Supported versions

None. Nothing has been released. `main` is the only branch that receives fixes.

## What is in scope

jobtree is a Kubernetes scheduler plugin and controller that decides which
workloads may consume GPUs, and charges that consumption against tenants'
budgets. The security-relevant surfaces are:

- **The funding path** — anything that lets a tenant charge GPU time to a budget
  they do not own, spend beyond their quota, or cause the scheduler to commit
  capacity twice. The scheduler plugin is the sole committer of funding; it mints
  one `Lease` per pod.
- **The provenance trust anchor** — the `ValidatingAdmissionPolicy` that restricts
  jobtree-owned pod fields (`rq.davidlangworthy.io/*` annotations, the `role`
  label, `schedulerName: jobtree`) to the controller's ServiceAccount.
- **Tenancy** — anything that lets one namespace read, spend, or influence
  another's budgets, leases, or runs.
- **RBAC and the Helm chart** — privilege escalation via the shipped roles, or a
  chart that grants more than the manager needs.

Known-open issues are tracked in the remediation board
([`docs/project/remediation/README.md`](docs/project/remediation/README.md)) and
in the audit that found them
([`docs/project/design-vs-implementation-audit.md`](docs/project/design-vs-implementation-audit.md)).
A finding already listed there is not a new report, but a better exploit for one
is very welcome.

## What is out of scope

- Vulnerabilities in Kubernetes itself, or in dependencies — report those upstream.
- Anything that requires cluster-admin, or the ability to write `Budget` objects.
  Budgets are administrator-owned by design; a principal who can write them can
  already allocate quota to themselves. See the tenancy design in
  [`docs/project/remediation/R7-tenancy-amendment.md`](docs/project/remediation/R7-tenancy-amendment.md).
