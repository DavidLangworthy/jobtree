#!/usr/bin/env bash
set -u

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <invariant> <tlc-command...>" >&2
  exit 2
fi

invariant="$1"
shift
log="$(mktemp)"
trap 'rm -f "$log"' EXIT

set +e
"$@" >"$log" 2>&1
status=$?
set -e

cat "$log"

if [ "$status" -ne 12 ]; then
  echo "expected TLC counterexample exit 12, got $status" >&2
  exit 1
fi
if ! grep -F "Invariant $invariant is violated." "$log" >/dev/null; then
  echo "TLC did not report the expected invariant $invariant" >&2
  exit 1
fi
if ! grep -F "The behavior up to this point is:" "$log" >/dev/null; then
  echo "TLC returned an error without a counterexample behavior" >&2
  exit 1
fi

echo "validated expected counterexample: $invariant"
