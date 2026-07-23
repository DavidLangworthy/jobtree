#!/usr/bin/env bash
# Codespace disk hygiene. This is maintenance, not a workaround.
#
# The codespace disk is 32 GB. Two things fill it across a week of building on
# many branches, and neither bounds itself:
#
#   * the Go BUILD cache (~/.cache/go-build) — measured at 13 GB on a disk that
#     was otherwise 6 GB of real content;
#   * the docker-in-docker image + buildkit store — every `make e2e-image`
#     leaves layers and dangling build cache behind.
#
# When the disk fills it also runs up the account's Codespaces STORAGE bill,
# because GitHub charges for the codespace's disk footprint for the whole time
# the codespace exists (retention here is measured in weeks).
#
# Everything this removes is regenerable: the next `go build` re-warms the build
# cache, the next `make e2e` re-pulls the base images. It never touches the git
# tree, the module SOURCES (~/go/pkg/mod), or your home dot-config. So it is
# safe to run whenever, and safe to run on a timer.
#
# Usage:
#   .devcontainer/disk-hygiene.sh                # reclaim now (what `make disk-hygiene` runs)
#   .devcontainer/disk-hygiene.sh --if-above 70  # reclaim only if / is >=70% full (the cron mode)
#
# The --if-above gate is why the weekly cron does not cold-start your caches: on
# a normal week the disk sits well under the watermark and the run is a no-op.
set -uo pipefail

threshold=0
teardown_clusters=1
if [[ "${1:-}" == "--if-above" ]]; then
  threshold="${2:-70}"
  # In watermark (cron) mode, never tear down a kind cluster: you might be using
  # it right now. Unused kind node images are still reclaimed by docker prune.
  teardown_clusters=0
fi

log() { echo "[disk-hygiene] $(date -Is) $*"; }
used_pct() { df --output=pcent / | tail -1 | tr -dc '0-9'; }
used_human() { df -h / | awk 'NR==2 {print $3" / "$2" ("$5")"}'; }

before_pct="$(used_pct)"

if (( threshold > 0 )) && (( before_pct < threshold )); then
  log "/ at ${before_pct}% (< ${threshold}% watermark) — nothing to do"
  exit 0
fi

log "starting — / at $(used_human)"

# The usual runaway. Rebuilds on demand.
if command -v go >/dev/null 2>&1; then
  if go clean -cache 2>/dev/null; then
    log "  cleared Go build cache"
  fi
fi

# Stopped containers, unused images, and dangling buildkit cache. An image that
# a running container (e.g. a kind cluster you are using) depends on is "in use"
# and is NOT removed, so this is safe mid-session. Re-pulled on the next e2e.
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  if docker system prune -af >/dev/null 2>&1; then
    log "  pruned unused docker images + build cache"
  fi
fi

# Dead kind clusters left behind by interrupted e2e runs. Only in manual mode —
# see the note above about not deleting a cluster you may be using.
if (( teardown_clusters )) && command -v kind >/dev/null 2>&1; then
  for c in $(kind get clusters 2>/dev/null); do
    if kind delete cluster --name "$c" >/dev/null 2>&1; then
      log "  deleted kind cluster '$c'"
    fi
  done
fi

log "done — / at $(used_human)"
