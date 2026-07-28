#!/usr/bin/env bash
set -eu

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
  echo "expected TLC initial-state counterexample exit 12, got $status" >&2
  exit 1
fi
if ! grep -F "Invariant $invariant is violated by the initial state:" "$log" >/dev/null; then
  echo "TLC did not report the expected initial-state witness for $invariant" >&2
  exit 1
fi
if ! grep -F "/\\ podState =" "$log" >/dev/null ||
   ! grep -F "/\\ machineLive =" "$log" >/dev/null ||
   ! grep -F "/\\ leaseOpen =" "$log" >/dev/null; then
  echo "TLC named $invariant but did not print the required physical/lease witness state" >&2
  exit 1
fi

echo "validated expected initial-state witness: $invariant"
