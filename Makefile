.PHONY: test fmt helm-lint cli-build cli-test

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
