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
# Steering from your phone: reply to the status issue with "@autopilot <instruction>"
# (e.g. "@autopilot do R26 next", "@autopilot stop after this item"). The next turn reads your
# latest @autopilot comment and obeys it. It stays in force until you post another one.
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
RESUME="${AUTOPILOT_RESUME:-true}"           # persist+resume the session across segments; false = clean slate
# One stable Claude session id for the whole re-dispatch chain: generated once on segment 0, threaded
# through every re-dispatch, and used to --resume the restored session so an interrupted harness run
# cache-hits instead of re-running its (paid) subagents. Also names the saved-session artifact.
SID="${AUTOPILOT_SID:-}"
[ -z "$SID" ] && SID="$(cat /proc/sys/kernel/random/uuid 2>/dev/null || uuidgen | tr 'A-Z' 'a-z')"
[ -n "${GITHUB_ENV:-}" ] && echo "AUTOPILOT_SID=$SID" >> "$GITHUB_ENV"   # the upload step names the artifact from this
# Keep the three saved-session roots present so the upload artifact's layout is stable (its archive
# is anchored at the common ancestor of the paths; a missing dir would shift it and break restore).
mkdir -p "$HOME/.claude/projects" "$HOME/.claude/tasks" "$HOME/.claude/sessions"
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
  # prefer the short --name label; else a trimmed slug of the task; else a date
  TITLE="${AUTOPILOT_NAME:-}"
  [ -z "$TITLE" ] && TITLE="$(printf '%s' "$TASK" | tr '\n' ' ' | sed -E 's/^ *//; s/ +/ /g' | cut -c1-60)"
  ISSUE="$(gh issue create --repo "$REPO" --title "🤖 ${TITLE:-Autopilot $(date -u +%Y-%m-%d)}" \
    --body $'Unattended autopilot. I post progress here as I go.\n\n**To steer me from your phone:** reply with **`@autopilot <instruction>`** — e.g. *"@autopilot do R26 next"*, *"@autopilot skip R13"*, *"@autopilot stop after this item"*. I read your latest `@autopilot` comment on my next turn and it stays in force until you post another one.\n\nThe `@autopilot` prefix matters: my own status notes are posted with the same account, so it is how I tell your instructions apart from my own chatter.\n\nI open PRs but do **not** merge; merge them yourself (the mobile app works).' \
    2>/dev/null | grep -oE '[0-9]+$' || true)"
fi
note $'▶️ **Segment '"$((CONT+1))"' started.** ['"[logs]($RUN_URL)"$'\n\n**Task:** '"$TASK"

# Latest owner directive: ONLY comments that open with "@autopilot" count.
#
# This used to be a blocklist — "anything not starting with a status emoji is David talking" — which
# worked while my own notes were terse ▶️/⏸/✅ lines. Then the scrum-note change made me post prose
# ("Reviewing the three lease-mint sites…"), which is indistinguishable from a human directive, so I
# began reading my OWN last note back as "OWNER DIRECTIVE … overrides priority" on every turn. (Seen
# live: a segment inherited its own "stand down" note and burned a turn reconciling it against its
# task.) Author can't discriminate either — I post with David's PAT, so my comments are authored by
# him with assoc=OWNER. An explicit marker is the one signal I won't accidentally emit; note I do
# write things like "Autopilot resumed (17:10Z)", so the bare word alone would reintroduce the bug.
#
# Also makes a directive STICKY: it stays in force through all my chatter until David posts another,
# instead of being silently replaced by my next status note.
owner_directive() {
  gh issue view "$ISSUE" --repo "$REPO" --json comments \
    --jq '[.comments[] | select(.body | test("^\\s*@autopilot\\b"; "i"))] | last
          | (.body // "") | sub("^\\s*@autopilot\\s*:?\\s*"; ""; "i")' 2>/dev/null
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
5. Post status like a developer giving standup notes — specific, useful, and paced like a human
   who's actually working, NOT a machine heartbeat. A reader should skim the issue and know exactly
   what happened and why. Post when you start something, at each REAL milestone, and when you finish
   or hit a blocker — never filler, never the same line twice. Good updates:
     "Reviewing the three lease-mint sites for the namespace derivation."
     "Found 2 issues: a missing PaidByNamespace on the hypothetical lease, and a golden that wasn't
      retopologized. Fixing the first now."
     "make verify + eviction fuzzer green; pushed the fix, #NN updated."
     "Blocked: the amendment doesn't say whether X — parking it in DECISIONS-NEEDED, moving on."
   Post with:  gh issue comment $ISSUE --repo $REPO --body '<your standup-style line>'
   Do NOT narrate mechanics (retries, waits) — the runner handles those. Quality over frequency:
   a handful of meaningful updates beats a stream of noise.
6. Record judgment calls in docs/project/remediation/IMPLEMENTATION-LOG.md; keep the boards in sync.

TASK: $TASK

When every unparked item is done or has an open PR, or you hit a stop condition, write a one-line
summary to .autopilot-done at the repo root and stop.
PROMPT

read -r -d '' CONT_MSG <<PROMPT || true
ultrathink. Continue per docs/project/autonomous-run-playbook.md. git fetch origin first. Keep
posting per-item progress to issue #$ISSUE, obey the park list, no per-PR reviews, do not merge.
If a Workflow adversarial-review run was in flight when the previous segment ended, RESUME it with
resumeFromRunId (the runId is in your earlier Workflow tool result, and the persisted script +
completed subagent journals are on disk under the session dir) rather than starting a new run — a
fresh run re-bills every subagent you already paid for. If everything is done or you hit a stop
condition, write .autopilot-done and stop.
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
  # Resume the pinned session if it already exists on this disk (a later turn in this job, or a
  # segment we just restored); otherwise create it with that id. Require it to be NON-EMPTY: a
  # truncated restore would make --resume fail, and the loop would misread that non-zero exit as
  # spent quota and stall in a wait cycle. A bad file is discarded and we start clean instead.
  local sflag sess
  sess="$(compgen -G "$HOME/.claude/projects/*/$SID.jsonl" 2>/dev/null | head -1)"
  if [ -n "$sess" ] && [ -s "$sess" ]; then
    sflag=(--resume "$SID")
  else
    [ -n "$sess" ] && { echo "  ⚠️ restored session $SID.jsonl is empty — starting a clean session."; rm -f "$sess"; }
    sflag=(--session-id "$SID")
  fi
  claude --dangerously-skip-permissions --model "$MODEL" "${sflag[@]}" \
         --output-format stream-json --verbose --settings "$SETTINGS" -p "$prompt" 2>&1 | tee "$LOG" | pretty
  local rc=${PIPESTATUS[0]}
  grep -qiE "usage limit|rate limit|reset[s]? (at|in)|too many requests|quota (exceeded|reached)" "$LOG" 2>/dev/null && LIMIT=1
  return "$rc"
}

redispatch() {
  if [ "$CONT" -lt "$MAX_CONT" ]; then
    note "⏸ $1 — re-dispatching segment $((CONT+2))."
    gh workflow run autopilot.yml --repo "$REPO" -f task="$TASK" -f issue="$ISSUE" -f cont="$((CONT+1))" \
      -f resume="$RESUME" -f sid="$SID" \
      >/dev/null 2>&1 || note "⚠️ re-dispatch failed — run \`gh workflow run autopilot.yml -f issue=$ISSUE\` yourself."
  else
    note "🛑 Hit the re-dispatch chain cap ($MAX_CONT). Stopping; re-launch manually if there's more."
  fi
}

# --- quota-aware waiting (all bash — Claude cannot think once it is limited) -------------
# Seconds until the usage-limit reset, parsed from a turn's own output ($LOG). Empty if unknown,
# in which case the caller polls hourly. Best-effort across phrasings; the hourly fallback is the
# guarantee, the parse is the optimisation.
parse_reset_seconds() {
  local now h m ts clock tgt line; now=$(date -u +%s)
  h=$(grep -oiE "in ([0-9]+) hours?" "$LOG" 2>/dev/null | grep -oE "[0-9]+" | tail -1)
  m=$(grep -oiE "in ([0-9]+) minutes?" "$LOG" 2>/dev/null | grep -oE "[0-9]+" | tail -1)
  if [ -n "$h" ] || [ -n "$m" ]; then echo $(( ${h:-0}*3600 + ${m:-0}*60 )); return; fi
  ts=$(grep -oiE "reset[^0-9]{0,24}(1[0-9]{9})" "$LOG" 2>/dev/null | grep -oE "1[0-9]{9}" | tail -1)
  if [ -n "$ts" ] && [ "$ts" -gt "$now" ]; then echo $(( ts - now )); return; fi
  line=$(grep -oiE "reset[s]?( at)? [0-9]{1,2}:[0-9]{2} ?(am|pm)?" "$LOG" 2>/dev/null | tail -1)
  if [ -n "$line" ]; then
    clock=$(echo "$line" | grep -oiE "[0-9]{1,2}:[0-9]{2} ?(am|pm)?")
    tgt=$(date -u -d "$clock" +%s 2>/dev/null || echo "")
    if [ -n "$tgt" ]; then [ "$tgt" -le "$now" ] && tgt=$((tgt+86400)); echo $(( tgt - now )); return; fi
  fi
  echo ""
}
# Pause until the quota returns, in-job so the session (and any in-flight review) survives. Posts
# a single "paused" note the first time (the loop posts "resumed" on the next success).
wait_for_quota() {
  local secs; secs="$(parse_reset_seconds)"
  if [ -n "$secs" ] && [ "$secs" -gt 0 ] && [ "$secs" -lt 21600 ]; then
    secs=$(( secs + 300 ))
    [ "$limited" -eq 0 ] && note "⏸ **Usage limit.** Its reset reads ~$((secs/60))m out — sleeping until then, then resuming this same run."
  else
    secs=3600
    [ "$limited" -eq 0 ] && note "⏸ **Usage limit** — no clear reset time in the output, so I'll retry **hourly** and pick up where I left off."
  fi
  limited=1
  if [ $(((SECONDS + secs)/60)) -lt "$DEADLINE_MIN" ]; then sleep "$secs"
  else redispatch "Quota wait exceeds this segment's budget — a fresh job keeps waiting"; exit 0; fi
}

# --- the loop ---------------------------------------------------------------------------
mode=first; waits=0; limited=0
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
    waits=$((waits+1)); wait_for_quota
  elif [ "$rc" -ne 0 ]; then
    # A non-zero exit with no matched limit text is, in THIS workload, almost always spent quota
    # whose message we didn't catch (that's what buried the last run in 310 retry lines). Treat it
    # as a limit and WAIT — hourly, quietly, resuming when quota returns — not spam, not an early
    # stand-down. A cap bounds a genuine hard error so it can't wait forever.
    waits=$((waits+1))
    if [ "$waits" -ge "${MAX_WAITS:-10}" ]; then
      note "🛑 **Standing down** after ${waits} quota-waits with no successful turn. Re-launch me once you know quota is back, or reply here."
      printf 'stood down after %s quota-waits\n' "$waits" > "$SENTINEL"
    else
      wait_for_quota
    fi
  else
    [ "$limited" -eq 1 ] && { note "▶️ **Quota's back — resuming.**"; limited=0; }
    waits=0
  fi
done

# --- done -------------------------------------------------------------------------------
note $'✅ **Done** — '"$(cat "$SENTINEL")"$'\n\nOpen PRs are ready for your review/merge.'
