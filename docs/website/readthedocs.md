# Publishing on Read the Docs

The jobtree documentation already ships as Markdown under `docs/`. This guide explains how to
publish it on [Read the Docs](https://readthedocs.io) so the project feels first-class for new
contributors.

## 1. Choose a documentation engine

Read the Docs supports Sphinx and MkDocs. We recommend **MkDocs** because:

* All content is Markdown today.
* The navigation structure maps cleanly to the existing `docs/` tree.
* It integrates with Material for MkDocs for a polished look.

Install tooling locally:

```bash
pip install mkdocs mkdocs-material
```

## 2. Create `mkdocs.yml`

Add a configuration file at the repository root:

```yaml
site_name: Jobtree
site_url: https://jobtree.readthedocs.io
repo_url: https://github.com/davidlangworthy/jobtree
nav:
  - Overview: docs/concepts/overview.md
  - Quick start:
      - CLI quickstart: docs/user-guide/quickstart.md
      - Elastic runs: docs/user-guide/elastic-runs.md
  - Concepts:
      - Budgets: docs/concepts/budgets.md
      - Runs: docs/concepts/runs.md
      - Reservations: docs/user-guide/reservations.md
      - Leases: docs/concepts/leases.md
  - Operator guide:
      - Install: docs/operator-guide/install.md
      - Observability: docs/operator-guide/observability.md
  - Roadmap: docs/roadmap/milestones.md
  - Community: MAINTAINERS.md
theme:
  name: material
markdown_extensions:
  - admonition
  - footnotes
  - toc:
      permalink: true
```

Commit this file along with `docs/` content.

## 3. Configure Read the Docs

1. Create a project at https://readthedocs.org/dashboard/.
2. Point it at `github.com/davidlangworthy/jobtree`.
3. In project settings:
   * **Documentation type:** MkDocs.
   * **Python version:** 3.11.
   * **Install requirements:** supply a `docs/requirements.txt` with `mkdocs` and `mkdocs-material`.
   * Enable PR previews for documentation branches.
4. Trigger a build to ensure the site renders.

## 4. Continuous integration

Add a GitHub workflow to build docs on pull requests:

```yaml
name: docs
on:
  pull_request:
    paths:
      - 'docs/**'
      - 'mkdocs.yml'
      - 'MAINTAINERS.md'

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: '3.11'
      - run: pip install mkdocs mkdocs-material
      - run: mkdocs build --strict
```

This keeps the site buildable before Read the Docs renders it.

## 5. Badges and discoverability

Once the site is live:

* Add a Read the Docs badge to `README.md`.
* Update onboarding docs to reference `https://jobtree.readthedocs.io` as the canonical entry point.

## 6. Future enhancements

* Host API reference docs generated from CRD schemas.
* Publish CLI command reference using `kubectl runs --help` output.
* Embed Grafana dashboard screenshots in the observability section.

With these steps, jobtree becomes approachable like other first-class schedulers.
