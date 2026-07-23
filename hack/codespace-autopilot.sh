#!/usr/bin/env bash
#
# codespace-autopilot.sh — run Claude Code UNATTENDED against the remediation backlog.
#
# Meant for a disposable GitHub Codespace (a container, so --dangerously-skip-permissions
# is allowed even though it refuses on a bare host as root). It does NOT stop for approval
# prompts; the safety rails are the deny list in `.claude/settings.json` (which still
# applies under bypass) plus the PARK LIST in the playbook, which the agent is told to obey.
#
#   docs/project/autonomous-run-playbook.md   <- the operating contract (READ THIS)
#   .claude/settings.json                     <- deny guardrails that hold under bypass
#
# Effort:  runs Opus at maximum thinking (AUTOPILOT_MODEL, MAX_THINKING_TOKENS below).
# Limits:  when usage limits are hit, it WAITS for the reset and resumes (with a heartbeat
#          that keeps the Codespace from idle-suspending). See the caveat under KEEPALIVE.
# PRs:     each item branches off origin/main (so it picks up other agents' merged work and
#          your redirects); genuinely-dependent items stack. `git fetch` runs every turn.
# Redirect: push to origin/main — the playbook or docs/project/AUTOPILOT-CONTROL.md — and the
#          agent re-reads them each item.
#
# Usage:
#   hack/codespace-autopilot.sh
#   AUTOPILOT_MAX_ITERS=60 hack/codespace-autopilot.sh
#
# Stop any time with Ctrl-C; every landed item is a pushed branch/PR, so nothing is lost.
# If the Codespace is suspended anyway, just re-run this script — `--continue` resumes.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

command -v claude >/dev/null 2>&1 || { echo "error: 'claude' CLI not found on PATH" >&2; exit 1; }

# --- Effort: Opus at max thinking -------------------------------------------------------
# Best-effort levers (unverified against your exact CLI version — sanity-check once):
#   --model opus            selects Opus
#   MAX_THINKING_TOKENS     Claude Code's extended-thinking budget; set high to force it
#   "ultrathink" in prompt  reinforces max reasoning in headless mode
AUTOPILOT_MODEL="${AUTOPILOT_MODEL:-opus}"
export MAX_THINKING_TOKENS="${MAX_THINKING_TOKENS:-31999}"

MAX_ITERS="${AUTOPILOT_MAX_ITERS:-40}"
# Fallback wait when the reset time isn't machine-readable: a Max plan's window is 5h, so
# wait a touch over that, then retry. If a reset time IS printed, we honor it (below).
LIMIT_WAIT="${AUTOPILOT_LIMIT_WAIT:-18600}"   # 5h10m
SENTINEL=".autopilot-done"
LOG=".autopilot-last.log"
rm -f "$SENTINEL"

# --- KEEPALIVE --------------------------------------------------------------------------
# GitHub Codespaces suspend after an idle timeout (default 30m, MAX 240m in personal
# settings). A silent `sleep` can be treated as idle. This heartbeat emits light activity
# so a connected session stays active. HONEST CAVEAT: if idle detection is purely
# connection-based and nobody is attached, a wait longer than the 240m ceiling may still
# suspend the box. Mitigations: (1) set your Codespace idle timeout to the 240m max;
# (2) keep a browser/VS Code tab attached; (3) if it suspends, re-run this script on
# resume — pushed PRs are safe and `--continue` picks up. Size the run so it rarely goes
# fully dark for >4h.
heartbeat() { while true; do date -u +">> autopilot-alive %FT%TZ"; sleep 110; done; }
heartbeat & HEARTBEAT_PID=$!
cleanup() { kill "$HEARTBEAT_PID" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Try to raise the idle timeout to the max (best-effort; ignore if gh/flag unsupported).
if command -v gh >/dev/null 2>&1 && [ -n "${CODESPACE_NAME:-}" ]; then
  gh codespace edit -c "$CODESPACE_NAME" --idle-timeout 240m >/dev/null 2>&1 \
    && echo ">> autopilot: idle timeout set to 240m" \
    || echo ">> autopilot: could not set idle timeout (set it to 240m in Codespaces settings)"
fi

# --- Prompts ----------------------------------------------------------------------------
read -r -d '' KICKOFF <<'PROMPT' || true
ultrathink. You are running UNATTENDED in a disposable Codespace. There is no human to ask.

1. Read AGENTS.md and docs/project/autonomous-run-playbook.md IN FULL, then follow the
   playbook exactly.
2. Before each item, run `git fetch origin` and RE-READ
   docs/project/autonomous-run-playbook.md and docs/project/AUTOPILOT-CONTROL.md from
   origin/main (`git show origin/main:<path>`) for new instructions or a redirect — obey
   them. Then BASE the new item's branch on origin/main, so it includes whatever merged
   since (your earlier items, other agents' work, David's redirects). Only STACK (base on a
   previous UNMERGED branch) when the item genuinely depends on that branch's code — say so
   in the PR. Open a PR, push the branch. Do NOT merge, and do NOT run a per-PR review.
3. The ENTIRE remaining backlog is ONE milestone. The milestone review runs ONCE at the
   very end (David runs it) — so as you go, flag any sole-committer/funding-path change in
   docs/project/DECISIONS-NEEDED.md for that final review; never launch a review yourself.
4. Per-PR gate = `make verify` green + envtest + the eviction fuzzer for engine/plugin/
   funding changes; mutation-verify each fix. That is enough to push.
5. Obey the PARK LIST: never make an owner decision (R7 pt2, R4 pt1b staleness bound,
   R4 pt2b, ROLES, or any new policy question). Record parked items in
   docs/project/DECISIONS-NEEDED.md and move on.
6. Record every implementation judgment call in
   docs/project/remediation/IMPLEMENTATION-LOG.md, and keep both boards
   (remediation/README.md, SIZING.md) in sync with what you land.

When every UNPARKED item is done or has an open PR (the milestone is complete), or you hit
a stop condition in the playbook, write a one-line summary to the file .autopilot-done at
the repo root and stop.
PROMPT

read -r -d '' CONTINUE <<'PROMPT' || true
ultrathink. Continue per docs/project/autonomous-run-playbook.md. First `git fetch origin`
and re-read the playbook + docs/project/AUTOPILOT-CONTROL.md from origin/main for any
redirect; obey it. Base each new item on origin/main (stack only genuinely-dependent items).
Obey the park list, no per-PR reviews. If every unparked item is done or has an open PR, or
you hit a stop condition, write a one-line summary to .autopilot-done and stop.
PROMPT

# --- Run loop ---------------------------------------------------------------------------
run_claude() {  # $1 = prompt, $2 = "first" | "resume"
  local prompt="$1" mode="$2"
  # --verbose streams the turn's progress live (tool calls, messages) so you can watch it
  # instead of staring at heartbeats. If your CLI version still buffers, switch the format to
  #   --output-format stream-json --verbose   (noisier JSON, but definitely streams).
  local args=(--dangerously-skip-permissions --model "$AUTOPILOT_MODEL" --verbose)
  [ "$mode" = "resume" ] && args+=(--continue)
  args+=(-p "$prompt")
  claude "${args[@]}" 2>&1 | tee "$LOG"
  return "${PIPESTATUS[0]}"
}

hit_usage_limit() {  # inspect the last turn's output
  grep -qiE "usage limit|rate limit|reset[s]? (at|in)|too many requests|quota (exceeded|reached)|429" "$LOG" 2>/dev/null
}

wait_for_reset() {
  local secs="$LIMIT_WAIT"
  # If the CLI printed an explicit reset time we could parse it here; absent that, wait the
  # fixed window. The heartbeat keeps emitting output throughout.
  echo ">> autopilot: usage limit reached — waiting $((secs/3600))h$(((secs%3600)/60))m for the reset, then resuming…"
  sleep "$secs"
  echo ">> autopilot: reset window elapsed; resuming."
}

mode="first"; prompt="$KICKOFF"
for i in $(seq 1 "$MAX_ITERS"); do
  if [ -f "$SENTINEL" ]; then echo ">> autopilot: DONE — $(cat "$SENTINEL")"; exit 0; fi
  git fetch origin --prune >/dev/null 2>&1 || true   # pick up merged work + redirects on origin/main
  echo ">> autopilot: turn $i/$MAX_ITERS (model=$AUTOPILOT_MODEL, mode=$mode) [origin/main @ $(git rev-parse --short origin/main 2>/dev/null || echo '?')]"

  run_claude "$prompt" "$mode"; rc=$?
  mode="resume"; prompt="$CONTINUE"

  if hit_usage_limit; then
    wait_for_reset
  elif [ "$rc" -ne 0 ]; then
    echo ">> autopilot: claude exited $rc (no limit signature); 60s backoff then retry"
    sleep 60
  fi
done

echo ">> autopilot: hit the ${MAX_ITERS}-turn cap without a done sentinel. Review open PRs and docs/project/DECISIONS-NEEDED.md."
