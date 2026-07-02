.PHONY: test fmt generate manifests verify-generate helm-lint cli-build cli-test

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
