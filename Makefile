.PHONY: test envtest fmt generate manifests verify-generate spec-check spec-counterexamples helm-lint cli-build cli-test antifake kind-up kind-down e2e-image e2e

# Regenerate deepcopy functions from the API types.
generate:
	go tool controller-gen object paths=./api/v1/...

# Regenerate CRD manifests and sync them into the Helm chart.
# allowDangerousTypes permits the float64 GPU-hour fields, which appear only
# in status (a derived cache, never read back by the control path).
manifests:
	go tool controller-gen crd:allowDangerousTypes=true paths=./api/v1/... output:crd:artifacts:config=config/crd/bases
	go tool controller-gen webhook paths=./controllers/kube/... output:webhook:artifacts:config=config/webhook
	cp config/crd/bases/*.yaml deploy/helm/gpu-fleet/crds/

# CI guard: generated artifacts must be committed up to date.
verify-generate: generate manifests
	git diff --exit-code -- api/v1/zz_generated.deepcopy.go config/crd config/webhook deploy/helm/gpu-fleet/crds

fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

test:
	go test ./...

# Integration tests against a real API server (envtest). The suite skips
# itself when KUBEBUILDER_ASSETS is not set, so `go test ./...` stays
# self-contained; this target provides the assets and forces the run.
# The assets are resolved as a separate set -e statement: in the
# `VAR=$$(...) cmd` prefix form the substitution's failure is discarded and
# the suite would silently skip.
ENVTEST_K8S_VERSION ?= 1.36.2
SETUP_ENVTEST := go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.1

envtest:
	@set -e; \
	assets="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)"; \
	KUBEBUILDER_ASSETS="$$assets" go test -race -count=1 ./controllers/kube/...

helm-lint:
	helm lint deploy/helm/gpu-fleet

# Anti-fake lint gates (Track F — TESTINFRA; docs/project/make-it-real-plan.md,
# docs/project/fake-features-audit.md §3 "the pattern"). Also covered by a
# plain `go test ./...`, but named separately so CI can report it (and so it
# can be run standalone) as the mechanical enforcement of the anti-fake
# discipline, not just incidental test coverage:
#   - no *_test.go may hand-set a workload Pod's terminal .Status.Phase
#     without a documented, ratcheted, annotated exception
#     (hack/antifake/terminalphase.go)
#   - no api/v1 CRD field may ship with zero non-generated, non-test readers
#     outside api/v1 without the same (hack/antifake/crdfields.go)
antifake:
	go test ./hack/antifake/... -v

# kind e2e harness (Track F — TESTINFRA-1/2/7): real API server, real
# manager, real webhooks, real kubelet. `make e2e` is the fail-hard,
# don't-skip entry point — see hack/e2e/run-e2e.sh. `kind-up`/`kind-down` let
# you drive the cluster manually (e.g. `make kind-up`, poke at it with
# kubectl, `make kind-down` when done).
kind-up:
	hack/e2e/kind-up.sh

kind-down:
	hack/e2e/kind-down.sh

e2e-image:
	@set -a; . hack/e2e/versions.env; set +a; \
	echo "Building $$E2E_IMAGE"; \
	docker build -t "$$E2E_IMAGE" .; \
	kind load docker-image "$$E2E_IMAGE" --name "$$KIND_CLUSTER_NAME"

e2e:
	hack/e2e/run-e2e.sh

cli-build:
	@mkdir -p bin
	go build -o bin/kubectl-runs ./cmd/kubectl-runs

cli-test:
	go test ./cmd/kubectl-runs/...

TLA2TOOLS := specs/.cache/tla2tools.jar
TLC := java -XX:+UseParallelGC -cp .cache/tla2tools.jar tlc2.TLC -deadlock -workers auto

$(TLA2TOOLS):
	mkdir -p $(dir $(TLA2TOOLS))
	curl -fsSL -o $(TLA2TOOLS) https://github.com/tlaplus/tlaplus/releases/latest/download/tla2tools.jar

# Model-check the design-level specs (the entry gate for the Kubernetes port).
spec-check: $(TLA2TOOLS)
	cd specs && $(TLC) -config ReservationLifecycle.cfg ReservationLifecycle.tla
	cd specs && $(TLC) -config BudgetConservation.cfg BudgetConservation.tla
	cd specs && $(TLC) -config QuotaEvaluation.cfg QuotaEvaluation.tla

# The historical bugs, demonstrated: these configurations MUST fail.
spec-counterexamples: $(TLA2TOOLS)
	cd specs && ! $(TLC) -config ReservationLifecycleBug.cfg ReservationLifecycle.tla
	cd specs && ! $(TLC) -config BudgetConservationRacy.cfg BudgetConservation.tla
