#!/usr/bin/env bash
# The PR-time e2e images (Dockerfile.fast) wrap binaries compiled on the CI runner
# rather than inside the production Dockerfile's golang stage. That is only honest
# if the binary is byte-for-byte the one we ship — i.e. if both paths compile with
# the SAME flags.
#
# This asserts exactly that, and fails `make verify` if the two ever drift. Without
# it, someone edits the Dockerfile's ldflags, PR e2e keeps testing the old shape,
# and nobody hears about it — the silent-pass pattern this repo keeps paying for.
set -euo pipefail

ENV_STR='CGO_ENABLED=0 GOOS=linux'
FLAG_STR='-trimpath -ldflags="-s -w"'

fail() { echo "::error::$*" >&2; exit 1; }

grep -qF -- "$ENV_STR"  Dockerfile || fail "Dockerfile no longer builds with '$ENV_STR'"
grep -qF -- "$FLAG_STR" Dockerfile || fail "Dockerfile no longer builds with '$FLAG_STR'"

grep -qF -- "GO_BUILD_ENV := $ENV_STR" Makefile \
  || fail "Makefile's GO_BUILD_ENV drifted from the Dockerfile's '$ENV_STR'"
grep -qF -- 'GO_BUILD_FLAGS := -trimpath -ldflags="$(GO_LDFLAGS)"' Makefile \
  || fail "Makefile's GO_BUILD_FLAGS is not the '-trimpath -ldflags=...' shape the Dockerfile uses"
grep -qF -- 'GO_LDFLAGS := -s -w' Makefile \
  || fail "Makefile's GO_LDFLAGS drifted from the Dockerfile's '-s -w'"

# Dockerfile.fast must NOT compile anything — it only wraps prebuilt binaries.
if grep -qE '^\s*(RUN|FROM golang)' Dockerfile.fast; then
  fail "Dockerfile.fast must only COPY prebuilt binaries; it must never compile"
fi

echo "build flags agree between Makefile and Dockerfile"

# The fast path only produces the shipped binary if the RUNNER's Go matches the
# Dockerfile's. We found this the hard way: the runner had 1.26.4 and `golang:1.26`
# floated to 1.26.5, so the two binaries differed (same size, different sha256).
# Go builds are reproducible given the same toolchain and -trimpath; pin both.
GO_VER="$(sed -nE 's/^FROM golang:([0-9.]+) AS build.*/\1/p' Dockerfile)"
[ -n "$GO_VER" ] || fail "Dockerfile's golang base image must pin an exact patch version (FROM golang:X.Y.Z)"
for wf in .github/workflows/ci.yaml .github/workflows/e2e.yaml .github/workflows/release.yaml; do
  grep -qF -- "go-version: '$GO_VER'" "$wf" \
    || fail "$wf pins a different Go than the Dockerfile's $GO_VER (or uses a floating '1.26.x')"
done
echo "Go toolchain pinned to $GO_VER in the Dockerfile and every workflow"
