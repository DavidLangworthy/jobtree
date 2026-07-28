#!/usr/bin/env bash
set -u

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <module.tla> <config.cfg> <invariant>" >&2
  exit 2
fi

module="$1"
config="$2"
invariant="$3"
apalache="${APALACHE:-.cache/apalache/bin/apalache-mc}"
length="${APALACHE_LENGTH:-1}"
heap="${APALACHE_JVM_ARGS:--Xmx5500m}"
log="$(mktemp)"
trap 'rm -f "$log"' EXIT

set +e
JVM_ARGS="$heap" "$apalache" check \
  --config="$config" \
  --length="$length" \
  --no-deadlock \
  "$module" >"$log" 2>&1
status=$?
set -e

cat "$log"

if [ "$status" -ne 12 ]; then
  echo "expected Apalache counterexample exit 12, got $status" >&2
  exit 1
fi
if ! grep -F "Using inv predicate(s) $invariant" "$log" >/dev/null; then
  echo "Apalache did not select the expected invariant $invariant" >&2
  exit 1
fi
if ! grep -F "state invariant 0 violated" "$log" >/dev/null; then
  echo "Apalache did not report the sole configured invariant as violated" >&2
  exit 1
fi
if ! grep -F "Check the trace in:" "$log" >/dev/null; then
  echo "Apalache returned an error without a counterexample trace" >&2
  exit 1
fi

echo "validated expected counterexample: $config violates $invariant"
