#!/usr/bin/env bash
# Cross-vendor review seat: run OpenAI Codex (gpt-5.6, high reasoning effort, read-only)
# as a HUNTER over the R7 pt2 diff. Honest exit codes: non-zero means the seat produced
# nothing and must fail closed. Per docs/project/codex-review-panelist-assessment.md §3.
set -o pipefail
REPO=/home/runner/work/jobtree/jobtree
OUT=${1:-/tmp/codex/out.json}
LOG=${2:-/tmp/codex/exec.log}
mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"

codex exec \
  --sandbox read-only \
  --ephemeral \
  -m gpt-5.6 \
  -c model_reasoning_effort=high \
  --ignore-user-config \
  --ignore-rules \
  -C "$REPO" \
  --skip-git-repo-check \
  --output-schema /tmp/codex/report.schema.json \
  -o "$OUT" \
  - < /tmp/codex/hunt-prompt.md >"$LOG" 2>&1
rc=$?

if [ $rc -ne 0 ]; then
  echo "CODEX_FAILED exit=$rc" >&2
  tail -30 "$LOG" >&2
  exit $rc
fi
if [ ! -s "$OUT" ]; then
  echo "CODEX_FAILED empty output at $OUT" >&2
  tail -30 "$LOG" >&2
  exit 3
fi
echo "CODEX_OK $OUT"
exit 0
