.PHONY: test envtest fmt generate manifests verify-generate spec-check spec-counterexamples helm-lint cli-build cli-test

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
