#!/usr/bin/env bash
#
# actions-autopilot.sh — the unattended loop, for GitHub Actions (see .github/workflows/autopilot.yml).
#
# Grinds a task with headless Claude on your Max quota, posts progress to a GitHub Issue you can
# watch and steer from your phone, and re-dispatches itself to continue past the 6h per-job cap.
# It does NOT merge — it opens PRs and posts them to the issue; you merge (protected `main` stays
# yours; the GitHub mobile app merges PRs fine). The `hack/autopilot-settings.json` guardrail is
# passed to Claude so even an errant turn cannot `gh pr merge` or do anything destructive.
#
# Steering from your phone: reply to the status issue with a directive
# ("do R26 next", "stop after this item", "skip R13"). The next turn reads your latest comment
# and obeys it.
#
# NOTE: this is a first-cut. The likely first-run snags are the Claude install/flags and the Go
# toolchain for `make verify`; watch the first segment's logs and adjust.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

: "${GH_TOKEN:?need GH_TOKEN (the AUTOPILOT_PAT)}"
: "${GITHUB_REPOSITORY:?}"
command -v claude >/dev/null 2>&1 || { echo "claude CLI not found"; exit 1; }

REPO="$GITHUB_REPOSITORY"
TASK="${AUTOPILOT_TASK:-}"
ISSUE="${AUTOPILOT_ISSUE:-}"
CONT="${AUTOPILOT_CONT:-0}"
MODEL="${AUTOPILOT_MODEL:-opus}"
export MAX_THINKING_TOKENS="${MAX_THINKING_TOKENS:-31999}"

DEADLINE_MIN="${AUTOPILOT_DEADLINE_MIN:-330}"   # re-dispatch after 5h30m of work (< the 6h cap)
MAX_CONT="${AUTOPILOT_MAX_CONT:-24}"            # cap the re-dispatch chain (safety backstop)
LIMIT_SLEEP="${AUTOPILOT_LIMIT_SLEEP:-1200}"    # 20m nap on a usage-limit, then retry in-job
SETTINGS="$(git rev-parse --show-toplevel)/hack/autopilot-settings.json"
SENTINEL=".autopilot-done"; rm -f "$SENTINEL"
LOG=".autopilot-turn.log"

[ -z "$TASK" ] && TASK="Work the OPEN remediation items per docs/project/autonomous-run-playbook.md, in priority order."
RUN_URL="${GITHUB_SERVER_URL:-https://github.com}/$REPO/actions/runs/${GITHUB_RUN_ID:-}"

note() { [ -n "$ISSUE" ] && gh issue comment "$ISSUE" --repo "$REPO" --body "$1" >/dev/null 2>&1 || true; }

# --- status issue -----------------------------------------------------------------------
if [ -z "$ISSUE" ]; then
  # descriptive title from the task's first line (Claude refines it on turn 1); fall back to a date
  TITLE_SLUG="$(printf '%s' "$TASK" | tr '\n' ' ' | sed -E 's/^ *//; s/ +/ /g' | cut -c1-80)"
  ISSUE="$(gh issue create --repo "$REPO" --title "🤖 Autopilot — ${TITLE_SLUG:-$(date -u +%Y-%m-%d)}" \
    --body $'Unattended autopilot. I post progress here as I go.\n\n**To steer me from your phone:** reply with a directive — e.g. *"do R26 next"*, *"skip R13"*, *"stop after this item"* — and I obey it on my next turn.\n\nI open PRs but do **not** merge; merge them yourself (the mobile app works).' \
    2>/dev/null | grep -oE '[0-9]+$' || true)"
fi
note $'▶️ **Segment '"$((CONT+1))"' started.** ['"[logs]($RUN_URL)"$'\n\n**Task:** '"$TASK"

# latest human directive on the issue (skip my own ▶️/✅/⏸/🅿️/🛑 status comments)
owner_directive() {
  gh issue view "$ISSUE" --repo "$REPO" --json comments \
    --jq '[.comments[] | select(.body | test("^(▶️|✅|⏸|🅿️|🛑|⚠️|🤖)") | not)] | last | .body // ""' 2>/dev/null
}

# --- prompts ----------------------------------------------------------------------------
read -r -d '' KICK <<PROMPT || true
ultrathink. You are running UNATTENDED in GitHub Actions. There is no human to ask.

1. Read AGENTS.md and docs/project/autonomous-run-playbook.md IN FULL, then follow the playbook.
   First, give this status issue a concise, descriptive title summarising the task:
     gh issue edit $ISSUE --repo $REPO --title '🤖 <5-8 word summary>'
2. Do the TASK below. One PR per item, based on origin/main (git fetch first). Push each branch
   and open a PR. Do NOT merge, and do NOT run a per-PR adversarial review.
3. Obey the PARK LIST — never make an owner decision (R7 pt2, R4 pt1b staleness bound, R4 pt2b,
   ROLES, or any new policy question). Record parked items in docs/project/DECISIONS-NEEDED.md
   AND post a '🅿️ parked: <item> — <why>' comment to issue #$ISSUE.
4. Per-PR gate = make verify green (+ envtest, + eviction fuzzer for engine/plugin/funding), and
   mutation-verify each fix. That is enough to push.
5. Post short progress comments to the status issue so it shows a live pulse — NOT just at the
   end. Post when you START an item, at milestones inside a long one (design read; code done;
   'make verify' passing; pushing), and when you finish:
     gh issue comment $ISSUE --repo $REPO --body '🔧 R7 pt2: make verify green, opening the PR'
     gh issue comment $ISSUE --repo $REPO --body '✅ R7 pt2 — https://github.com/$REPO/pull/NN'
   (use ⚠️ for a blocker). One line each; a big item should tick a few times before it's done.
6. Record judgment calls in docs/project/remediation/IMPLEMENTATION-LOG.md; keep the boards in sync.

TASK: $TASK

When every unparked item is done or has an open PR, or you hit a stop condition, write a one-line
summary to .autopilot-done at the repo root and stop.
PROMPT

read -r -d '' CONT_MSG <<PROMPT || true
ultrathink. Continue per docs/project/autonomous-run-playbook.md. git fetch origin first. Keep
posting per-item progress to issue #$ISSUE, obey the park list, no per-PR reviews, do not merge.
If everything is done or you hit a stop condition, write .autopilot-done and stop.
PROMPT

# --- one Claude turn; sets LIMIT=1 on a usage-limit signature ----------------------------
LIMIT=0
# Render stream-json events as readable lines in the Actions log — LIVE, not buffered to the
# turn's end the way plain --verbose is. Raw stream is still tee'd to $LOG for the limit grep.
pretty() {
  command -v jq >/dev/null 2>&1 || { cat; return; }
  jq -Rr --unbuffered '
    (try fromjson catch null) as $j |
    if   $j == null           then .
    elif $j.type=="assistant" then ($j.message.content[]? |
           if   .type=="text"     then (.text | gsub("\n";" ") | .[0:200])
           elif .type=="tool_use" then "  🔧 " + .name + " " +
                  ((.input.command // .input.file_path // .input.pattern // .input.description // "") | tostring | .[0:140])
           else empty end)
    elif $j.type=="result"    then "  ── turn done (" + ($j.subtype // "ok") + ")"
    else empty end' 2>/dev/null
}
run_turn() {
  local prompt="$1"; LIMIT=0
  claude --dangerously-skip-permissions --model "$MODEL" \
         --output-format stream-json --verbose --settings "$SETTINGS" -p "$prompt" 2>&1 | tee "$LOG" | pretty
  local rc=${PIPESTATUS[0]}
  grep -qiE "usage limit|rate limit|reset[s]? (at|in)|too many requests|quota (exceeded|reached)" "$LOG" 2>/dev/null && LIMIT=1
  return "$rc"
}

redispatch() {
  if [ "$CONT" -lt "$MAX_CONT" ]; then
    note "⏸ $1 — re-dispatching segment $((CONT+2))."
    gh workflow run autopilot.yml --repo "$REPO" -f task="$TASK" -f issue="$ISSUE" -f cont="$((CONT+1))" \
      >/dev/null 2>&1 || note "⚠️ re-dispatch failed — run \`gh workflow run autopilot.yml -f issue=$ISSUE\` yourself."
  else
    note "🛑 Hit the re-dispatch chain cap ($MAX_CONT). Stopping; re-launch manually if there's more."
  fi
}

# --- the loop ---------------------------------------------------------------------------
mode=first
while true; do
  [ -f "$SENTINEL" ] && break

  # time budget for this job segment
  if [ $((SECONDS/60)) -ge "$DEADLINE_MIN" ]; then
    redispatch "Segment time budget reached"
    exit 0
  fi

  # fold in any phone directive
  DIR="$(owner_directive)"; EXTRA=""
  [ -n "$DIR" ] && EXTRA=$'\n\nOWNER DIRECTIVE from the status issue (obey it, it overrides priority): '"$DIR"

  if [ "$mode" = first ]; then PROMPT="$KICK$EXTRA"; else PROMPT="$CONT_MSG$EXTRA"; fi
  run_turn "$PROMPT"; rc=$?
  mode=resume

  if [ "$LIMIT" -eq 1 ]; then
    # Max-quota reset is on a ~5h window. Napping fits inside a 6h job if the limit hit early;
    # if we're near the budget, re-dispatch so a fresh segment retries once quota returns.
    if [ $(((SECONDS + LIMIT_SLEEP)/60)) -lt "$DEADLINE_MIN" ]; then
      note "⏸ Usage limit — napping $((LIMIT_SLEEP/60))m, then retrying."
      sleep "$LIMIT_SLEEP"
    else
      redispatch "Usage limit near the segment budget"
      exit 0
    fi
  elif [ "$rc" -ne 0 ]; then
    note "⚠️ Claude turn exited $rc (no limit signature). Brief backoff, then retry."
    sleep 60
  fi
done

# --- done -------------------------------------------------------------------------------
note $'✅ **Done** — '"$(cat "$SENTINEL")"$'\n\nOpen PRs are ready for your review/merge.'
