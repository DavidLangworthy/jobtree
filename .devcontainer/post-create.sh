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

step "Installing the docs toolchain (mkdocs) into a venv at ~/.venvs/docs"
if sudo apt-get install -y -qq python3-pip python3-venv 2>/dev/null \
   && python3 -m venv "$HOME/.venvs/docs" \
   && "$HOME/.venvs/docs/bin/pip" install -q -r docs/requirements.txt; then
  echo "  docs build: ~/.venvs/docs/bin/mkdocs build --strict   (matches Read the Docs' fail_on_warning)"
else
  echo "WARN: docs toolchain install failed"
fi

step "Installing the weekly disk-hygiene cron (in-codespace only)"
# This cron runs INSIDE the codespace. It never starts the codespace and does
# nothing while the codespace is stopped -- an idle codespace's disk isn't
# growing, so there is nothing to reclaim. When the codespace IS running it
# checks every 6 hours and only acts once / crosses 70% full (see
# disk-hygiene.sh --if-above), so a normal week's warm caches are left alone.
# `sudo service cron start` is repeated in devcontainer.json's postStartCommand
# so the daemon comes back after every stop/start, not just at create time.
HYGIENE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/disk-hygiene.sh"
if sudo apt-get install -y -qq cron 2>/dev/null; then
  sudo service cron start || echo "  WARN: could not start cron now (postStartCommand will)"
  cron_line="0 */6 * * * $HYGIENE --if-above 70 >> \$HOME/.disk-hygiene.log 2>&1"
  ( crontab -l 2>/dev/null | grep -vF 'disk-hygiene.sh'; echo "$cron_line" ) | crontab -
  echo "  installed: $cron_line"
  echo "  on-demand: make disk-hygiene   (log: ~/.disk-hygiene.log)"
else
  echo "WARN: cron install failed; run 'make disk-hygiene' by hand when the disk fills"
fi

step "Done. Quick reference:"
echo "  go test ./...                      # unit tests (~20s)"
echo "  kind create cluster                # local real cluster"
echo "  kwokctl create cluster --wait 60s  # fake-node scale cluster"
echo "  make disk-hygiene                  # reclaim disk (Go/docker caches) if it fills"
