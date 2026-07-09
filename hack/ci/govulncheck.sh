#!/usr/bin/env bash
# Report vulnerabilities that are REACHABLE from jobtree's code.
#
# Dependabot counts advisories against everything in go.sum. That number is large,
# mostly transitive, and mostly irrelevant: the question is not "does a vulnerable
# version appear in the graph" but "can our code call the vulnerable function".
# govulncheck answers the second one. On this repo the two answers have differed by
# an order of magnitude.
#
# This is a PROBE, not a gate. It never fails the build: a new advisory published on
# a Sunday is not a reason for Monday's unrelated pull request to go red. The Tuesday
# maintenance workflow runs it and reports, which is the whole point of batching.
#
# Exit codes: 0 = nothing reachable outside the allowlist; 2 = something new.
# The last line of stdout is always `SUMMARY <new> <allowlisted>` for the caller.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ALLOWLIST="$ROOT/hack/ci/vuln-allowlist.txt"
REPORT="$(mktemp)"
trap 'rm -f "$REPORT"' EXIT

echo "==> govulncheck ./... (reachable only)"
go run golang.org/x/vuln/cmd/govulncheck@latest ./... > "$REPORT" 2>&1

# Do NOT key off the exit status. govulncheck exits 3 when it finds something, but
# `go run` swallows that: it prints "exit status 3" and exits 1 itself. A finding
# and a broken invocation would look identical. Key off the report instead — it
# always says one of these two things when the tool actually ran.
if ! grep -qE '^(=== Symbol Results ===|No vulnerabilities found)' "$REPORT"; then
  echo "::error::govulncheck did not produce a report; it failed to run" >&2
  sed -n '1,20p' "$REPORT" >&2
  echo "SUMMARY unknown unknown"
  exit 1
fi

# Each reachable finding is a "Vulnerability #N: GO-YYYY-NNNN" header followed by
# "Found in:" / "Fixed in:" lines. Pair them up.
mapfile -t found < <(awk '
  /^Vulnerability #/ { id = $3 }
  /^[[:space:]]*Found in:/ { pkg = $3 }
  /^[[:space:]]*Fixed in:/ { print id "\t" pkg "\t" $3 }
' "$REPORT")

allowed() { grep -qE "^${1}[[:space:]]" "$ALLOWLIST"; }

new=0
allowlisted=0
for entry in "${found[@]}"; do
  [ -n "$entry" ] || continue
  id="${entry%%$'\t'*}"
  rest="${entry#*$'\t'}"
  pkg="${rest%%$'\t'*}"
  fix="${rest##*$'\t'}"

  if allowed "$id"; then
    allowlisted=$((allowlisted + 1))
    echo "    allowlisted: $id  ($pkg)"
    continue
  fi
  new=$((new + 1))
  if [ "$fix" = "N/A" ]; then
    echo "::warning::REACHABLE, no fix available: $id in $pkg — decide, then add it to hack/ci/vuln-allowlist.txt with a reason"
  else
    echo "::warning::REACHABLE and fixable: $id in $pkg — fixed in $fix"
  fi
done

echo
if [ "$new" -eq 0 ]; then
  echo "no reachable vulnerabilities outside the allowlist (${allowlisted} allowlisted)"
elif [ "$new" -eq 1 ]; then
  echo "1 reachable vulnerability needs a decision (${allowlisted} allowlisted)"
else
  echo "${new} reachable vulnerabilities need a decision (${allowlisted} allowlisted)"
fi

echo "SUMMARY ${new} ${allowlisted}"
[ "$new" -eq 0 ] || exit 2
