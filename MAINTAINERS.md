# Maintainers

## Maintainer

| GitHub handle | Role |
| ------------- | ---- |
| [@DavidLangworthy](https://github.com/DavidLangworthy) | Sole maintainer and copyright holder. Every decision, every merge. |

There is one maintainer. There is no rotation, no on-call, no review quorum, and
no voting process — earlier versions of this file described all four, along with
four "maintainers" who were component names rather than people. None of it existed.

## How to reach the project

- **Security issues:** [SECURITY.md](SECURITY.md) — use GitHub's private
  vulnerability reporting. No email address is published, and none is needed.
- **Everything else:** open a GitHub issue.

## Contributions

**Not currently accepted.** [No licence is granted](LICENSE) for this code, so
there is nothing to contribute *under*. The source is public to be read and
discussed. If that changes, it will change in `LICENSE` and here first.

## How decisions get made

The maintainer decides; the work is done by AI agents under his direction. Design
decisions are recorded — with their reasoning, and with the owner's rulings quoted
verbatim — in
[`docs/project/remediation/IMPLEMENTATION-LOG.md`](docs/project/remediation/IMPLEMENTATION-LOG.md).
The working method, including the rules that were learned the expensive way, is
written down in [`docs/project/working-agreement.md`](docs/project/working-agreement.md).

## Authorship

A substantial share of this repository — code, tests, design documents, and this
file — was written by **Claude** (Anthropic) working under David Langworthy's
direction. This is recorded, commit by commit, in the git history: **97 of 189
commits** carry a `Co-Authored-By: Claude` trailer as of 2026-07-09. Run
`git log --grep="Co-Authored-By: Claude"` to see them.

Several load-bearing design documents were produced by Claude models working in
distinct roles — design and hard root-cause, implementation, and adversarial
review — a split described in
[`docs/project/working-agreement.md`](docs/project/working-agreement.md).
Adversarial review, by one model of another's code, caught four merge-blocking
defects in the funding path that the unit tests and the golden oracle both passed.

**Claude is not a maintainer**, and is not listed above. A maintainer must be
accountable: able to answer a security report, to take responsibility for a merge,
and to be reached by a person harmed by a mistake. That is a property of people,
not of models — and it is the same principle this project applies to its own
tenancy design, where *permissions flow with accountability*. The credit belongs
in the git history and in this section. The responsibility belongs to the human
whose name is at the top of this file.
