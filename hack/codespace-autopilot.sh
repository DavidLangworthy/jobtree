#!/usr/bin/env bash
#
# codespace-autopilot.sh — run Claude Code UNATTENDED against the remediation backlog.
#
# Meant for a disposable GitHub Codespace (a container, so --dangerously-skip-permissions
# is allowed even though it refuses on a bare host as root). It does NOT stop for approval
# prompts; the safety rails are the deny list in `.claude/settings.json` (which still
# applies under bypass) plus the PARK LIST in the playbook, which the agent is told to
# obey. It will still make its own implementation decisions — read the playbook first.
#
#   docs/project/autonomous-run-playbook.md   <- the operating contract (READ THIS)
#   .claude/settings.json                     <- deny guardrails (rm -rf, force-push,
#                                                pr merge, secrets) that hold under bypass
#
# Usage:
#   hack/codespace-autopilot.sh            # run the loop (default cap: 8 turns)
#   AUTOPILOT_MAX_ITERS=20 hack/codespace-autopilot.sh
#
# Stop it any time with Ctrl-C; every landed item is its own pushed PR, so nothing is lost.

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

command -v claude >/dev/null 2>&1 || { echo "error: 'claude' CLI not found on PATH" >&2; exit 1; }

MAX_ITERS="${AUTOPILOT_MAX_ITERS:-8}"
SENTINEL=".autopilot-done"
rm -f "$SENTINEL"

read -r -d '' KICKOFF <<'PROMPT' || true
You are running UNATTENDED in a disposable Codespace. There is no human to ask.

1. Read AGENTS.md and docs/project/autonomous-run-playbook.md in full, then follow the
   playbook exactly.
2. Work the OPEN remediation items in priority order. One PR per item: branch off main,
   implement, make the per-PR gate green (make verify + envtest + eviction fuzzer for
   engine/plugin/funding changes), mutation-verify each fix, commit, and push. Do NOT
   merge and do NOT run a per-PR adversarial review.
3. Obey the PARK LIST: never make an owner decision (R7 pt2, R4 pt1b staleness bound,
   R4 pt2b, ROLES, or any new policy question). Record parked items in
   docs/project/DECISIONS-NEEDED.md and move on.
4. Record every implementation judgment call in
   docs/project/remediation/IMPLEMENTATION-LOG.md.
5. Keep both boards (remediation/README.md, SIZING.md) in sync with what you land.

When every UNPARKED item is done or has an open PR, or you hit a stop condition in the
playbook, write a one-line summary to the file .autopilot-done at the repo root and stop.
PROMPT

echo ">> autopilot: first turn"
claude --dangerously-skip-permissions -p "$KICKOFF"

# No built-in "loop until done" exists — this is a bounded resume loop. It nudges the same
# session onward until the agent writes the sentinel or the cap is hit. If your Claude Code
# version does not support `--continue -p`, drop the loop and re-run the first turn instead.
for i in $(seq 1 "$MAX_ITERS"); do
  if [ -f "$SENTINEL" ]; then
    echo ">> autopilot: agent signaled done — $(cat "$SENTINEL")"
    exit 0
  fi
  echo ">> autopilot: continuation turn $i/$MAX_ITERS"
  claude --dangerously-skip-permissions --continue -p \
    "Continue per docs/project/autonomous-run-playbook.md. If every unparked item is done or has an open PR, or you hit a stop condition, write a one-line summary to .autopilot-done and stop."
done

echo ">> autopilot: hit the $MAX_ITERS-turn cap without a done sentinel; stopping. Review open PRs and DECISIONS-NEEDED.md."
