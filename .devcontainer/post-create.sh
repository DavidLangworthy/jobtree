#!/usr/bin/env bash
# One-time setup after the devcontainer is created: Tier 3 tooling and a warm
# build cache. Solver and agent prerequisites fail closed. Optional cluster
# helpers are explicitly guarded so their failure does not brick the Codespace.
set -euo pipefail

step() {
  echo "==> $*"
}

step "Installing tmux (persistent sessions over ssh)"
sudo apt-get update -qq && sudo apt-get install -y -qq tmux || echo "WARN: tmux install failed"

step "Installing the current Codex and Claude CLIs"
# The devcontainer features provide Node and an initial Claude install. Upgrade
# both agent CLIs at creation time so a long-lived prebuild does not silently
# pin the formal-verification campaign to an old agent.
node --version
node_major="$(node -p 'Number(process.versions.node.split(".")[0])')"
if (( node_major < 22 )); then
  echo "ERROR: current Claude Code requires Node >=22; found $(node --version)" >&2
  exit 1
fi
sudo env "PATH=$PATH" npm install -g @openai/codex@latest @anthropic-ai/claude-code@latest
codex --version
claude --version

step "Installing and validating the formal-verification toolchain"
# Makefile pins Apalache and owns both cache layouts. Use its targets instead of
# teaching the devcontainer a second set of solver download/version rules.
java -version
make specs/.cache/tla2tools.jar
make specs/.cache/apalache/bin/apalache-mc
tlc_help="$(java -cp specs/.cache/tla2tools.jar tlc2.TLC -help 2>&1 || true)"
grep -q "TLC - provides model checking" <<<"$tlc_help"
specs/.cache/apalache/bin/apalache-mc version

step "Cloning the sibling Kubernetes lifecycle model"
if [ ! -d /workspaces/tla-k8s/.git ]; then
  git clone --branch codex/codespaces-ci-security --single-branch \
    https://github.com/DavidLangworthy/tla-k8s.git /workspaces/tla-k8s \
    || echo "WARN: could not clone tla-k8s; clone it manually before model-gap analysis"
fi

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
  existing_cron="$(crontab -l 2>/dev/null || true)"
  {
    printf '%s\n' "$existing_cron" | grep -vF 'disk-hygiene.sh' || true
    echo "$cron_line"
  } | crontab -
  echo "  installed: $cron_line"
  echo "  on-demand: make disk-hygiene   (log: ~/.disk-hygiene.log)"
else
  echo "WARN: cron install failed; run 'make disk-hygiene' by hand when the disk fills"
fi

step "Done. Quick reference:"
echo "  go test ./...                      # unit tests (~20s)"
echo "  make spec-check                    # baseline TLC models"
echo "  make ledger-compaction-apalache-check  # ordinary bounded SMT rail"
echo "  codex --version && claude --version"
echo "  kind create cluster                # local real cluster"
echo "  kwokctl create cluster --wait 60s  # fake-node scale cluster"
echo "  make disk-hygiene                  # reclaim disk (Go/docker caches) if it fills"
