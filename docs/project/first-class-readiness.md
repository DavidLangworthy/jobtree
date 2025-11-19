# First-class project checklist

To stand alongside schedulers like SLURM and IBM Queue, jobtree needs polished community, docs, and
operational tooling. Use this checklist to track remaining work beyond the milestones.

## Documentation

- [ ] Publish docs on Read the Docs (`docs/website/readthedocs.md`).
- [ ] Add architecture diagrams (control loop, cover/pack/bind pipeline).
- [ ] Generate API reference from CRD schemas.
- [ ] Record tutorial videos for researchers and operators.

## Community & governance

- [x] Maintain a `MAINTAINERS.md` file.
- [ ] Define a code of conduct (`CODE_OF_CONDUCT.md`).
- [ ] Set up GitHub Discussions for Q&A.
- [ ] Schedule quarterly community calls / office hours.

## Product experience

- [ ] Build the budget allocation UX (see `docs/product/researcher-budget-ux.md`).
- [ ] Provide mock data + screenshot gallery of the UX.
- [ ] Instrument the CLI with analytics (command usage, errors) respecting privacy.

## Observability & visualizations

- [ ] Publish Grafana dashboard screenshots in docs.
- [ ] Ship a topology heatmap visualization (see `docs/visualizations/cluster-allocation.md`).
- [ ] Automate data exports for downstream BI tools.

## Operations

- [ ] Add conformance tests (kind/e2e) to CI.
- [ ] Provide upgrade playbooks and rollback procedures.
- [ ] Publish container images to an OCI registry with SBOM attestation.

## Adoption

- [ ] Collect case studies from early users.
- [ ] Benchmark vs. SLURM/Kueue on representative workloads.
- [ ] Publish migration guides from other schedulers.

Track progress here and link to GitHub issues for each unchecked item. When this list is complete,
jobtree will present like a first-class open-source project.
