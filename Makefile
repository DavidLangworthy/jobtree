.PHONY: test fmt

fmt:
gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

test:
go test ./...
