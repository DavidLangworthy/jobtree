#!/usr/bin/env bash
# One-time setup after the devcontainer is created: Tier 3 tooling and a warm
# build cache. Everything here is best-effort; a failure should not brick the
# codespace, so each step reports rather than aborts.
set -uo pipefail

step() {
  echo "==> $*"
}

step "Installing tmux (persistent sessions over ssh)"
sudo apt-get update -qq && sudo apt-get install -y -qq tmux || echo "WARN: tmux install failed"

step "Installing kind (Kubernetes-in-Docker)"
go install sigs.k8s.io/kind@latest || echo "WARN: kind install failed"

step "Installing kwok/kwokctl (scale-testing fake nodes)"
go install sigs.k8s.io/kwok/cmd/kwok@latest || echo "WARN: kwok install failed"
go install sigs.k8s.io/kwok/cmd/kwokctl@latest || echo "WARN: kwokctl install failed"

step "Warming the Go build cache"
go build ./... || echo "WARN: initial build failed"
go vet ./... || echo "WARN: vet failed"

step "Done. Quick reference:"
echo "  go test ./...                      # unit tests (~20s)"
echo "  kind create cluster                # local real cluster"
echo "  kwokctl create cluster --wait 60s  # fake-node scale cluster"
