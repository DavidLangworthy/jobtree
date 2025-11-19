# Read the Docs Site

Jobtree now ships a **fully navigable documentation site** backed by MkDocs + Material and published
on [Read the Docs](https://readthedocs.io). This page describes how we structure the site, build it
locally, and keep the hosted version healthy.

## 1. Tooling

* **Engine:** MkDocs with the Material theme (great search, mobile-friendly, Markdown-first).
* **Config:** `mkdocs.yml` at the repo root defines navigation, palette, and search.
* **Dependencies:** listed in `docs/requirements.txt` (installed by CI and Read the Docs).

Install locally:

```bash
python3 -m venv .venv-docs
source .venv-docs/bin/activate
pip install -r docs/requirements.txt
```

Serve with hot reload:

```bash
mkdocs serve
# open http://127.0.0.1:8000
```

## 2. Navigation model

The live site organizes content for three primary audiences:

1. **Researchers** – `docs/user-guide/researcher-guide.md` (quick start, elasticity, borrowing).
2. **Operators** – `docs/operator-guide/admin-setup.md` plus observability + visualization guides.
3. **Migrators** – SLURM and Kueue bridges that map their vocabulary to Jobtree’s abstractions.

Reference sections cover Budgets, Runs, Leases, CLI usage, worked examples, and project governance.
Navigation is defined in `mkdocs.yml` so Read the Docs renders the same structure we preview locally.

## 3. Continuous integration

`.github/workflows/docs.yaml` builds the site on every pull request that touches docs, MkDocs
configuration, or the MAINTAINERS roster:

```yaml
runs-on: ubuntu-latest
steps:
  - uses: actions/checkout@v4
  - uses: actions/setup-python@v5
    with:
      python-version: '3.11'
  - run: pip install -r docs/requirements.txt
  - run: mkdocs build --strict
```

This prevents broken navigation, missing pages, or formatting regressions from landing in `main`.

## 4. Read the Docs project settings

1. Create the project at https://readthedocs.org/dashboard/ and point it to
   `github.com/davidlangworthy/jobtree`.
2. In **Advanced Settings**:
   * Documentation type: *MkDocs*.
   * Python version: *3.11*.
   * Requirements file: `docs/requirements.txt`.
   * Enable pull-request previews for docs-heavy branches.
3. Add the Read the Docs badge to `README.md` (already done) so users can discover the site.

## 5. Content review checklist

* New features must add or update a page referenced in `mkdocs.yml`.
* Worked examples should be runnable via manifests in `config/samples/**`.
* When editing specs (Budget/Run/Reservation/Lease), keep the corresponding concept page in sync.
* Add images (cluster allocation heatmaps, CLI screenshots) under `docs/visualizations/` when
  they materially help the story.

## 6. Future enhancements

* Generate API reference pages from CRD Go doc comments using `mkdocs-gen-files`.
* Publish CLI reference directly from `kubectl runs --help` output as part of CI.
* Embed Grafana dashboard PNGs in the observability section for quick recognition.

With these steps Jobtree feels like a first-class project: newcomers land on a polished site, see
examples tailored to their persona, and can cross-reference specs, guides, and roadmap updates.
