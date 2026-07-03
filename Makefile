.PHONY: test fmt generate manifests verify-generate spec-check spec-counterexamples helm-lint cli-build cli-test

# Regenerate deepcopy functions from the API types.
generate:
	go tool controller-gen object paths=./api/v1/...

# Regenerate CRD manifests and sync them into the Helm chart.
# allowDangerousTypes permits the float64 GPU-hour fields, which appear only
# in status (a derived cache, never read back by the control path).
manifests:
	go tool controller-gen crd:allowDangerousTypes=true paths=./api/v1/... output:crd:artifacts:config=config/crd/bases
	cp config/crd/bases/*.yaml deploy/helm/gpu-fleet/crds/

# CI guard: generated artifacts must be committed up to date.
verify-generate: generate manifests
	git diff --exit-code -- api/v1/zz_generated.deepcopy.go config/crd deploy/helm/gpu-fleet/crds

fmt:
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

test:
	go test ./...

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
