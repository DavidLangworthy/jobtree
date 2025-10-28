# Repository layout roadmap

The tree below captures the intended long-term structure of the repository, providing an orientation map for future milestones. Directories that do not yet exist are aspirational and will be created as their corresponding milestones are implemented.

```text
gpu-fleet/
├─ go.mod
├─ go.sum
├─ Makefile
├─ README.md
├─ LICENSE
├─ CODEOWNERS
├─ .github/
│  ├─ workflows/
│  │  ├─ ci.yaml
│  │  ├─ e2e.yaml
│  │  └─ release.yaml
│  └─ ISSUE_TEMPLATE.md
│
├─ api/
│  └─ v1/
│     ├─ budget_types.go
│     ├─ run_types.go
│     ├─ reservation_types.go
│     ├─ lease_types.go
│     ├─ groupversion_info.go
│     └─ zz_generated.deepcopy.go
│
├─ controllers/
│  ├─ budget_controller.go
│  ├─ run_controller.go
│  ├─ reservation_controller.go
│  ├─ lease_controller.go
│  ├─ webhooks/
│  │  ├─ budget_webhook.go
│  │  ├─ run_webhook.go
│  │  └─ kustomizeconfig.yaml
│  └─ suite_test.go
│
├─ pkg/
│  ├─ cover/
│  │  ├─ cover.go
│  │  └─ lending.go
│  ├─ pack/
│  │  ├─ domains.go
│  │  ├─ pack_to_empty.go
│  │  └─ grouping.go
│  ├─ binder/
│  │  ├─ bind.go
│  │  └─ materialize.go
│  ├─ resolver/
│  │  ├─ structural_cuts.go
│  │  ├─ lottery.go
│  │  └─ scope.go
│  ├─ forecast/
│  │  ├─ deficit_predictor.go
│  │  └─ remedies.go
│  ├─ index/
│  │  ├─ active_index.go
│  │  └─ usage_index.go
│  ├─ ledger/
│  │  └─ ledger.go
│  ├─ policy/
│  │  ├─ reclaim.go
│  │  └─ budgets.go
│  ├─ topology/
│  │  └─ labels.go
│  ├─ util/
│  │  ├─ prng.go
│  │  ├─ clock.go
│  │  └─ k8s.go
│  └─ version/
│     └─ version.go
│
├─ cmd/
│  ├─ manager/
│  │  ├─ main.go
│  │  └─ Dockerfile
│  ├─ binder/
│  │  ├─ main.go
│  │  └─ Dockerfile
│  ├─ scheduler-plugin/
│  │  ├─ main.go
│  │  ├─ plugin.go
│  │  └─ Dockerfile
│  ├─ notifier/
│  │  ├─ main.go
│  │  └─ Dockerfile
│  └─ kubectl-runs/
│     ├─ cmd/
│     │  ├─ root.go
│     │  ├─ submit.go
│     │  ├─ plan.go
│     │  ├─ watch.go
│     │  ├─ explain.go
│     │  ├─ shrink.go
│     │  ├─ budgets.go
│     │  ├─ sponsors.go
│     │  └─ leases.go
│     ├─ kube/
│     │  └─ clients.go
│     ├─ main.go
│     └─ Dockerfile
│
├─ config/
│  ├─ crd/
│  │  └─ bases/
│  │     ├─ rq.davidlangworthy.io_budgets.yaml
│  │     ├─ rq.davidlangworthy.io_runs.yaml
│  │     ├─ rq.davidlangworthy.io_reservations.yaml
│  │     └─ rq.davidlangworthy.io_leases.yaml
│  ├─ rbac/
│  ├─ manager/
│  │  ├─ manager.yaml
│  │  └─ kustomization.yaml
│  ├─ scheduler/
│  │  ├─ scheduler-config.yaml
│  │  └─ deployment.yaml
│  ├─ webhook/
│  │  ├─ kustomization.yaml
│  │  └─ service.yaml
│  ├─ samples/
│  │  ├─ budgets/
│  │  │  ├─ budget-west-h100.yaml
│  │  │  └─ budget-multienv.yaml
│  │  ├─ runs/
│  │  │  ├─ run-128-groups.yaml
│  │  │  ├─ run-incr-sweep.yaml
│  │  │  └─ run-with-spares.yaml
│  │  ├─ reservations/
│  │  │  └─ reservation-example.yaml
│  │  └─ leases/
│  │     └─ README.md
│  ├─ default/
│  │  └─ kustomization.yaml
│  └─ kustomization.yaml
│
├─ deploy/
│  ├─ helm/
│  │  └─ gpu-fleet/
│  │     ├─ Chart.yaml
│  │     ├─ values.yaml
│  │     └─ templates/...
│  └─ kustomize/
│     ├─ dev/
│     └─ prod/
│
├─ docs/
│  ├─ overview.md
│  ├─ concepts/
│  │  ├─ budgets.md
│  │  ├─ runs.md
│  │  ├─ reservations.md
│  │  └─ leases.md
│  ├─ design/
│  │  ├─ calculus.md
│  │  ├─ cover-and-lending.md
│  │  ├─ oversubscription.md
│  │  └─ failure-and-spares.md
│  ├─ operator-guide/
│  │  ├─ install.md
│  │  ├─ topology-labeling.md
│  │  ├─ quotas-and-families.md
│  │  └─ upgrades.md
│  ├─ user-guide/
│  │  ├─ quickstart.md
│  │  ├─ elastic-runs.md
│  │  ├─ spares-and-fill.md
│  │  ├─ cofunded-runs.md
│  │  └─ troubleshooting.md
│  └─ cli/
│     ├─ kubectl-runs.md
│     └─ completions.md
│
├─ examples/
│  ├─ single-domain/
│  │  ├─ budget.yaml
│  │  ├─ run-fixed-32.yaml
│  │  ├─ run-incr-24.yaml
│  │  └─ demo.md
│  ├─ two-domains-family-sharing/
│  │  ├─ budgets-rai-mm.yaml
│  │  ├─ run-rai-64-spares.yaml
│  │  ├─ run-mm-64.yaml
│  │  └─ demo.md
│  └─ cofunded-run/
│     ├─ budget-rai.yaml
│     ├─ budget-mm-vision.yaml
│     ├─ run-128-sponsors.yaml
│     └─ demo.md
│
├─ tests/
│  ├─ unit/
│  │  ├─ cover_test.go
│  │  ├─ pack_test.go
│  │  ├─ resolver_test.go
│  │  ├─ forecast_test.go
│  │  └─ ledger_test.go
│  └─ e2e/
│     ├─ kind.yaml
│     ├─ labeler.sh
│     ├─ e2e_test.go
│     ├─ fixtures/
│     │  ├─ budgets/*.yaml
│     │  └─ runs/*.yaml
│     └─ README.md
│
├─ hack/
│  ├─ kube-codegen.sh
│  ├─ local-kind-up.sh
│  ├─ local-kind-down.sh
│  └─ generate-yamls.sh
│
└─ plugins/
   └─ krew/
      └─ runs.yaml
```

The `jobtree` repository will adopt this layout as features from the milestone roadmap become available.
