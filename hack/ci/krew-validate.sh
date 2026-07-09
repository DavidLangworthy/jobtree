#!/usr/bin/env bash
# Validate the krew plugin manifest's platform matrix (R23).
# Extracted from .github/workflows/ci.yaml so `make verify` and CI share it.
set -euo pipefail

m=plugins/krew/runs.yaml

# Each platform must be present as an os/arch PAIR. Pair each arch with the os
# that precedes it in the same stanza (os always comes first), so a missing or
# duplicated combo can't be masked by the strings appearing elsewhere. awk-based
# to avoid a PyYAML dependency on the runner.
pairs="$(awk '/^[[:space:]]*os:/{os=$2} /^[[:space:]]*arch:/{print os"/"$2}' "$m" | sort)"
want=$'linux/amd64\nlinux/arm64\ndarwin/amd64\ndarwin/arm64'
if [ "$pairs" != "$(sort <<<"$want")" ]; then
  echo "::error::krew platforms mismatch; got:" >&2
  echo "$pairs" >&2
  exit 1
fi

test "$(grep -c 'bin: kubectl-runs' "$m")" -eq 4 \
  || { echo "::error::krew manifest must list 4 platforms with bin: kubectl-runs" >&2; exit 1; }
test "$(grep -c '\.tar\.gz' "$m")" -ge 4 \
  || { echo "::error::krew manifest platforms must point at tar.gz archives" >&2; exit 1; }

echo "krew manifest OK"
