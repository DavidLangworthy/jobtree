#!/usr/bin/env bash
# The Tuesday fix-up: merge what is provably safe, hold what is not, and always
# say which was which.
#
# Called by .github/workflows/weekly-maintenance.yaml. The rules it enforces are
# explained in docs/project/weekly-maintenance.md.
set -euo pipefail

REPO="${GITHUB_REPOSITORY:-DavidLangworthy/jobtree}"
GATE_RESULT="${GATE_RESULT:-failure}"
ENVTEST_FLAKES="${ENVTEST_FLAKES:-unknown}"
RUN_URL="${RUN_URL:-}"
LOG_ISSUE_TITLE="Weekly maintenance log"

# The checks the branch ruleset requires. A pull request is merged only when
# EVERY one of these has concluded SUCCESS. We check them ourselves rather than
# using `gh pr merge --auto`, which depends on a repository setting that someone
# could turn off, and which would merge immediately if the required-checks rule
# were ever removed. Verifying explicitly fails closed.
REQUIRED_CHECKS=("ci" "kind e2e (real cluster)")

merged=()
held=()

gate_ok() { [ "$GATE_RESULT" = "success" ]; }

# --- 1. Triage Dependabot's open pull requests --------------------------------
#
# Ecosystem is read from the branch name Dependabot creates:
#   dependabot/go_modules/...     -> NEVER auto-merged
#   dependabot/github_actions/... -> auto-merge when fully green
#   dependabot/docker/...         -> auto-merge when fully green

triage_prs() {
  local prs
  prs="$(gh pr list --repo "$REPO" --author 'app/dependabot' --state open \
          --json number,headRefName,title \
          --jq '.[] | [.number, .headRefName, .title] | @tsv' || true)"
  [ -n "$prs" ] || return 0

  gh label create needs-human --repo "$REPO" --color D93F0B \
    --description "A human (or an adversarial review) must look at this" >/dev/null 2>&1 || true

  while IFS=$'\t' read -r num ref title; do
    [ -n "${num:-}" ] || continue

    # A Go dependency bump can change funding behaviour. The golden oracle
    # captures class widths and lenders, NOT the wall-clock-derived GPU-hour
    # floats, so a green suite does not prove the accrual is unchanged. These
    # always get a human.
    if [[ "$ref" == dependabot/go_modules/* ]]; then
      gh pr edit "$num" --repo "$REPO" --add-label needs-human >/dev/null 2>&1 || true
      held+=("#${num} — \`gomod\`: a Go dependency bump can change funding behaviour, and the golden oracle captures widths, not GPU-hours. Needs a human, and an adversarial review if it touches \`pkg/funding\`'s dependencies.")
      continue
    fi

    if ! gate_ok; then
      held+=("#${num} — held: \`make verify\` did not pass on \`main\` this week, so nothing was merged.")
      continue
    fi

    local checks all_green=1 state
    checks="$(gh pr checks "$num" --repo "$REPO" --json name,state 2>/dev/null || echo '[]')"
    for required in "${REQUIRED_CHECKS[@]}"; do
      state="$(jq -r --arg n "$required" '[.[] | select(.name == $n) | .state] | first // "MISSING"' <<<"$checks")"
      if [ "$state" != "SUCCESS" ]; then
        all_green=0
        held+=("#${num} — required check \`${required}\` is \`${state}\`, not \`SUCCESS\`.")
        break
      fi
    done

    if [ "$all_green" -eq 1 ]; then
      if gh pr merge "$num" --repo "$REPO" --merge --delete-branch >/dev/null 2>&1; then
        merged+=("#${num} — ${title}")
      else
        held+=("#${num} — all checks green, but the merge was refused (branch rules, or a conflict).")
      fi
    fi
  done <<<"$prs"
}

triage_prs

# --- 2. Compose one report. Always. -------------------------------------------
#
# A week with no comment means this workflow did not run. That must be visible,
# so we post even when there is nothing to say.

body="## Weekly maintenance — $(date -u '+%Y-%m-%d')"$'\n\n'

if gate_ok; then
  body+="**Gate:** \`make verify\` passed on \`main\`."$'\n'
else
  body+="**Gate: FAILED.** \`make verify\` did not pass on \`main\`. **Nothing was merged.** This is the first thing to look at."$'\n'
fi

case "$ENVTEST_FLAKES" in
  0)       body+="**envtest flake probe:** 3/3 clean."$'\n' ;;
  1|2|3)   body+="**envtest flake probe:** failed **${ENVTEST_FLAKES}/3** runs — consistent with the known stale-node-failure flake (task #36). If this climbs, it is no longer a flake."$'\n' ;;
  *)       body+="**envtest flake probe:** did not report (\`${ENVTEST_FLAKES}\`). Treat as unknown, not as clean."$'\n' ;;
esac

body+=$'\n'
if [ "${#merged[@]}" -gt 0 ]; then
  body+="### Merged (${#merged[@]})"$'\n'
  for m in "${merged[@]}"; do body+="- ${m}"$'\n'; done
else
  body+="### Merged (0)"$'\n'"Nothing was safe to merge automatically."$'\n'
fi

body+=$'\n'
if [ "${#held[@]}" -gt 0 ]; then
  body+="### Held for you (${#held[@]})"$'\n'
  for h in "${held[@]}"; do body+="- ${h}"$'\n'; done
else
  body+="### Held for you (0)"$'\n'"Nothing is waiting."$'\n'
fi

body+=$'\n'"---"$'\n'"Rules this run followed: [\`docs/project/weekly-maintenance.md\`](${GITHUB_SERVER_URL:-https://github.com}/${REPO}/blob/main/docs/project/weekly-maintenance.md)."$'\n'
# NB: `[ -n "$X" ] && body+=...` would exit under `set -e` when X is empty —
# the report would be silently skipped. Use an explicit if.
if [ -n "$RUN_URL" ]; then
  body+="Workflow run: ${RUN_URL}"$'\n'
fi

issue="$(gh issue list --repo "$REPO" --state open --search "\"$LOG_ISSUE_TITLE\" in:title" \
          --json number --jq '.[0].number // empty' || true)"

if [ -z "$issue" ]; then
  # `gh issue create` prints the new issue's URL; the number is its last segment.
  issue="$(gh issue create --repo "$REPO" --title "$LOG_ISSUE_TITLE" \
    --body "One comment per week from the Tuesday maintenance workflow. Pin this issue; it is the only notification the routine produces." \
    | grep -oE '[0-9]+$')"
fi

gh issue comment "$issue" --repo "$REPO" --body "$body"
echo "Reported on issue #${issue}"

# Fail the run when the gate failed, so a red X appears next to the week.
gate_ok || { echo "::error::make verify failed on main; nothing merged"; exit 1; }
