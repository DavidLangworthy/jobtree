.PHONY: verify fmt-check vet test-race golden-clean build-bins helm-assert krew-validate test envtest fmt generate manifests verify-generate spec-check spec-counterexamples helm-lint cli-build cli-test antifake kind-up kind-down e2e-build e2e-load e2e-image e2e-bins e2e-build-fast build-flags-agree e2e

# ---------------------------------------------------------------------------
# `make verify` is THE gate. CI runs exactly this target and nothing else, so a
# green `verify` locally means a green CI. Do not add a check to CI without
# adding it here.
#
# This exists because the two lists drifted and a red CI merged: `go test ./...`
# reports `ok` for controllers/kube while SKIPPING the whole envtest suite
# (no KUBEBUILDER_ASSETS), so a sweep that looked green never ran the
# integration tests at all. A gate you can't run before pushing is a gate that
# catches things too late; a gate CI doesn't run is not a gate.
verify: fmt-check vet verify-generate antifake test-race envtest golden-clean build-bins helm-lint helm-assert krew-validate build-flags-agree
	@echo "== make verify: all gates passed"

fmt-check:
	@out="$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"; \
	if [ -n "$$out" ]; then echo "::error::gofmt needed on:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

test-race:
	go test -race ./...

# The golden oracle is a snapshot fixture: a test run must never rewrite it as a
# side effect. Regenerating is a deliberate act (`UPDATE_GOLDEN=1`), and the diff
# is the review artifact. NOTE the golden captures class WIDTHS and lenders, not
# GPU-hour floats — it cannot catch an accrual regression on its own.
golden-clean:
	@go test ./controllers/ -run TestGoldenScenarios >/dev/null
	@if ! git diff --quiet -- controllers/testdata/golden; then \
		echo "::error::golden fixtures changed during a plain test run"; \
		git --no-pager diff --stat -- controllers/testdata/golden; \
		exit 1; \
	fi
	@echo "golden fixtures unchanged"

# -o /dev/null: this gate only asks "does it link?", and a bare `go build` would
# drop `manager` and `kubectl-runs` binaries into the repo root.
build-bins:
	go build -o /dev/null ./cmd/manager
	go build -o /dev/null ./cmd/kubectl-runs

helm-assert:
	hack/ci/helm-assertions.sh

krew-validate:
	hack/ci/krew-validate.sh

# The PR-time e2e image wraps binaries built here rather than inside the
# Dockerfile's golang stage. That is only honest if both compile identically.
build-flags-agree:
	hack/ci/assert-build-flags.sh
# ---------------------------------------------------------------------------

# The one definition of how the shipped binaries are built. The Dockerfile must
# use the same flags; `make build-flags-agree` (part of `verify`) enforces it.
GO_BUILD_ENV := CGO_ENABLED=0 GOOS=linux
GO_LDFLAGS := -s -w
GO_BUILD_FLAGS := -trimpath -ldflags="$(GO_LDFLAGS)"

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
#
# JOBTREE_REQUIRE_ENVTEST turns that last hazard from a comment into an error:
# this target INTENDS to run the integration suite, so if the assets still fail
# to resolve, TestMain exits non-zero instead of skipping to a green `ok`.
ENVTEST_K8S_VERSION ?= 1.36.2
SETUP_ENVTEST := go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.1

envtest:
	@set -e; \
	assets="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)"; \
	KUBEBUILDER_ASSETS="$$assets" JOBTREE_REQUIRE_ENVTEST=1 go test -race -count=1 ./controllers/kube/...

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

# e2e-build and e2e-load are split so CI can run `kind-up` CONCURRENTLY with the
# image build (the cluster is only needed to LOAD). `make e2e-image` keeps the
# old, sequential meaning for local use.
#
# DOCKER_BUILD is overridable so CI can substitute `docker buildx build` with a
# registry-backed layer cache without this target and CI drifting apart.
DOCKER_BUILD ?= docker build

e2e-build:
	@set -a; . hack/e2e/versions.env; set +a; \
	set -e; \
	echo "Building $$E2E_IMAGE (target manager)"; \
	$(DOCKER_BUILD) --target manager -t "$$E2E_IMAGE" .; \
	echo "Building $$E2E_SCHEDULER_IMAGE (target scheduler)"; \
	$(DOCKER_BUILD) --target scheduler -t "$$E2E_SCHEDULER_IMAGE" .

e2e-load:
	@set -a; . hack/e2e/versions.env; set +a; \
	set -e; \
	kind load docker-image "$$E2E_IMAGE" --name "$$KIND_CLUSTER_NAME"; \
	kind load docker-image "$$E2E_SCHEDULER_IMAGE" --name "$$KIND_CLUSTER_NAME"

e2e-image: e2e-build e2e-load

# --- the fast path (PR-time e2e) ------------------------------------------
# Compile on THIS machine, where the Go build cache lives, then wrap the binaries
# in the same distroless base. The Dockerfile's golang stage cannot see that
# cache, and its ~180s compile is ~97% vendored deps that never change: with a
# warm cache, touching pkg/funding/evaluate.go rebuilds the scheduler in 4.3s.
# `make verify` runs build-flags-agree so the binary is the one we ship.
e2e-bins:
	@mkdir -p bin
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o bin/manager ./cmd/manager
	$(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o bin/scheduler ./cmd/scheduler

# Context is ./bin, so the repo-root .dockerignore (which excludes bin) does not
# apply and the context is two files instead of the whole tree.
e2e-build-fast: e2e-bins
	@set -a; . hack/e2e/versions.env; set +a; \
	set -e; \
	docker build -f Dockerfile.fast --target manager   -t "$$E2E_IMAGE"           ./bin; \
	docker build -f Dockerfile.fast --target scheduler -t "$$E2E_SCHEDULER_IMAGE" ./bin

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
